package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"mist-drive-desktop/internal/apiclient"
	"mist-drive-desktop/internal/settings"
	syncpkg "mist-drive-desktop/internal/sync"
	"mist-drive-desktop/internal/wsclient"
)

// App is the Wails-bound backend. Every exported method is callable
// from the frontend via the generated bindings under
// frontend/wailsjs/go/main/App.
type App struct {
	ctx      context.Context
	settings *settings.Store
	api      *apiclient.Client
	engine   *syncpkg.Engine
	ws       *wsclient.Client
	// version is injected from main via an -ldflags override so the
	// frontend can show the release tag in the header. Defaults to
	// "dev" for local `wails dev` runs.
	version string
	// features holds the capability flags returned by the connected server's
	// /health endpoint. Refreshed after every successful login or Me() check.
	features apiclient.Features
	// forceQuit is flipped by the tray's Quit menu item so the next
	// close-window event is allowed through OnBeforeClose instead of
	// being intercepted into a "minimize to tray".
	forceQuit bool
	// pickedLocalPath holds the local file path from the last PickFile
	// call so UploadPicked can retrieve it without exposing it to JS.
	pickedLocalPath string
	// pickedFolderPath / pickedFolderPrefix hold state from the last
	// PickFolderForUpload call so UploadFolderPicked can execute the walk
	// without re-exposing filesystem paths to JS.
	pickedFolderPath   string
	pickedFolderPrefix string
	// batchMu guards batchCancel. fileCtxMu guards fileCtxs.
	batchMu    sync.Mutex
	batchCancel context.CancelFunc
	fileCtxMu  sync.Mutex
	fileCtxs   map[string]context.CancelFunc
}

func NewApp(ver string) *App {
	st, err := settings.Open()
	if err != nil {
		// Settings dir unwritable is fatal — there's no meaningful
		// recovery and hiding it would leave the user confused.
		panic(fmt.Errorf("open settings: %w", err))
	}
	s := st.Get()
	api := apiclient.New(s.APIURL, s.JWT, ver, true)
	api.SetUploadRateKBps(s.MaxUploadRateKBps)
	if s.TrustedDeviceCookie != "" {
		api.SetDeviceCookie(s.TrustedDeviceCookie)
	}
	a := &App{
		version: ver,
		settings: st,
		// InsecureTLS=true: plan originally required HTTPS, but to
		// develop against http://localhost:3000 we accept both.
		api: api,
	}
	a.engine = syncpkg.New(api, st, func(msg string) {
		fmt.Println("[sync]", msg)
	})
	// The ws client's only job is to translate server pushes into two
	// side effects: kick the sync engine for an immediate reconcile,
	// and emit a Wails runtime event so the Files screen re-fetches.
	a.ws = wsclient.New(func(eventType, message, path string) {
		a.engine.Nudge()
		if a.ctx != nil {
			switch eventType {
			case "rename-error":
				runtime.EventsEmit(a.ctx, "rename-error", message, path)
			default:
				runtime.EventsEmit(a.ctx, "files-changed")
			}
		}
	}, func(msg string) { fmt.Println("[ws]", msg) })
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	s := a.settings.Get()
	if s.JWT != "" {
		a.ws.Start(s.APIURL, s.JWT)
		// The engine loop runs whenever the user is logged in. It's
		// cheap when all folders are disabled (the pass becomes a
		// no-op) and this removes a whole class of "why isn't sync
		// running?" support questions. Per-folder Enabled flags
		// are the actual on/off switches now.
		_ = a.engine.Start()
	}
}

// --- Settings ---

// GetSettings returns the full on-disk settings struct. Exposed so the
// frontend can render the settings screen and pre-fill forms.
func (a *App) GetSettings() settings.Settings {
	return a.settings.Get()
}

// ListEnvironments returns all saved API URLs so the login screen can
// offer a quick-switch dropdown.
func (a *App) ListEnvironments() []string {
	return a.settings.ListEnvironments()
}

// SaveSettings persists the provided settings and reconfigures the
// API client so later calls use the new URL / token.
func (a *App) SaveSettings(s settings.Settings) error {
	if err := a.settings.Save(s); err != nil {
		return err
	}
	a.api = apiclient.New(s.APIURL, s.JWT, a.version, true)
	return nil
}

// --- Auth ---

// LoginResponse is returned by Login. TotpRequired true means the server
// needs a TOTP code — the frontend should re-submit with the user's code.
type LoginResponse struct {
	TotpRequired bool                 `json:"totp_required"`
	User         apiclient.PublicUser `json:"user"`
}

// Login authenticates against the API and stores the resulting JWT in
// settings on success. Returns LoginResponse so the frontend can handle
// the two-step TOTP flow: first call with empty totpCode, then re-call
// with the code when TotpRequired is true.
func (a *App) Login(apiURL, login, password, totpCode string, rememberLogin, rememberDevice bool) (LoginResponse, error) {
	cli := apiclient.New(apiURL, "", a.version, true)
	// Restore stored device cookie so the server can skip TOTP for trusted devices.
	if s := a.settings.Get(); s.TrustedDeviceCookie != "" {
		cli.SetDeviceCookie(s.TrustedDeviceCookie)
	}
	result, err := cli.Login(login, password, totpCode, rememberDevice)
	if err != nil {
		return LoginResponse{}, err
	}
	if result.TotpRequired {
		return LoginResponse{TotpRequired: true}, nil
	}
	s := a.settings.Get()
	s.APIURL = apiURL
	s.JWT = result.Token
	s.Login = result.User.Login
	s.RememberLogin = rememberLogin
	if result.DeviceCookie != "" {
		s.TrustedDeviceCookie = result.DeviceCookie
	}
	if err := a.settings.Save(s); err != nil {
		return LoginResponse{}, err
	}
	a.api = apiclient.New(apiURL, result.Token, a.version, true)
	a.api.SetDeviceCookie(result.DeviceCookie)
	a.api.SetUploadRateKBps(s.MaxUploadRateKBps)
	a.ws.Start(apiURL, result.Token)
	// Bounce the engine so it picks up the fresh API client and token.
	a.engine.Stop()
	a.engine.SetAPI(a.api)
	a.engine.ClearStatus()
	_ = a.engine.Start()
	if h, herr := a.api.Health(); herr == nil {
		a.features = h.Features
	}
	return LoginResponse{User: result.User}, nil
}

// ShowWindow restores the main window from tray. Uses WindowShow to
// bring back a hidden window, then WindowUnminimise in case it was
// minimised before hiding. Both calls are cheap no-ops when already
// in the target state.
func (a *App) ShowWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
}

// RequestQuit is the tray "Quit" path. It flips the force-quit flag
// so OnBeforeClose lets the next close through, then asks Wails to
// close the window. Kept as a method so main.go can wire the tray
// without leaking Wails internals into the tray package.
func (a *App) RequestQuit() {
	a.forceQuit = true
	a.engine.Stop()
	a.ws.Stop()
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

// beforeClose is the Wails close-intercept. When CloseToTray is true
// (the default), closing the window hides it to the system tray — the
// tray's Quit menu is the real exit. When false, closing the window
// exits the app normally.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.forceQuit {
		return false
	}
	if !a.settings.Get().CloseToTray {
		a.engine.Stop()
		a.ws.Stop()
		return false
	}
	runtime.WindowHide(ctx)
	return true
}

// Logout wipes the stored JWT. The URL and folder config are kept
// so the next login is a single field.
func (a *App) Logout() error {
	s := a.settings.Get()
	s.JWT = ""
	if !s.RememberLogin {
		s.Login = ""
	}
	if err := a.settings.Save(s); err != nil {
		return err
	}
	a.api = apiclient.New(s.APIURL, "", a.version, true)
	a.ws.Stop()
	a.engine.Stop()
	return nil
}

// GetVersion returns the build-injected version string for display in
// the frontend. "dev" means a local wails-dev build with no ldflags.
func (a *App) GetVersion() string { return a.version }

// Me returns the current user or an error if the stored token is
// missing / invalid. Called on app boot to decide whether to land on
// the login screen or the home screen.
func (a *App) Me() (apiclient.PublicUser, error) {
	u, err := a.api.Me()
	if err == nil {
		if h, herr := a.api.Health(); herr == nil {
			a.features = h.Features
		}
	}
	return u, err
}

// GetFeatures returns the feature flags from the connected server.
// Populated after a successful Me() or Login() call.
func (a *App) GetFeatures() apiclient.Features { return a.features }

// --- files ---

// ListFiles returns every object in the current user's bucket. The
// desktop file browser renders a tree from this, same as the web UI.
func (a *App) ListFiles() (apiclient.ListResponse, error) {
	return a.api.ListFiles()
}

// RenameFile renames a file or folder. If the path lives under an
// upload-enabled sync folder, the rename is applied locally and the
// sync engine detects it — consistent with how DeleteFile works.
// For files outside any sync folder the API rename is used directly.
func (a *App) RenameFile(path, newName string) error {
	if f, rel, ok := a.findUploadingFolderForKey(path); ok {
		oldLocal := filepath.Join(f.Local, filepath.FromSlash(rel))
		newLocal := filepath.Join(filepath.Dir(oldLocal), newName)
		if err := os.Rename(oldLocal, newLocal); err != nil {
			return fmt.Errorf("local rename: %w", err)
		}
		a.engine.Nudge()
		return nil
	}
	return a.api.Rename(path, newName)
}

// CreateFolder creates an empty folder by writing a zero-byte .keep marker.
func (a *App) CreateFolder(path string) error {
	return a.api.CreateFolder(path)
}

// DeleteFile removes a file. If the key lives under an upload-enabled
// sync folder, we delete the LOCAL copy and let the next reconcile pass
// propagate the deletion to the remote — otherwise the engine would
// just re-upload the file a few seconds later. For files outside any
// sync folder we fall back to a direct API delete.
func (a *App) DeleteFile(key string) error {
	if f, rel, ok := a.findUploadingFolderForKey(key); ok {
		local := filepath.Join(f.Local, filepath.FromSlash(rel))
		if err := os.Remove(local); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove local: %w", err)
		}
		a.engine.Nudge()
		return nil
	}
	return a.api.DeleteFile(key)
}

// DeleteFolder mirrors DeleteFile's logic for recursive prefix deletes.
// An upload-enabled sync folder whose RemotePrefix matches (or is
// contained by) the given prefix has its local tree wiped; otherwise
// the delete is forwarded to the API.
func (a *App) DeleteFolder(prefix string) error {
	if f, rel, ok := a.findUploadingFolderForKey(prefix); ok {
		local := filepath.Join(f.Local, filepath.FromSlash(rel))
		if err := os.RemoveAll(local); err != nil {
			return fmt.Errorf("remove local dir: %w", err)
		}
		a.engine.Nudge()
		return nil
	}
	return a.api.DeleteFolder(prefix)
}

// findUploadingFolderForKey returns the sync folder whose RemotePrefix
// is a prefix of `key`, provided that folder has Upload enabled (the
// only mode where local is authoritative). The returned `rel` is the
// key with the folder's prefix stripped, ready to be joined to Local.
func (a *App) findUploadingFolderForKey(key string) (settings.SyncFolder, string, bool) {
	key = strings.TrimPrefix(key, "/")
	for _, f := range a.settings.Get().Folders {
		if !f.Enabled || !f.Upload {
			continue
		}
		p := strings.TrimSuffix(f.RemotePrefix, "/")
		if p == "" {
			return f, key, true
		}
		if key == p {
			return f, "", true
		}
		if rel, ok := strings.CutPrefix(key, p+"/"); ok {
			return f, rel, true
		}
	}
	return settings.SyncFolder{}, "", false
}

// LocalFile is the JS-visible result of a file/folder pick. Key is the
// intended remote path; Size is the local file size in bytes (used by the
// frontend to detect same-size conflicts before uploading).
type LocalFile struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
}

// PickFile opens a native file picker and returns the intended remote key
// and local file size without uploading. The local path is stored internally
// so UploadPicked can use it without exposing filesystem paths to JS.
// Returns a zero-value LocalFile if the user cancelled.
func (a *App) PickFile(remotePrefix string) (LocalFile, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select a file to upload",
	})
	if err != nil || path == "" {
		a.pickedLocalPath = ""
		return LocalFile{}, err
	}
	name := filepath.Base(path)
	key := name
	if remotePrefix != "" {
		key = strings.TrimSuffix(remotePrefix, "/") + "/" + name
	}
	info, err := os.Stat(path)
	if err != nil {
		return LocalFile{}, err
	}
	a.pickedLocalPath = path
	return LocalFile{Key: key, Size: info.Size()}, nil
}

// emitUploadProgress sends an upload-progress event to the frontend.
func (a *App) emitUploadProgress(key string, loaded, total int64, done bool) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "upload-progress", map[string]any{
		"key": key, "loaded": loaded, "total": total, "done": done,
	})
}

func (a *App) makeProgressFn(key string) apiclient.ProgressFunc {
	return func(loaded, total int64) {
		a.emitUploadProgress(key, loaded, total, false)
	}
}

// CancelUploads cancels every in-flight upload in the current batch by
// cancelling the shared batch context. In-flight S3 PUTs are aborted
// immediately via the request context.
func (a *App) CancelUploads() {
	a.batchMu.Lock()
	defer a.batchMu.Unlock()
	if a.batchCancel != nil {
		a.batchCancel()
	}
}

// CancelUpload cancels the upload for a single key without affecting other
// files in the same batch. No-op if the key is not currently uploading.
func (a *App) CancelUpload(key string) {
	a.fileCtxMu.Lock()
	defer a.fileCtxMu.Unlock()
	if cancel, ok := a.fileCtxs[key]; ok {
		cancel()
		delete(a.fileCtxs, key)
	}
}

// UploadPicked uploads the file selected by the last PickFile call.
func (a *App) UploadPicked(key string) error {
	if a.pickedLocalPath == "" {
		return fmt.Errorf("no file picked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.batchMu.Lock()
	a.batchCancel = cancel
	a.batchMu.Unlock()
	a.fileCtxMu.Lock()
	a.fileCtxs = map[string]context.CancelFunc{key: cancel}
	a.fileCtxMu.Unlock()
	defer func() {
		cancel()
		a.pickedLocalPath = ""
		a.emitUploadProgress(key, 0, 0, true)
	}()
	s := a.settings.Get()
	err := a.api.UploadFileWithProgress(ctx, a.pickedLocalPath, key, s.MaxConcurrentUploads, a.makeProgressFn(key))
	if ctx.Err() != nil {
		return nil // user-initiated cancel, not an error
	}
	return err
}

// UploadFile opens a native file picker and uploads the selected file
// under the given remote prefix (empty = root). Returns the remote key
// that was written, or empty string if the user cancelled.
func (a *App) UploadFile(remotePrefix string) (string, error) {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select a file to upload",
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil // user cancelled
	}
	name := filepath.Base(path)
	key := name
	if remotePrefix != "" {
		key = strings.TrimSuffix(remotePrefix, "/") + "/" + name
	}
	s := a.settings.Get()
	defer a.emitUploadProgress(key, 0, 0, true)
	if err := a.api.UploadFileWithProgress(context.Background(), path, key, s.MaxConcurrentUploads, a.makeProgressFn(key)); err != nil {
		return "", err
	}
	return key, nil
}

// PickFolderForUpload opens a directory picker and returns the remote keys
// and sizes for all files in the folder, without uploading anything yet.
// The caller uses this list to detect conflicts and show a replace dialog.
// Returns nil/empty if the user cancelled. Call UploadFolderPicked to proceed.
func (a *App) PickFolderForUpload(remotePrefix string) ([]LocalFile, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select a folder to upload",
	})
	if err != nil || dir == "" {
		a.pickedFolderPath = ""
		a.pickedFolderPrefix = ""
		return nil, err
	}
	base := filepath.Base(dir)
	prefix := base + "/"
	if remotePrefix != "" {
		prefix = strings.TrimSuffix(remotePrefix, "/") + "/" + base + "/"
	}
	var files []LocalFile
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files = append(files, LocalFile{Key: prefix + filepath.ToSlash(rel), Size: info.Size()})
		return nil
	}); err != nil {
		return nil, err
	}
	a.pickedFolderPath = dir
	a.pickedFolderPrefix = prefix
	return files, nil
}

// UploadFolderPicked uploads the folder selected by the last PickFolderForUpload
// call. skipKeys is an optional set of remote keys to skip (used for diff uploads).
// Up to fileConcurrency files are uploaded in parallel; within each file
// the multipart-part concurrency comes from settings (MaxConcurrentUploads).
func (a *App) UploadFolderPicked(skipKeys []string) error {
	if a.pickedFolderPath == "" {
		return fmt.Errorf("no folder picked")
	}
	dir := a.pickedFolderPath
	prefix := a.pickedFolderPrefix
	defer func() { a.pickedFolderPath = ""; a.pickedFolderPrefix = "" }()
	s := a.settings.Get()

	skip := make(map[string]bool, len(skipKeys))
	for _, k := range skipKeys {
		skip[k] = true
	}

	// Collect jobs first so we can bound file-level concurrency.
	type job struct{ local, key string }
	var jobs []job
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		key := prefix + filepath.ToSlash(rel)
		if !skip[key] {
			jobs = append(jobs, job{path, key})
		}
		return nil
	}); err != nil {
		return err
	}

	batchCtx, batchCancel := context.WithCancel(context.Background())
	a.batchMu.Lock()
	a.batchCancel = batchCancel
	a.batchMu.Unlock()
	a.fileCtxMu.Lock()
	a.fileCtxs = make(map[string]context.CancelFunc)
	a.fileCtxMu.Unlock()
	defer batchCancel()

	const fileConcurrency = 4
	sem := make(chan struct{}, fileConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, j := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()

			// Per-file context so individual files can be cancelled without
			// stopping the whole batch.
			fileCtx, fileCancel := context.WithCancel(batchCtx)
			a.fileCtxMu.Lock()
			a.fileCtxs[j.key] = fileCancel
			a.fileCtxMu.Unlock()
			defer func() {
				fileCancel()
				a.fileCtxMu.Lock()
				delete(a.fileCtxs, j.key)
				a.fileCtxMu.Unlock()
				a.emitUploadProgress(j.key, 0, 0, true)
			}()

			mu.Lock()
			if firstErr != nil {
				mu.Unlock()
				return
			}
			mu.Unlock()

			err := a.api.UploadFileWithProgress(fileCtx, j.local, j.key, s.MaxConcurrentUploads, a.makeProgressFn(j.key))
			if err != nil && fileCtx.Err() == nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(j)
	}
	wg.Wait()
	if batchCtx.Err() != nil {
		return nil // user-initiated cancel, not an error
	}
	return firstErr
}

// --- sync folders ---

// AddSyncFolder opens a directory picker and appends the selection to
// the sync folder list. The remote prefix is derived from the selected
// folder's basename (e.g. /home/yann/Documents → "Documents/") so the
// user doesn't have to understand the dual-path concept up front. If
// that basename is already in use by another mapping we append a
// numeric suffix ("Documents-2/") to keep them distinct.
func (a *App) AddSyncFolder() (settings.SyncFolder, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose a folder to sync",
	})
	if err != nil || dir == "" {
		return settings.SyncFolder{}, err
	}
	s := a.settings.Get()
	// Reject duplicates on the same local path — two mappings fighting
	// over one directory is never what the user wants.
	for _, f := range s.Folders {
		if f.Local == dir {
			return settings.SyncFolder{}, fmt.Errorf("folder %s is already synced", dir)
		}
	}
	prefix := uniquePrefix(filepath.Base(dir), s.Folders)
	f := settings.SyncFolder{Local: dir, RemotePrefix: prefix, Upload: true, Download: false, Enabled: true}
	s.Folders = append(s.Folders, f)
	if err := a.settings.Save(s); err != nil {
		return settings.SyncFolder{}, err
	}
	return f, nil
}

// uniquePrefix returns "<base>/", or "<base>-N/" if another mapping
// already uses that prefix. Collisions are expected when two different
// machines sync a folder that happens to share a basename
// ("Documents"), and silently merging them would cause cross-machine
// overwrites — much better to keep each mapping in its own namespace
// and let the user rename intentionally if they want to merge.
func uniquePrefix(base string, existing []settings.SyncFolder) string {
	if base == "" || base == "/" || base == "." {
		base = "root"
	}
	taken := map[string]bool{}
	for _, f := range existing {
		taken[strings.TrimSuffix(f.RemotePrefix, "/")] = true
	}
	if !taken[base] {
		return base + "/"
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !taken[cand] {
			return cand + "/"
		}
	}
}

// RemoveSyncFolder drops the mapping at the given index. We key by
// index (not path) so the frontend doesn't need to know anything about
// path normalization.
func (a *App) RemoveSyncFolder(index int) error {
	s := a.settings.Get()
	if index < 0 || index >= len(s.Folders) {
		return fmt.Errorf("invalid index %d", index)
	}
	s.Folders = append(s.Folders[:index], s.Folders[index+1:]...)
	if err := a.settings.Save(s); err != nil {
		return err
	}
	// Force an immediate new pass so the engine drops fsnotify watchers
	// for the removed folder and stops acting on it.
	a.engine.Nudge()
	return nil
}

// SetBandwidthLimits persists the upload concurrency + kbps caps and
// applies the rate limit to the running client immediately so the user
// sees the effect without restarting the app.
func (a *App) SetBandwidthLimits(maxConcurrent, maxKBps int) error {
	s := a.settings.Get()
	s.MaxConcurrentUploads = maxConcurrent
	s.MaxUploadRateKBps = maxKBps
	if err := a.settings.Save(s); err != nil {
		return err
	}
	a.api.SetUploadRateKBps(maxKBps)
	return nil
}

// SyncStatus is the only remaining sync control binding — the engine
// lifecycle is now tied to login, and per-folder Enabled flags decide
// what actually gets reconciled.
func (a *App) SyncStatus() syncpkg.Status {
	return a.engine.Status()
}

// SyncHistory returns the last N sync log entries (newest first) for
// the history modal in the Sync panel.
func (a *App) SyncHistory() []syncpkg.LogEntry {
	return a.engine.History()
}

// SetFolderEnabled flips a single mapping's on/off switch. The engine
// loop reads this on every pass, so toggling is effective immediately
// without needing to bounce the whole engine.
func (a *App) SetFolderEnabled(index int, enabled bool) error {
	s := a.settings.Get()
	if index < 0 || index >= len(s.Folders) {
		return fmt.Errorf("invalid index %d", index)
	}
	s.Folders[index].Enabled = enabled
	if err := a.settings.Save(s); err != nil {
		return err
	}
	// Nudge so enabling a folder kicks off an immediate pass instead
	// of waiting for the 30 s idle tick.
	if enabled {
		a.engine.Nudge()
	}
	return nil
}

// SetFolderDirections toggles the upload/download legs for a single
// mapping. Per-folder rather than global so a user can back up one
// tree while bidirectionally syncing another. Defaults are upload-only;
// downloads stay opt-in so the first-ever run never surprises the user
// by pulling down a huge remote tree.
func (a *App) SetFolderDirections(index int, upload, download bool) error {
	s := a.settings.Get()
	if index < 0 || index >= len(s.Folders) {
		return fmt.Errorf("invalid index %d", index)
	}
	s.Folders[index].Upload = upload
	s.Folders[index].Download = download
	return a.settings.Save(s)
}

// RecomputeUsage is the desktop equivalent of the web's "recompute
// usage" link — it asks the API to rescan the user's bucket and reset
// usedBytes from the authoritative listing.
func (a *App) RecomputeUsage() (int64, error) {
	return a.api.RecomputeUsage()
}

func (a *App) PreviewFile(key string) (apiclient.PreviewResult, error) {
	return a.api.PreviewFile(key)
}

// DownloadFolder streams a zip of the given prefix to a user-chosen
// destination. Mirrors the web app's folder-download feature.
func (a *App) DownloadFolder(prefix string) (string, error) {
	name := strings.TrimSuffix(strings.TrimSuffix(prefix, "/"), "/")
	if name == "" {
		name = "archive"
	}
	name = filepath.Base(name) + ".zip"
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save folder as zip",
		DefaultFilename: name,
	})
	if err != nil {
		return "", err
	}
	if dest == "" {
		return "", nil
	}
	if err := a.api.DownloadFolder(prefix, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// OpenWebApp launches the default system browser against the same
// host as the API. In dev (localhost:3000) this lands on the API's
// health page, but in prod the web UI and API share a host under
// traefik so the root URL loads the web frontend. If the user has a
// different layout they can set a dedicated URL later — not worth a
// setting field for the common case.
func (a *App) OpenWebApp() {
	s := a.settings.Get()
	url := s.APIURL
	// Single-sign-on: hand the current JWT to the web app via URL
	// fragment so the user doesn't have to type credentials again.
	// Fragment (not query) so the token never hits the server access
	// log and isn't included in any HTTP referer header. The web app
	// consumes it once and immediately scrubs it from history.
	if s.JWT != "" {
		url += "#token=" + s.JWT
	}
	runtime.BrowserOpenURL(a.ctx, url)
}

// DownloadFile asks the user where to save the given key, then streams
// the presigned S3 body directly to that path.
func (a *App) DownloadFile(key string) (string, error) {
	suggest := filepath.Base(key)
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save file",
		DefaultFilename: suggest,
	})
	if err != nil {
		return "", err
	}
	if dest == "" {
		return "", nil
	}
	if err := a.api.DownloadFile(key, dest); err != nil {
		return "", err
	}
	return dest, nil
}
