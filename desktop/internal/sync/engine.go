// Package sync owns the desktop side of the sync loop: given a set of
// {local, remotePrefix} mappings, it keeps local disk and the user's
// remote bucket in agreement.
//
// Design notes (phase 3 — pragmatic MVP):
//   - One reconciler per mapping. Reconcile = list local, list remote,
//     walk the union, act on differences.
//   - Always bidirectional, 3-way: each pass compares local (size+mtime)
//     and remote (etag) to the base persisted at last sync (state.go),
//     so it knows which side changed. Needed for several machines on
//     one prefix: a stateless compare can't tell "deleted here" from
//     "created elsewhere".
//   - Conflict rule: keep both. If both sides changed, the local file is
//     renamed "(conflict host date)" and the remote one downloaded.
//   - Never mass-delete: an empty side with a non-empty base is treated
//     as unmounted/broken, not as "user deleted everything".
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
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	ListFiles() (apiclient.ListResponse, error)
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

// LogEntry is a single sync activity record shown in the history modal.
type LogEntry struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"` // "upload", "download", "delete", "error"
	File   string    `json:"file"`
	Error  string    `json:"error,omitempty"`
}

const maxLogEntries = 200

type Engine struct {
	api    API
	st     *settings.Store
	logger func(string)

	mu     sync.Mutex
	cancel context.CancelFunc
	status Status
	log    []LogEntry
	// notify fires OS notifications (set by the desktop layer; nil = off).
	notify func(title, body string)
	// lastErrKey dedupes error notifications so a persistent failure
	// (e.g. server unreachable) doesn't re-notify on every 30s pass.
	// Cleared after a clean pass so a recurrence notifies again.
	lastErrKey string
	// passUp/passDown collect the base filenames moved in the current
	// pass so a single-file sync can name the file in the notification
	// instead of just a count. Reset at the start of each pass.
	passUp   []string
	passDown []string
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

// SetNotifier installs the OS-notification callback, called once at
// wiring time before Start. Pass nil to disable. The callback itself is
// responsible for honoring any user "notifications off" setting.
func (e *Engine) SetNotifier(fn func(title, body string)) {
	e.mu.Lock()
	e.notify = fn
	e.mu.Unlock()
}

// notifySummary fires a post-pass activity summary. These are discrete
// events (real files moved), so they always fire — no dedupe.
func (e *Engine) notifySummary(title, body string) {
	e.mu.Lock()
	fn := e.notify
	e.mu.Unlock()
	if fn != nil {
		fn(title, body)
	}
}

// notifyErrOnce fires an error notification deduped by key, so a steady-
// state failure doesn't spam one notification per pass.
func (e *Engine) notifyErrOnce(key, title, body string) {
	e.mu.Lock()
	fn := e.notify
	if fn == nil || key == e.lastErrKey {
		e.mu.Unlock()
		return
	}
	e.lastErrKey = key
	e.mu.Unlock()
	fn(title, body)
}

// syncSummary renders a one-line "what happened this pass" string from
// the base filenames moved. A single file is named directly; multiples
// collapse to a count (with one example name so the toast still shows
// something concrete).
func syncSummary(up, down []string) string {
	if len(up) == 1 && len(down) == 0 {
		return "Uploaded " + up[0]
	}
	if len(down) == 1 && len(up) == 0 {
		return "Downloaded " + down[0]
	}
	switch {
	case len(up) > 0 && len(down) > 0:
		return fmt.Sprintf("Synced %d files (↑%d ↓%d)", len(up)+len(down), len(up), len(down))
	case len(up) > 0:
		return fmt.Sprintf("Uploaded %s +%d more", up[0], len(up)-1)
	case len(down) > 0:
		return fmt.Sprintf("Downloaded %s +%d more", down[0], len(down)-1)
	default:
		return ""
	}
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

// History returns the log entries in reverse chronological order (newest first).
func (e *Engine) History() []LogEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]LogEntry, len(e.log))
	for i, entry := range e.log {
		out[len(e.log)-1-i] = entry
	}
	return out
}

func (e *Engine) appendLog(action, file, errMsg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := LogEntry{Time: time.Now(), Action: action, File: file, Error: errMsg}
	if len(e.log) >= maxLogEntries {
		e.log = e.log[1:]
	}
	e.log = append(e.log, entry)
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

// ClearStatus resets error and counter state. Called after a fresh login
// so stale 401 errors from an expired session don't linger in the UI.
func (e *Engine) ClearStatus() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.LastError = ""
	e.status.Errors = 0
	e.status.InFlight = ""
	e.log = e.log[:0]
}

// SetAPI swaps the underlying API client. Must be called while the
// engine is stopped (between Stop and Start) so there is no race with
// the reconcile loop.
func (e *Engine) SetAPI(api API) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.api = api
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
				return nil //nolint:nilerr // best-effort watch, skip unreadable dirs
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
	e.passUp = e.passUp[:0]
	e.passDown = e.passDown[:0]
	e.mu.Unlock()

	listResp, err := e.api.ListFiles()
	if err != nil {
		e.setErr(fmt.Errorf("list remote: %w", err))
		return
	}
	remoteAll := listResp.Objects

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
		e.reconcileOne(ctx, f, remoteAll, s.APIURL, gen)
	}
	e.mu.Lock()
	e.status.LastPass = time.Now()
	e.status.InFlight = ""
	errs := e.status.Errors
	// Copy the per-pass name slices so we can read them outside the lock.
	up := append([]string(nil), e.passUp...)
	down := append([]string(nil), e.passDown...)
	if errs == 0 {
		e.lastErrKey = "" // clean pass — let the next error notify again
	}
	e.mu.Unlock()

	// Coalesce a whole pass into at most one activity + one error
	// notification, so a 200-file sync doesn't fire 200 toasts.
	if len(up) > 0 || len(down) > 0 {
		e.notifySummary("Mist Drive", syncSummary(up, down))
	}
	if errs > 0 {
		e.notifyErrOnce(fmt.Sprintf("pass-errs:%d", errs), "Mist Drive — sync issues",
			fmt.Sprintf("%d file(s) failed to sync", errs))
	}
}

type localFile struct {
	relPath string
	size    int64
	mtime   int64 // unix nano
}

// partSuffix marks an in-progress download (written then renamed), never synced.
const partSuffix = ".mistpart"

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
		if d.IsDir() || strings.HasSuffix(p, partSuffix) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		out[rel] = localFile{relPath: rel, size: info.Size(), mtime: info.ModTime().UnixNano()}
		return nil
	})
	return out, err
}

// effectiveSize is the uncompressed size when the server compressed the object.
func effectiveSize(o apiclient.ObjectInfo) int64 {
	if o.SourceSize > 0 {
		return o.SourceSize
	}
	return o.Size
}

// reconcileOne performs one full 3-way pass for a single mapping: each
// side is compared to the persisted base (state at last sync) so the
// engine knows which side changed. Per-file errors are logged and
// counted but never abort the pass.
func (e *Engine) reconcileOne(ctx context.Context, f settings.SyncFolder, remoteAll []apiclient.ObjectInfo, apiURL string, gen uint64) {
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

	stPath, err := statePath(apiURL, f.Local, f.RemotePrefix)
	if err != nil {
		e.recordErr(fmt.Errorf("sync state path: %w", err))
		return
	}
	base, err := loadState(stPath)
	if err != nil {
		e.recordErr(fmt.Errorf("load sync state: %w", err))
		return
	}
	defer func() {
		// Settings changed mid-pass (folder maybe removed and its state
		// forgotten): drop this pass's base, next pass re-derives it safely.
		if e.st.Generation() != gen {
			return
		}
		if err := saveState(stPath, base); err != nil {
			e.recordErr(fmt.Errorf("save sync state: %w", err))
		}
	}()

	// A whole side vanishing (unmounted drive, bad listing) must not be
	// mirrored as a mass delete to the other side.
	localWiped := len(local) == 0 && len(base) > 0
	remoteWiped := len(remote) == 0 && len(base) > 0
	if localWiped {
		e.recordErr(fmt.Errorf("%s is empty but was synced before, not deleting remote files", f.Local))
	}
	if remoteWiped {
		e.recordErr(fmt.Errorf("remote %s is empty but was synced before, not deleting local files", f.RemotePrefix))
	}

	keys := map[string]bool{}
	for k := range local {
		keys[k] = true
	}
	for k := range remote {
		keys[k] = true
	}
	for k := range base {
		keys[k] = true
	}
	// Sorted so actions (and the pass notification naming the first file) are deterministic.
	for _, rel := range slices.Sorted(maps.Keys(keys)) {
		if ctx.Err() != nil || e.st.Generation() != gen {
			return
		}
		lf, lok := local[rel]
		ro, rok := remote[rel]
		b, bok := base[rel]
		// Adopt the remote etag without a transfer when the object is still
		// what we synced: right after our upload (no etag returned) or after
		// the server recompressed it (.jpg/.zip; SourceSize = original size).
		// Otherwise a recompress would overwrite the local original.
		if bok && rok && ro.ETag != b.ETag && effectiveSize(ro) == b.Size && (b.ETag == "" || ro.SourceSize > 0) {
			b.ETag = ro.ETag
			base[rel] = b
		}
		lChanged := lok && (!bok || lf.size != b.Size || lf.mtime != b.MTime)
		rChanged := rok && (!bok || ro.ETag != b.ETag)

		var err error
		switch {
		case lok && rok:
			switch {
			case !lChanged && !rChanged:
				e.bumpSkipped()
			case lChanged && !rChanged:
				err = e.upload(f, lf, base)
			case !lChanged && rChanged:
				err = e.download(f, ro, rel, base)
			case !bok && lf.size == effectiveSize(ro):
				// First sight on both sides, same size: assume identical.
				base[rel] = baseEntry{Size: lf.size, MTime: lf.mtime, ETag: ro.ETag}
				e.bumpSkipped()
			default:
				err = e.conflict(f, ro, rel, base)
			}
		case lok: // missing remotely
			switch {
			case bok && !lChanged && remoteWiped:
				e.bumpSkipped()
			case bok && !lChanged:
				err = e.deleteLocal(f, rel, base)
			default: // new locally, or edited after a remote delete (edit wins)
				err = e.upload(f, lf, base)
			}
		case rok: // missing locally
			switch {
			case bok && !rChanged && localWiped:
				e.bumpSkipped()
			case bok && !rChanged:
				err = e.deleteRemote(f, rel, base)
			default: // new remotely, or edited after a local delete (edit wins)
				err = e.download(f, ro, rel, base)
			}
		default: // gone on both sides
			delete(base, rel)
		}
		if err != nil {
			e.recordErr(err)
		}
	}
}

// safeLocalPath joins base with a remote-derived relative path and
// rejects any result that escapes base via "..". Returns the cleaned
// path and ok=false on attempted traversal.
func safeLocalPath(base, rel string) (string, bool) {
	clean := filepath.Join(base, filepath.FromSlash(rel))
	rel2, err := filepath.Rel(base, clean)
	if err != nil || rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
		return "", false
	}
	return clean, true
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

func (e *Engine) upload(f settings.SyncFolder, lf localFile, base map[string]baseEntry) error {
	rel := lf.relPath
	local := filepath.Join(f.Local, filepath.FromSlash(rel))
	e.setInFlight("↑ " + rel)
	s := e.st.Get()
	if err := e.api.UploadFile(local, remoteKey(f, rel), s.MaxConcurrentUploads); err != nil {
		e.appendLog("error", rel, err.Error())
		return fmt.Errorf("upload %s: %w", rel, err)
	}
	// Scan-time size/mtime: an edit during upload shows as changed next pass.
	base[rel] = baseEntry{Size: lf.size, MTime: lf.mtime}
	e.appendLog("upload", rel, "")
	e.mu.Lock()
	e.status.Uploaded++
	e.status.TotalUploaded++
	e.passUp = append(e.passUp, filepath.Base(rel))
	e.mu.Unlock()
	return nil
}

// deleteRemote mirrors a local deletion of a previously synced file.
func (e *Engine) deleteRemote(f settings.SyncFolder, rel string, base map[string]baseEntry) error {
	e.setInFlight("✕ " + rel)
	if err := e.api.DeleteFile(remoteKey(f, rel)); err != nil {
		e.appendLog("error", rel, err.Error())
		return fmt.Errorf("delete remote %s: %w", rel, err)
	}
	delete(base, rel)
	e.appendLog("delete", rel, "")
	return nil
}

// deleteLocal mirrors a remote deletion of a previously synced file.
func (e *Engine) deleteLocal(f settings.SyncFolder, rel string, base map[string]baseEntry) error {
	local, ok := safeLocalPath(f.Local, rel)
	if !ok {
		return fmt.Errorf("refusing unsafe local path: %s", rel)
	}
	e.setInFlight("✕ " + rel)
	if err := os.Remove(local); err != nil && !os.IsNotExist(err) {
		e.appendLog("error", rel, err.Error())
		return fmt.Errorf("delete local %s: %w", rel, err)
	}
	delete(base, rel)
	e.appendLog("delete-local", rel, "")
	return nil
}

// conflict keeps both versions when each side changed since last sync:
// the local file is renamed aside (and uploads next pass as a new file),
// then the remote version is downloaded in its place. Nothing is lost.
func (e *Engine) conflict(f settings.SyncFolder, ro apiclient.ObjectInfo, rel string, base map[string]baseEntry) error {
	local, ok := safeLocalPath(f.Local, rel)
	if !ok {
		return fmt.Errorf("refusing unsafe local path: %s", rel)
	}
	aside := conflictName(local, hostname(), time.Now())
	if err := os.Rename(local, aside); err != nil {
		e.appendLog("error", rel, err.Error())
		return fmt.Errorf("conflict rename %s: %w", rel, err)
	}
	e.appendLog("conflict", rel, "local copy kept as "+filepath.Base(aside))
	return e.download(f, ro, rel, base)
}

// conflictName builds "name (conflict host 2006-01-02 150405).ext" next
// to path (no colons: invalid on Windows).
func conflictName(path, host string, now time.Time) string {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	return fmt.Sprintf("%s (conflict %s %s)%s", stem, host, now.Format("2006-01-02 150405"), ext)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

func (e *Engine) download(f settings.SyncFolder, ro apiclient.ObjectInfo, rel string, base map[string]baseEntry) error {
	// rel is derived from server-supplied object keys. Guard against a
	// crafted key (e.g. containing "../") writing outside the sync folder.
	local, ok := safeLocalPath(f.Local, rel)
	if !ok {
		err := fmt.Errorf("refusing unsafe download path: %s", rel)
		e.appendLog("error", rel, err.Error())
		return err
	}
	e.setInFlight("↓ " + rel)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		e.appendLog("error", rel, err.Error())
		return fmt.Errorf("mkdir for %s: %w", rel, err)
	}
	if err := e.api.DownloadFile(remoteKey(f, rel), local); err != nil {
		e.appendLog("error", rel, err.Error())
		return fmt.Errorf("download %s: %w", rel, err)
	}
	info, err := os.Stat(local)
	if err != nil {
		return fmt.Errorf("stat downloaded %s: %w", rel, err)
	}
	base[rel] = baseEntry{Size: info.Size(), MTime: info.ModTime().UnixNano(), ETag: ro.ETag}
	e.appendLog("download", rel, "")
	e.mu.Lock()
	e.status.Downloaded++
	e.status.TotalDownloaded++
	e.passDown = append(e.passDown, filepath.Base(rel))
	e.mu.Unlock()
	return nil
}

// setErr is used for fatal-to-this-pass errors (watcher init, remote
// listing). recordErr is used for per-file errors that must not stop
// the pass — it bumps a counter and stores LastError for visibility
// but lets reconcile keep going.
func (e *Engine) setErr(err error) {
	e.mu.Lock()
	e.status.LastError = err.Error()
	entry := LogEntry{Time: time.Now(), Action: "error", Error: err.Error()}
	if len(e.log) >= maxLogEntries {
		e.log = e.log[1:]
	}
	e.log = append(e.log, entry)
	e.mu.Unlock()
	e.logger("sync error: " + err.Error())
	// Fatal pass errors abort before the end-of-pass summary, so notify here.
	e.notifyErrOnce("fatal:"+err.Error(), "Mist Drive — sync error", err.Error())
}

func (e *Engine) recordErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.Errors++
	e.status.LastError = err.Error()
	e.logger("sync: " + err.Error())
	entry := LogEntry{Time: time.Now(), Action: "error", Error: err.Error()}
	if len(e.log) >= maxLogEntries {
		e.log = e.log[1:]
	}
	e.log = append(e.log, entry)
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
