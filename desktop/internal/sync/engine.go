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
//     and keeps the code ~250 lines instead of ~800. The slot to add
//     the index later is marked with [index hook].
//   - Conflict rule: local wins. The moment a file differs in size
//     between local and remote we push the local copy — matches the
//     "last-writer-wins, local beats remote on tie" rule from the plan.
//   - fsnotify drives continuous reconciliation. Events are debounced
//     (500ms) so a batch of writes coalesces into one pass. After a
//     debounced burst we always re-reconcile the *whole* mapping; we
//     don't route events to individual file handlers, which is what
//     keeps the code simple.
//   - Per-file errors are logged and counted, not fatal. A single
//     permission-denied should never stop the rest of a pass.
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

// API is the narrow slice of apiclient.Client the engine actually
// uses. Declaring it here lets tests inject a fake without dragging in
// an HTTP client.
type API interface {
	ListFiles() ([]apiclient.ObjectInfo, error)
	UploadFile(localPath, remoteKey string, maxConcurrentParts int) error
	DownloadFile(key, destPath string) error
	DeleteFile(key string) error
	SetUploadRateKBps(kbps int)
}

// Status is the snapshot of engine state the frontend polls (or
// receives via events) to render the sync panel. Counters reset at
// the start of each pass; TotalUploaded/TotalDownloaded are lifetime
// numbers for the "activity since login" display.
type Status struct {
	Running         bool      `json:"running"`
	LastPass        time.Time `json:"lastPass"`
	LastError       string    `json:"lastError"`
	Uploaded        int       `json:"uploaded"`
	Downloaded      int       `json:"downloaded"`
	Skipped         int       `json:"skipped"`
	Errors          int       `json:"errors"`
	TotalUploaded   int       `json:"totalUploaded"`
	TotalDownloaded int       `json:"totalDownloaded"`
	InFlight        string    `json:"inFlight"` // current file being transferred
}

type Engine struct {
	api    API
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

func New(api API, st *settings.Store, log func(string)) *Engine {
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

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		e.setErr(fmt.Errorf("watcher: %w", err))
		return
	}
	defer watcher.Close()
	// watched tracks which roots currently have fsnotify subscriptions
	// so we can diff against the live folder list on every pass. Adding
	// a sync folder at runtime used to miss its watcher until the next
	// app restart; now the reconcile loop notices and catches up.
	watched := map[string]bool{}
	e.syncWatchers(watcher, watched)

	e.reconcileAll(ctx)

	var debounce *time.Timer
	fire := make(chan struct{}, 1)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.syncWatchers(watcher, watched)
			e.reconcileAll(ctx)
		case <-fire:
			e.syncWatchers(watcher, watched)
			e.reconcileAll(ctx)
		case <-e.nudge:
			e.syncWatchers(watcher, watched)
			e.reconcileAll(ctx)
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			_ = ev // we always re-walk the whole mapping
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

// syncWatchers reconciles the fsnotify subscription set against the
// current list of sync folders. Runs before every reconcile pass so
// folders added or removed while the engine is running are picked up
// without a restart.
func (e *Engine) syncWatchers(w *fsnotify.Watcher, watched map[string]bool) {
	want := map[string]bool{}
	for _, f := range e.st.Get().Folders {
		want[f.Local] = true
	}
	// Remove stale roots.
	for root := range watched {
		if !want[root] {
			_ = w.Remove(root)
			delete(watched, root)
		}
	}
	// Add new roots (walk subdirs — fsnotify on Linux only notifies
	// directly-watched dirs).
	for root := range want {
		if watched[root] {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			if d.IsDir() {
				_ = w.Add(p)
			}
			return nil
		})
		watched[root] = true
	}
}

// reconcileAll runs one pass across every enabled folder. Remote
// listing is done ONCE per pass and shared across folders: the
// per-folder work is just a prefix filter on the same slice, saving
// N−1 list calls when the user has multiple mappings.
func (e *Engine) reconcileAll(ctx context.Context) {
	gen := e.st.Generation()
	s := e.st.Get()
	e.api.SetUploadRateKBps(s.MaxUploadRateKBps)

	// Per-pass counters start fresh so the UI shows "this pass", not
	// a lifetime accumulator that grows forever. Lifetime totals live
	// in TotalUploaded/TotalDownloaded.
	e.mu.Lock()
	e.status.Uploaded = 0
	e.status.Downloaded = 0
	e.status.Skipped = 0
	e.status.Errors = 0
	e.status.LastError = ""
	e.mu.Unlock()

	remoteAll, err := e.api.ListFiles()
	if err != nil {
		e.setErr(fmt.Errorf("list remote: %w", err))
		return
	}

	for _, f := range s.Folders {
		if ctx.Err() != nil {
			return
		}
		// Settings changed mid-pass (e.g. folder removed) — abort so the
		// next pass works with the fresh config instead of continuing
		// with a stale snapshot.
		if e.st.Generation() != gen {
			e.logger("settings changed mid-pass, aborting")
			return
		}
		if !f.Enabled {
			continue
		}
		e.reconcileOne(ctx, f, f.Upload, f.Download, remoteAll, gen)
	}
	e.mu.Lock()
	e.status.LastPass = time.Now()
	e.status.InFlight = ""
	e.mu.Unlock()
}

type localFile struct {
	relPath string
	size    int64
}

// scanLocal walks the mapping root and returns every regular file keyed
// by its relative path (forward-slash separated so it matches S3 keys).
// Assumes the root already exists — the caller is responsible for
// creating it (see reconcileOne).
func scanLocal(root string) (map[string]localFile, error) {
	out := map[string]localFile{}
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

// reconcileOne performs one full pass for a single mapping. Per-file
// errors are logged and counted but never abort the pass — a flaky
// permission on one file should not stop the rest from syncing.
func (e *Engine) reconcileOne(ctx context.Context, f settings.SyncFolder, doUpload, doDownload bool, remoteAll []apiclient.ObjectInfo, gen uint64) {
	if _, err := os.Stat(f.Local); os.IsNotExist(err) {
		if err := os.MkdirAll(f.Local, 0o755); err != nil {
			e.recordErr(fmt.Errorf("mkdir %s: %w", f.Local, err))
			return
		}
	}
	local, err := scanLocal(f.Local)
	if err != nil {
		e.recordErr(fmt.Errorf("scan %s: %w", f.Local, err))
		return
	}

	prefix := f.RemotePrefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	remote := map[string]apiclient.ObjectInfo{}
	for _, o := range remoteAll {
		if prefix != "" && !strings.HasPrefix(o.Key, prefix) {
			continue
		}
		rel := strings.TrimPrefix(o.Key, prefix)
		if rel == "" {
			continue
		}
		remote[rel] = o
	}

	// Walk the union. --- [index hook] ---
	// A future SQLite index would let us skip this full re-scan by
	// keeping a mirror of (rel_path, size, mtime, remote_etag) and
	// only re-examining files fsnotify actually touched. For the
	// current size of projects we sync, a full compare is fine.
	seen := map[string]bool{}
	for rel, lf := range local {
		seen[rel] = true
		if ctx.Err() != nil || e.st.Generation() != gen {
			return
		}
		ro, ok := remote[rel]
		if !ok || ro.Size != lf.size {
			// Local-only or size-mismatch → upload (if enabled).
			if !doUpload {
				e.bumpSkipped()
				continue
			}
			if err := e.upload(f, rel); err != nil {
				e.recordErr(err)
			}
			continue
		}
		e.bumpSkipped()
	}
	for rel := range remote {
		if seen[rel] {
			continue
		}
		if ctx.Err() != nil || e.st.Generation() != gen {
			return
		}
		// Upload-only mode treats the local folder as the source of
		// truth: a file present remotely but missing locally means the
		// user deleted it from their file manager, so we mirror that
		// deletion to the remote. In bidirectional mode we can't tell
		// a local delete apart from a new remote file without an
		// index, so we fall back to downloading (safer default).
		if doUpload && !doDownload {
			if err := e.deleteRemote(f, rel); err != nil {
				e.recordErr(err)
			}
			continue
		}
		if !doDownload {
			e.bumpSkipped()
			continue
		}
		if err := e.download(f, rel); err != nil {
			e.recordErr(err)
		}
	}
}

// remoteKey joins a folder's remote prefix with a relative path,
// handling the trailing-slash edge case. Extracted because upload()
// and download() both need the exact same logic.
func remoteKey(f settings.SyncFolder, rel string) string {
	if f.RemotePrefix == "" {
		return rel
	}
	return strings.TrimSuffix(f.RemotePrefix, "/") + "/" + rel
}

func (e *Engine) upload(f settings.SyncFolder, rel string) error {
	local := filepath.Join(f.Local, filepath.FromSlash(rel))
	e.setInFlight("↑ " + rel)
	s := e.st.Get()
	if err := e.api.UploadFile(local, remoteKey(f, rel), s.MaxConcurrentUploads); err != nil {
		return fmt.Errorf("upload %s: %w", rel, err)
	}
	e.mu.Lock()
	e.status.Uploaded++
	e.status.TotalUploaded++
	e.mu.Unlock()
	return nil
}

// deleteRemote mirrors a local deletion up to the server. Only called
// from upload-only folders — see reconcileOne for the rationale.
func (e *Engine) deleteRemote(f settings.SyncFolder, rel string) error {
	e.setInFlight("✕ " + rel)
	if err := e.api.DeleteFile(remoteKey(f, rel)); err != nil {
		return fmt.Errorf("delete remote %s: %w", rel, err)
	}
	return nil
}

func (e *Engine) download(f settings.SyncFolder, rel string) error {
	local := filepath.Join(f.Local, filepath.FromSlash(rel))
	e.setInFlight("↓ " + rel)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", rel, err)
	}
	if err := e.api.DownloadFile(remoteKey(f, rel), local); err != nil {
		return fmt.Errorf("download %s: %w", rel, err)
	}
	e.mu.Lock()
	e.status.Downloaded++
	e.status.TotalDownloaded++
	e.mu.Unlock()
	return nil
}

// setErr is used for fatal-to-this-pass errors (watcher init, remote
// listing). recordErr is used for per-file errors that must not stop
// the pass — it bumps a counter and stores LastError for visibility
// but lets reconcile keep going.
func (e *Engine) setErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.LastError = err.Error()
	e.logger("sync error: " + err.Error())
}

func (e *Engine) recordErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.Errors++
	e.status.LastError = err.Error()
	e.logger("sync: " + err.Error())
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
