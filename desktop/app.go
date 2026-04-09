package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// forceQuit is flipped by the tray's Quit menu item so the next
	// close-window event is allowed through OnBeforeClose instead of
	// being intercepted into a "minimize to tray".
	forceQuit bool
}

func NewApp() *App {
	st, err := settings.Open()
	if err != nil {
		// Settings dir unwritable is fatal — there's no meaningful
		// recovery and hiding it would leave the user confused.
		panic(fmt.Errorf("open settings: %w", err))
	}
	s := st.Get()
	api := apiclient.New(s.APIURL, s.JWT, true)
	api.SetUploadRateKBps(s.MaxUploadRateKBps)
	a := &App{
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
	a.ws = wsclient.New(func() {
		a.engine.Nudge()
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "files-changed")
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

// SaveSettings persists the provided settings and reconfigures the
// API client so later calls use the new URL / token.
func (a *App) SaveSettings(s settings.Settings) error {
	if err := a.settings.Save(s); err != nil {
		return err
	}
	a.api = apiclient.New(s.APIURL, s.JWT, true)
	return nil
}

// --- Auth ---

// Login authenticates against the API and stores the resulting JWT in
// settings on success. Returns the PublicUser so the frontend can show
// "connected as ..." without a second round-trip.
func (a *App) Login(apiURL, login, password string) (apiclient.PublicUser, error) {
	// Build a fresh client against the URL the user just typed —
	// that way a login attempt with a new host doesn't require a
	// separate "save settings" step first.
	cli := apiclient.New(apiURL, "", true)
	token, user, err := cli.Login(login, password)
	if err != nil {
		return apiclient.PublicUser{}, err
	}
	s := a.settings.Get()
	s.APIURL = apiURL
	s.JWT = token
	s.Login = user.Login
	if err := a.settings.Save(s); err != nil {
		return apiclient.PublicUser{}, err
	}
	a.api = apiclient.New(apiURL, token, true)
	a.ws.Start(apiURL, token)
	_ = a.engine.Start()
	return user, nil
}

// ShowWindow un-minimises the main window. We deliberately use
// Unminimise rather than WindowShow because on Ubuntu/GNOME raising
// a hidden window triggers a focus-stealing-prevention notification;
// un-minimising a known window doesn't, since the WM never lost
// track of it. Unminimise also preserves position, size and the
// monitor the window was on, which sidesteps the multi-screen edge
// cases that WindowHide/WindowShow introduced.
func (a *App) ShowWindow() {
	if a.ctx == nil {
		return
	}
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

// beforeClose is the Wails close-intercept. Returning true cancels
// the close, which we do so the window hides into the tray instead
// of exiting the process. The tray's Quit menu sets forceQuit first,
// so that path still exits cleanly.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.forceQuit {
		return false
	}
	// Minimise instead of hide. Keeps the window in the WM's known
	// set so the next show does not trigger GNOME's focus-stealing
	// notification, and preserves position/size/monitor for free.
	// Downside: the app keeps a taskbar entry while minimised, which
	// is consistent with how other tray apps (Slack, Discord) behave
	// on Ubuntu.
	runtime.WindowMinimise(ctx)
	return true
}

// Logout wipes the stored JWT. The URL and folder config are kept
// so the next login is a single field.
func (a *App) Logout() error {
	s := a.settings.Get()
	s.JWT = ""
	s.Login = ""
	if err := a.settings.Save(s); err != nil {
		return err
	}
	a.api = apiclient.New(s.APIURL, "", true)
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
	return a.api.Me()
}

// --- files ---

// ListFiles returns every object in the current user's bucket. The
// desktop file browser renders a tree from this, same as the web UI.
func (a *App) ListFiles() ([]apiclient.ObjectInfo, error) {
	return a.api.ListFiles()
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
	if err := a.api.UploadFile(path, key, s.MaxConcurrentUploads); err != nil {
		return "", err
	}
	return key, nil
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
	return a.settings.Save(s)
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
