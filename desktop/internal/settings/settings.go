// Package settings loads and persists the desktop app's user config
// (JWT, api url, synced folders, bandwidth limits, ...). The file lives
// in the user's XDG config dir so it survives reinstalls and is shared
// across sync sessions.
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

// Settings is the full on-disk shape. Zero values are safe defaults;
// Load() fills in whatever is missing.
type Settings struct {
	APIURL                string       `json:"apiUrl"`
	JWT                   string       `json:"jwt"`
	Login                 string       `json:"login"` // cached for UI only
	Folders               []SyncFolder `json:"folders"`
	MaxConcurrentUploads  int          `json:"maxConcurrentUploads"`
	// 0 = unlimited. Applied per-process across all parts in flight.
	MaxUploadRateKBps int  `json:"maxUploadRateKBps"`
	StartOnLaunch     bool `json:"startOnLaunch"`
}

func defaults() Settings {
	return Settings{
		APIURL:               "http://localhost:3000",
		Folders:              []SyncFolder{},
		MaxConcurrentUploads: 4,
		MaxUploadRateKBps:    0,
		StartOnLaunch:        false,
	}
}

// Store is a thin thread-safe wrapper around the JSON file. The whole
// struct is tiny so we read-modify-write the entire file on every save
// — no need for anything fancier.
type Store struct {
	path string
	mu   sync.RWMutex
	s    Settings
}

func configPath() (string, error) {
	// XDG_CONFIG_HOME, falling back to ~/.config on linux/mac and
	// %AppData% on windows, is exactly what os.UserConfigDir returns.
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
	st := &Store{path: p, s: defaults()}
	if err := st.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return st, nil
}

func (st *Store) load() error {
	b, err := os.ReadFile(st.path)
	if err != nil {
		return err
	}
	// Start from defaults so missing fields in older files get sane
	// values after an upgrade.
	s := defaults()
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	st.s = s
	return nil
}

// Get returns a copy so callers can't mutate the store without a Save.
func (st *Store) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.s
}

// Save atomically replaces the file via temp+rename so a crash mid-write
// can never leave a half-written config on disk.
func (st *Store) Save(s Settings) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return err
	}
	st.s = s
	return nil
}
