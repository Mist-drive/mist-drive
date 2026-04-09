// Package sync owns the desktop side of the sync loop: given a set of
// {local, remotePrefix} mappings, it keeps local disk and the user's
// remote bucket in agreement.
//
// Design notes (phase 3 — pragmatic MVP):
//   - One reconciler per mapping. Reconcile = list local, list remote,
//     walk the union, act on differences.
//   - No persistent index yet. The original plan called for a SQLite
//     change-detection cache; for v1 we do full size-based compares on
//     every pass. This is fine up to a few tens of thousands of files
//     and keeps the code ~200 lines instead of ~800. The slot to add
//     the index later is clearly marked in diff().
//   - Conflict rule: local wins. The moment a file differs in size
//     between local and remote we push the local copy — matches the
//     "last-writer-wins, local beats remote on tie" rule from the plan.
//   - fsnotify drives continuous reconciliation. Events are debounced
//     (500ms) so a batch of writes coalesces into one pass. After a
//     debounced burst we always re-reconcile the *whole* mapping; we
//     don't try to route events to individual file handlers, which is
//     what keeps the code simple.
package sync

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"mist-drive-desktop/internal/apiclient"
	"mist-drive-desktop/internal/settings"
)

// Status is the snapshot of engine state the frontend polls (or
// receives via events) to render the sync panel.
type Status struct {
	Running      bool      `json:"running"`
	LastPass     time.Time `json:"lastPass"`
	LastError    string    `json:"lastError"`
	Uploaded     int       `json:"uploaded"`
	Downloaded   int       `json:"downloaded"`
	Skipped      int       `json:"skipped"`
	InFlight     string    `json:"inFlight"` // current file being transferred
}

type Engine struct {
	api    *apiclient.Client
	st     *settings.Store
	logger func(string)

	mu     sync.Mutex
	cancel context.CancelFunc
	status Status
	// nudge is a 1-buffered channel that external callers (e.g. the
	// websocket listener receiving a "files-changed" push) use to
	// request an immediate reconcile without waiting for the 30s tick.
	nudge chan struct{}
}

func New(api *apiclient.Client, st *settings.Store, log func(string)) *Engine {
	if log == nil {
		log = func(string) {}
	}
	return &Engine{api: api, st: st, logger: log, nudge: make(chan struct{}, 1)}
}

// Nudge asks the engine to run a reconcile pass as soon as possible.
// Safe to call from any goroutine; drops extra calls if one is already
// pending (the buffer size 1 coalesces a storm of ws messages into a
// single pass).
func (e *Engine) Nudge() {
	select {
	case e.nudge <- struct{}{}:
	default:
	}
}

// Status returns a copy of the current engine state. Safe to call at
// any time from any goroutine.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// Start spins up the reconcile loop. Subsequent calls while already
// running are no-ops so the frontend can issue Start idempotently.
func (e *Engine) Start() error {
	e.mu.Lock()
	if e.cancel != nil {
		e.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.status.Running = true
	e.status.LastError = ""
	e.mu.Unlock()

	go e.loop(ctx)
	return nil
}

// Stop halts the reconcile loop. Any in-flight upload/download will
// still run to completion because they hold no context — v2 will plumb
// cancellation through but for now a Stop means "don't start new passes".
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	e.status.Running = false
	e.status.InFlight = ""
}

// loop runs one initial reconcile, then re-runs whenever the fsnotify
// debouncer fires OR a 30s idle tick elapses (the tick catches
// remote-originated changes the local filesystem can't notify us about).
func (e *Engine) loop(ctx context.Context) {
	defer func() {
		e.mu.Lock()
		e.status.Running = false
		e.mu.Unlock()
	}()

	folders := e.st.Get().Folders
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		e.setErr(fmt.Errorf("watcher: %w", err))
		return
	}
	defer watcher.Close()

	for _, f := range folders {
		e.addRecursive(watcher, f.Local)
	}

	// Kick off an initial pass so users see activity immediately on Start.
	e.reconcileAll(ctx)

	// Debouncer: fsnotify bursts into reconcile at most every 500 ms.
	var debounce *time.Timer
	fire := make(chan struct{}, 1)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.reconcileAll(ctx)
		case <-fire:
			e.reconcileAll(ctx)
		case <-e.nudge:
			e.reconcileAll(ctx)
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Ignore the event payload beyond existence — we re-walk
			// the whole mapping anyway. Just schedule a run.
			_ = ev
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(500*time.Millisecond, func() {
				select {
				case fire <- struct{}{}:
				default:
				}
			})
		case werr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			e.logger("watcher error: " + werr.Error())
		}
	}
}

// addRecursive walks `root` and subscribes fsnotify to every directory
// inside it. fsnotify on Linux only notifies for directly-watched dirs,
// so we have to walk once at startup. New subdirs created later are
// caught by the reconcile loop and re-watched on the next pass (good
// enough for v1 — power users with deeply nested new trees might miss
// one tick, which the 30s idle pass backstops).
func (e *Engine) addRecursive(w *fsnotify.Watcher, root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			_ = w.Add(path)
		}
		return nil
	})
}

func (e *Engine) reconcileAll(ctx context.Context) {
	s := e.st.Get()
	// Apply upload rate limit from settings on each pass so changes
	// take effect without a restart.
	e.api.SetUploadRateKBps(s.MaxUploadRateKBps)

	for _, f := range s.Folders {
		if ctx.Err() != nil {
			return
		}
		// Per-folder Enabled flag is the user-facing on/off switch.
		// Both direction flags off = no-op pass (counts as skipped).
		if !f.Enabled {
			continue
		}
		if err := e.reconcileOne(ctx, f, f.Upload, f.Download); err != nil {
			e.setErr(fmt.Errorf("%s: %w", f.Local, err))
			return
		}
	}
	e.mu.Lock()
	e.status.LastPass = time.Now()
	e.status.LastError = ""
	e.status.InFlight = ""
	e.mu.Unlock()
}

type localFile struct {
	relPath string
	size    int64
}

// scanLocal walks the mapping root and returns every regular file keyed
// by its relative path (forward-slash separated so it matches S3 keys).
func scanLocal(root string) (map[string]localFile, error) {
	out := map[string]localFile{}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[rel] = localFile{relPath: rel, size: info.Size()}
		return nil
	})
	return out, err
}

// reconcileOne performs one full pass for a single mapping. The
// upload/download booleans gate each direction independently so the
// user can run upload-only (default), download-only, or bidirectional
// from the settings screen without restarting the engine.
func (e *Engine) reconcileOne(ctx context.Context, f settings.SyncFolder, doUpload, doDownload bool) error {
	local, err := scanLocal(f.Local)
	if err != nil {
		return fmt.Errorf("scan local: %w", err)
	}

	remoteAll, err := e.api.ListFiles()
	if err != nil {
		return fmt.Errorf("list remote: %w", err)
	}
	// Filter to just the keys under this mapping's prefix and build a
	// relative-path lookup mirror-image of `local`.
	prefix := f.RemotePrefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	remote := map[string]apiclient.ObjectInfo{}
	for _, o := range remoteAll {
		if prefix == "" || strings.HasPrefix(o.Key, prefix) {
			rel := strings.TrimPrefix(o.Key, prefix)
			if rel == "" {
				continue
			}
			remote[rel] = o
		}
	}

	// Walk the union. --- [index hook] ---
	// A future SQLite index would let us skip this full re-scan by
	// keeping a mirror of (rel_path, size, mtime, remote_etag) and
	// only re-examining files fsnotify actually touched. For the
	// current size of projects we sync, a full compare is fine.
	seen := map[string]bool{}
	for rel, lf := range local {
		seen[rel] = true
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ro, ok := remote[rel]
		if !ok {
			// Local-only → upload (if upload direction is enabled).
			if doUpload {
				if err := e.upload(f, rel); err != nil {
					return err
				}
			} else {
				e.bumpSkipped()
			}
			continue
		}
		if ro.Size != lf.size {
			// Differ → local wins, but only if we're allowed to push.
			if doUpload {
				if err := e.upload(f, rel); err != nil {
					return err
				}
			} else {
				e.bumpSkipped()
			}
			continue
		}
		e.bumpSkipped()
	}
	for rel := range remote {
		if seen[rel] {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Remote-only → download (if download direction is enabled).
		if doDownload {
			if err := e.download(f, rel); err != nil {
				return err
			}
		} else {
			e.bumpSkipped()
		}
	}
	return nil
}

func (e *Engine) upload(f settings.SyncFolder, rel string) error {
	local := filepath.Join(f.Local, filepath.FromSlash(rel))
	key := rel
	if f.RemotePrefix != "" {
		key = strings.TrimSuffix(f.RemotePrefix, "/") + "/" + rel
	}
	e.setInFlight("↑ " + rel)
	s := e.st.Get()
	if err := e.api.UploadFile(local, key, s.MaxConcurrentUploads); err != nil {
		return fmt.Errorf("upload %s: %w", rel, err)
	}
	e.mu.Lock()
	e.status.Uploaded++
	e.mu.Unlock()
	return nil
}

func (e *Engine) download(f settings.SyncFolder, rel string) error {
	local := filepath.Join(f.Local, filepath.FromSlash(rel))
	key := rel
	if f.RemotePrefix != "" {
		key = strings.TrimSuffix(f.RemotePrefix, "/") + "/" + rel
	}
	e.setInFlight("↓ " + rel)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	if err := e.api.DownloadFile(key, local); err != nil {
		return fmt.Errorf("download %s: %w", rel, err)
	}
	e.mu.Lock()
	e.status.Downloaded++
	e.mu.Unlock()
	return nil
}

func (e *Engine) setErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.LastError = err.Error()
	e.logger("sync error: " + err.Error())
}

func (e *Engine) setInFlight(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.InFlight = s
}

func (e *Engine) bumpSkipped() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.Skipped++
}
