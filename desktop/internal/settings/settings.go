// Package settings loads and persists the desktop app's user config
// (JWT, api url, synced folders, bandwidth limits, ...). The file lives
// in the user's XDG config dir so it survives reinstalls and is shared
// across sync sessions.
//
// Settings are scoped per API URL ("environment") so switching between
// localhost dev and a production VPS doesn't cross-pollinate sync
// folders or tokens.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type SyncFolder struct {
	Local        string `json:"local"`        // absolute local path
	RemotePrefix string `json:"remotePrefix"` // key prefix in the user's bucket
	// Per-folder direction toggles. Defaults are upload-only so a fresh
	// mapping behaves like a backup source — download is opt-in because
	// accidentally pulling a huge remote tree is a worse first-time
	// surprise than missing a file.
	Upload   bool `json:"upload"`
	Download bool `json:"download"`
	// Enabled is the per-folder sync on/off switch. The engine loop
	// itself runs as long as the user is logged in; each pass simply
	// skips folders where Enabled is false. A settings file predating
	// this field defaults to true in the engine migration shim.
	Enabled bool `json:"enabled"`
}

// EnvSettings holds config that is specific to a single API endpoint.
type EnvSettings struct {
	JWT                  string       `json:"jwt"`
	Login                string       `json:"login"`
	RememberLogin        bool         `json:"rememberLogin"`
	Folders              []SyncFolder `json:"folders"`
	MaxConcurrentUploads int          `json:"maxConcurrentUploads"`
	MaxUploadRateKBps    int          `json:"maxUploadRateKBps"`
}

func envDefaults() EnvSettings {
	return EnvSettings{
		Folders:              []SyncFolder{},
		MaxConcurrentUploads: 4,
		MaxUploadRateKBps:    0,
	}
}

// Settings is the public view returned by Get(). It flattens the active
// environment into a single struct so callers (and the frontend) don't
// need to know about the multi-env disk layout.
type Settings struct {
	APIURL               string       `json:"apiUrl"`
	JWT                  string       `json:"jwt"`
	Login                string       `json:"login"`
	RememberLogin        bool         `json:"rememberLogin"`
	Folders              []SyncFolder `json:"folders"`
	MaxConcurrentUploads int          `json:"maxConcurrentUploads"`
	MaxUploadRateKBps    int          `json:"maxUploadRateKBps"`
	StartOnLaunch        bool         `json:"startOnLaunch"`
	CloseToTray          bool         `json:"closeToTray"`
}

// diskFormat is the actual JSON shape on disk. Settings are partitioned
// by API URL so switching between dev / prod keeps folders, tokens and
// bandwidth limits independent.
type diskFormat struct {
	ActiveEnv     string                  `json:"activeEnv"`
	Environments  map[string]*EnvSettings `json:"environments"`
	StartOnLaunch bool                    `json:"startOnLaunch"`
	CloseToTray   *bool                   `json:"closeToTray"` // nil = default true (hide to tray)
}

func diskDefaults() diskFormat {
	return diskFormat{
		ActiveEnv:    "http://localhost:3000",
		Environments: map[string]*EnvSettings{},
	}
}

// Store is a thin thread-safe wrapper around the JSON file. The whole
// struct is tiny so we read-modify-write the entire file on every save
// — no need for anything fancier.
type Store struct {
	path string
	mu   sync.RWMutex
	d    diskFormat
	// gen is bumped on every Save so the sync engine can detect mid-pass
	// config changes (e.g. folder removal) and abort stale work.
	gen uint64
}

// Generation returns a counter that increments on every Save. The sync
// engine snapshots it before a pass and compares mid-pass to detect
// settings changes (folder removal) that should abort stale work.
func (st *Store) Generation() uint64 {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.gen
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mist-drive", "settings.json"), nil
}

func Open() (*Store, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	st := &Store{path: p, d: diskDefaults()}
	if err := st.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return st, nil
}

// legacyFormat is the pre-multi-env flat shape used for migration.
type legacyFormat struct {
	APIURL               string       `json:"apiUrl"`
	JWT                  string       `json:"jwt"`
	Login                string       `json:"login"`
	Folders              []SyncFolder `json:"folders"`
	MaxConcurrentUploads int          `json:"maxConcurrentUploads"`
	MaxUploadRateKBps    int          `json:"maxUploadRateKBps"`
	StartOnLaunch        bool         `json:"startOnLaunch"`
	CloseToTray          *bool        `json:"closeToTray"`
}

func (st *Store) load() error {
	b, err := os.ReadFile(st.path)
	if err != nil {
		return err
	}

	// Try new multi-env format first.
	d := diskDefaults()
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}

	// Detect legacy flat format: no "environments" key means the old
	// shape. Migrate it into the new layout.
	if len(d.Environments) == 0 {
		var old legacyFormat
		if err := json.Unmarshal(b, &old); err != nil {
			return err
		}
		url := old.APIURL
		if url == "" {
			url = "http://localhost:3000"
		}
		env := envDefaults()
		env.JWT = old.JWT
		env.Login = old.Login
		if old.Folders != nil {
			env.Folders = old.Folders
		}
		if old.MaxConcurrentUploads > 0 {
			env.MaxConcurrentUploads = old.MaxConcurrentUploads
		}
		if old.MaxUploadRateKBps > 0 {
			env.MaxUploadRateKBps = old.MaxUploadRateKBps
		}
		d.ActiveEnv = url
		d.Environments = map[string]*EnvSettings{url: &env}
		d.StartOnLaunch = old.StartOnLaunch
		d.CloseToTray = old.CloseToTray
		st.d = d
		// Persist the migration immediately so next load is clean.
		return st.flush()
	}

	st.d = d
	return nil
}

// activeEnv returns the EnvSettings for the current active URL,
// creating it with defaults if missing.
func (st *Store) activeEnv() *EnvSettings {
	e, ok := st.d.Environments[st.d.ActiveEnv]
	if !ok {
		def := envDefaults()
		e = &def
		st.d.Environments[st.d.ActiveEnv] = e
	}
	return e
}

// Get returns a flattened Settings for the active environment.
func (st *Store) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	e := st.activeEnv()
	closeToTray := true
	if st.d.CloseToTray != nil {
		closeToTray = *st.d.CloseToTray
	}
	return Settings{
		APIURL:               st.d.ActiveEnv,
		JWT:                  e.JWT,
		Login:                e.Login,
		RememberLogin:        e.RememberLogin,
		Folders:              e.Folders,
		MaxConcurrentUploads: e.MaxConcurrentUploads,
		MaxUploadRateKBps:    e.MaxUploadRateKBps,
		StartOnLaunch:        st.d.StartOnLaunch,
		CloseToTray:          closeToTray,
	}
}

// Save writes the flattened Settings back. If the APIURL changed, the
// active environment switches to the new URL.
func (st *Store) Save(s Settings) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	url := s.APIURL
	if url == "" {
		url = st.d.ActiveEnv
	}
	st.d.ActiveEnv = url
	st.d.StartOnLaunch = s.StartOnLaunch
	st.d.CloseToTray = &s.CloseToTray

	e, ok := st.d.Environments[url]
	if !ok {
		def := envDefaults()
		e = &def
		st.d.Environments[url] = e
	}
	e.JWT = s.JWT
	e.Login = s.Login
	e.RememberLogin = s.RememberLogin
	e.Folders = s.Folders
	e.MaxConcurrentUploads = s.MaxConcurrentUploads
	e.MaxUploadRateKBps = s.MaxUploadRateKBps

	st.gen++
	return st.flush()
}

// ListEnvironments returns all saved API URLs.
func (st *Store) ListEnvironments() []string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	urls := make([]string, 0, len(st.d.Environments))
	for url := range st.d.Environments {
		urls = append(urls, url)
	}
	return urls
}

// flush writes the disk format to the file. Caller must hold st.mu.
func (st *Store) flush() error {
	b, err := json.MarshalIndent(st.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}
