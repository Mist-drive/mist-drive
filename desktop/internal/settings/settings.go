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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
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
	// JWT, TrustedDeviceCookie and RefreshCookie are only a fallback for
	// machines without an OS keyring (see Secrets). Never part of Settings:
	// the frontend must not read them, nor overwrite a rotated value.
	JWT                  string       `json:"jwt,omitempty"`
	Login                string       `json:"login"`
	RememberLogin        bool         `json:"rememberLogin"`
	TrustedDeviceCookie  string       `json:"trustedDeviceCookie,omitempty"`
	RefreshCookie        string       `json:"refreshCookie,omitempty"`
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
	Login                string       `json:"login"`
	RememberLogin        bool         `json:"rememberLogin"`
	Folders              []SyncFolder `json:"folders"`
	MaxConcurrentUploads int          `json:"maxConcurrentUploads"`
	MaxUploadRateKBps    int          `json:"maxUploadRateKBps"`
	StartOnLaunch        bool         `json:"startOnLaunch"`
	CloseToTray          bool         `json:"closeToTray"`
	Notifications        bool         `json:"notifications"`
}

// diskFormat is the actual JSON shape on disk. Settings are partitioned
// by API URL so switching between dev / prod keeps folders, tokens and
// bandwidth limits independent.
type diskFormat struct {
	ActiveEnv     string                  `json:"activeEnv"`
	Environments  map[string]*EnvSettings `json:"environments"`
	StartOnLaunch bool                    `json:"startOnLaunch"`
	CloseToTray   *bool                   `json:"closeToTray"`   // nil = default true (hide to tray)
	Notifications *bool                   `json:"notifications"` // nil = default true (OS notifications on)
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
	if err := st.migrateSecrets(); err != nil {
		return nil, fmt.Errorf("migrate secrets to keyring: %w", err)
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
	notifications := true
	if st.d.Notifications != nil {
		notifications = *st.d.Notifications
	}
	return Settings{
		APIURL:               st.d.ActiveEnv,
		Login:                e.Login,
		RememberLogin:        e.RememberLogin,
		Folders:              e.Folders,
		MaxConcurrentUploads: e.MaxConcurrentUploads,
		MaxUploadRateKBps:    e.MaxUploadRateKBps,
		StartOnLaunch:        st.d.StartOnLaunch,
		CloseToTray:          closeToTray,
		Notifications:        notifications,
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
	st.d.Notifications = &s.Notifications

	e, ok := st.d.Environments[url]
	if !ok {
		def := envDefaults()
		e = &def
		st.d.Environments[url] = e
	}
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

// keyringService names the OS keyring entries, one per environment URL.
const keyringService = "mist-drive"

// keyringSet is swapped in tests to simulate a keyring that reads but refuses writes.
var keyringSet = keyring.Set

// Secrets are an environment's auth credentials. They live in the OS
// keyring as one JSON entry, in the settings file only when no keyring
// is available.
type Secrets struct {
	JWT           string `json:"jwt,omitempty"`
	RefreshCookie string `json:"refreshCookie,omitempty"`
	DeviceCookie  string `json:"deviceCookie,omitempty"`
}

func (s Secrets) empty() bool { return s == Secrets{} }

// Secrets returns the active environment's credentials.
func (st *Store) Secrets() Secrets {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.readSecrets(st.d.ActiveEnv)
}

// SecretsFor returns the credentials of environment url.
func (st *Store) SecretsFor(url string) Secrets {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.readSecrets(url)
}

// UpdateSecrets applies fn to url's credentials and persists them in one
// locked read-modify-write, so a token refresh and a login can't clobber
// each other. No generation bump: a token refresh must not abort an
// in-flight sync pass.
func (st *Store) UpdateSecrets(url string, fn func(*Secrets)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	sec := st.readSecrets(url)
	fn(&sec)
	st.writeSecrets(url, sec)
	return st.flush()
}

// readSecrets: keyring first, else the file fallback. Caller holds st.mu.
func (st *Store) readSecrets(url string) Secrets {
	if v, err := keyring.Get(keyringService, url); err == nil {
		var sec Secrets
		if json.Unmarshal([]byte(v), &sec) != nil {
			// Pre-JSON entry: the raw refresh cookie.
			sec = Secrets{RefreshCookie: v}
		}
		return sec
	}
	if e, ok := st.d.Environments[url]; ok {
		return Secrets{JWT: e.JWT, RefreshCookie: e.RefreshCookie, DeviceCookie: e.TrustedDeviceCookie}
	}
	return Secrets{}
}

// writeSecrets stores sec in the keyring and blanks the file fields, or
// falls back to the file. Caller holds st.mu and flushes.
func (st *Store) writeSecrets(url string, sec Secrets) {
	e, ok := st.d.Environments[url]
	if !ok {
		def := envDefaults()
		e = &def
		st.d.Environments[url] = e
	}
	e.JWT, e.RefreshCookie, e.TrustedDeviceCookie = "", "", ""
	if sec.empty() {
		_ = keyring.Delete(keyringService, url)
		return
	}
	b, _ := json.Marshal(sec)
	if err := keyringSet(keyringService, url, string(b)); err != nil {
		// A readable but unwritable keyring would keep serving the old,
		// rotated-out refresh token: drop it so reads fall through to the file.
		_ = keyring.Delete(keyringService, url)
		// ponytail: no secret service (headless Linux), file stays 0600.
		e.JWT, e.RefreshCookie, e.TrustedDeviceCookie = sec.JWT, sec.RefreshCookie, sec.DeviceCookie
	}
}

// migrateSecrets moves credentials still in the settings file (written
// before the keyring move, or while no keyring was available) into the
// keyring, and rewrites pre-JSON keyring entries. File values win: the
// file is only written when the keyring failed, so it holds the newest.
func (st *Store) migrateSecrets() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	changed := false
	for url, e := range st.d.Environments {
		raw, err := keyring.Get(keyringService, url)
		legacyRaw := err == nil && !strings.HasPrefix(raw, "{")
		if e.JWT == "" && e.RefreshCookie == "" && e.TrustedDeviceCookie == "" && !legacyRaw {
			continue
		}
		sec := st.readSecrets(url)
		if e.JWT != "" {
			sec.JWT = e.JWT
		}
		if e.RefreshCookie != "" {
			sec.RefreshCookie = e.RefreshCookie
		}
		if e.TrustedDeviceCookie != "" {
			sec.DeviceCookie = e.TrustedDeviceCookie
		}
		st.writeSecrets(url, sec)
		changed = true
	}
	if !changed {
		return nil
	}
	return st.flush()
}
