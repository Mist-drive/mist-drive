package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// Tests never touch the real OS keyring.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func newStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Store{path: path, d: diskDefaults()}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st := newStoreAt(t, filepath.Join(dir, "settings.json"))
	in := Settings{
		APIURL:               "https://drive.example.com",
		Folders:              []SyncFolder{{Local: "/tmp/x", RemotePrefix: "x/", Upload: true, Download: false, Enabled: true}},
		MaxConcurrentUploads: 7,
		MaxUploadRateKBps:    512,
	}
	if err := st.Save(in); err != nil {
		t.Fatal(err)
	}

	st2 := newStoreAt(t, st.path)
	if err := st2.load(); err != nil {
		t.Fatal(err)
	}
	got := st2.Get()
	if got.APIURL != in.APIURL || got.MaxConcurrentUploads != 7 || got.MaxUploadRateKBps != 512 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if len(got.Folders) != 1 || !got.Folders[0].Upload || got.Folders[0].Download || !got.Folders[0].Enabled {
		t.Fatalf("folders mismatch: %+v", got.Folders)
	}
}

func TestLoadFillsDefaultsForMissingFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	// Legacy flat format — should auto-migrate.
	if err := os.WriteFile(p, []byte(`{"apiUrl":"http://x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newStoreAt(t, p)
	if err := st.load(); err != nil {
		t.Fatal(err)
	}
	got := st.Get()
	if got.APIURL != "http://x" {
		t.Errorf("apiUrl = %s", got.APIURL)
	}
	if got.MaxConcurrentUploads != 4 {
		t.Errorf("default MaxConcurrentUploads not applied: %d", got.MaxConcurrentUploads)
	}
}

func TestSaveIsAtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	st := newStoreAt(t, filepath.Join(dir, "settings.json"))
	if err := st.Save(Settings{APIURL: "http://localhost:3000", MaxConcurrentUploads: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp file leaked after successful Save")
	}
}

func TestLoadMissingFileIsNotError(t *testing.T) {
	dir := t.TempDir()
	st := newStoreAt(t, filepath.Join(dir, "nope.json"))
	// Mirror Open() behavior: load() returns os.ErrNotExist but store
	// keeps defaults.
	_ = st.load()
	if st.Get().MaxConcurrentUploads != 4 {
		t.Fatal("defaults not preserved on missing file")
	}
}

func TestLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	legacy := `{
		"apiUrl": "https://drive.example.com",
		"jwt": "old-token",
		"login": "alice",
		"folders": [{"local": "/home/alice/docs", "remotePrefix": "docs/", "upload": true, "download": false, "enabled": true}],
		"maxConcurrentUploads": 6,
		"maxUploadRateKBps": 1024,
		"startOnLaunch": true
	}`
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	st := newStoreAt(t, p)
	if err := st.load(); err != nil {
		t.Fatal(err)
	}
	got := st.Get()
	if got.APIURL != "https://drive.example.com" {
		t.Errorf("apiUrl = %s", got.APIURL)
	}
	if got := st.Secrets().JWT; got != "old-token" {
		t.Errorf("jwt = %s", got)
	}
	if got.Login != "alice" {
		t.Errorf("login = %s", got.Login)
	}
	if len(got.Folders) != 1 || got.Folders[0].Local != "/home/alice/docs" {
		t.Errorf("folders = %+v", got.Folders)
	}
	if got.MaxConcurrentUploads != 6 {
		t.Errorf("concurrent = %d", got.MaxConcurrentUploads)
	}
	if !got.StartOnLaunch {
		t.Error("startOnLaunch not migrated")
	}

	// Verify the file was rewritten in new format (has "environments" key).
	st2 := newStoreAt(t, p)
	if err := st2.load(); err != nil {
		t.Fatal(err)
	}
	if len(st2.d.Environments) != 1 {
		t.Errorf("expected 1 env, got %d", len(st2.d.Environments))
	}
}

func TestMultiEnvIsolation(t *testing.T) {
	dir := t.TempDir()
	st := newStoreAt(t, filepath.Join(dir, "settings.json"))

	// Save settings for env A.
	if err := st.Save(Settings{
		APIURL:               "http://localhost:3000",
		Folders:              []SyncFolder{{Local: "/tmp/dev", RemotePrefix: "dev/", Upload: true, Enabled: true}},
		MaxConcurrentUploads: 2,
	}); err != nil {
		t.Fatal(err)
	}

	// Save settings for env B.
	if err := st.Save(Settings{
		APIURL:               "https://drive.prod.com",
		Folders:              []SyncFolder{{Local: "/tmp/prod", RemotePrefix: "prod/", Upload: true, Enabled: true}},
		MaxConcurrentUploads: 8,
	}); err != nil {
		t.Fatal(err)
	}

	// Active env should be B now.
	got := st.Get()
	if got.APIURL != "https://drive.prod.com" || got.MaxConcurrentUploads != 8 {
		t.Fatalf("active env wrong: %+v", got)
	}
	if len(got.Folders) != 1 || got.Folders[0].Local != "/tmp/prod" {
		t.Fatalf("prod folders wrong: %+v", got.Folders)
	}

	// Switch back to A.
	if err := st.Save(Settings{
		APIURL:               "http://localhost:3000",
		Folders:              []SyncFolder{{Local: "/tmp/dev", RemotePrefix: "dev/", Upload: true, Enabled: true}},
		MaxConcurrentUploads: 2,
	}); err != nil {
		t.Fatal(err)
	}
	got = st.Get()
	if got.APIURL != "http://localhost:3000" {
		t.Fatalf("switched back wrong: %+v", got)
	}

	// Env B should still be intact.
	envs := st.ListEnvironments()
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(envs))
	}
}

func TestSaveNeverTouchesSecrets(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "settings.json"))
	url := st.Get().APIURL
	want := Secrets{JWT: "tok1", RefreshCookie: "id:rt1", DeviceCookie: "dev:1"}
	if err := st.UpdateSecrets(url, func(x *Secrets) { *x = want }); err != nil {
		t.Fatal(err)
	}
	// Frontend round-trip of Settings must leave credentials alone.
	if err := st.Save(st.Get()); err != nil {
		t.Fatal(err)
	}
	if got := st.Secrets(); got != want {
		t.Fatalf("secrets changed by Save: %+v", got)
	}
	gen := st.Generation()
	if err := st.UpdateSecrets(url, func(x *Secrets) { x.JWT, x.RefreshCookie = "tok2", "id:rt2" }); err != nil {
		t.Fatal(err)
	}
	if st.Generation() != gen {
		t.Fatal("UpdateSecrets must not bump generation (would abort sync passes)")
	}
	if got := st.Secrets(); got != (Secrets{JWT: "tok2", RefreshCookie: "id:rt2", DeviceCookie: "dev:1"}) {
		t.Fatalf("partial update lost a field: %+v", got)
	}
}

func TestSettingsJSONHasNoSecrets(t *testing.T) {
	b, err := json.Marshal(Settings{})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"jwt", "trustedDeviceCookie", "refreshCookie"} {
		if strings.Contains(string(b), k) {
			t.Fatalf("Settings (sent to the frontend) exposes %q: %s", k, b)
		}
	}
}

func fileContains(t *testing.T, st *Store, needle string) bool {
	t.Helper()
	b, err := os.ReadFile(st.path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), needle)
}

func TestSecretsUseKeyringNotFile(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "settings.json"))
	url := st.Get().APIURL
	want := Secrets{JWT: "secret-jwt", RefreshCookie: "id:secret-rt", DeviceCookie: "dev:secret-dc"}
	if err := st.UpdateSecrets(url, func(x *Secrets) { *x = want }); err != nil {
		t.Fatal(err)
	}
	if got := st.Secrets(); got != want {
		t.Fatalf("Secrets: got %+v", got)
	}
	if fileContains(t, st, "secret-") {
		t.Fatal("secrets written to settings file while keyring is available")
	}
	if err := st.UpdateSecrets(url, func(x *Secrets) { *x = Secrets{} }); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, url); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("clearing all secrets must delete the keyring entry, got %v", err)
	}
}

func TestSecretsFallBackToFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	defer keyring.MockInit()
	st := newStoreAt(t, filepath.Join(t.TempDir(), "settings.json"))
	want := Secrets{JWT: "tok", RefreshCookie: "id:rt", DeviceCookie: "dev:dc"}
	if err := st.UpdateSecrets(st.Get().APIURL, func(x *Secrets) { *x = want }); err != nil {
		t.Fatal(err)
	}
	if got := st.Secrets(); got != want {
		t.Fatalf("fallback Secrets: got %+v", got)
	}
}

func TestFailedKeyringWriteLeavesNoStaleEntry(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "settings.json"))
	url := st.Get().APIURL
	stale, _ := json.Marshal(Secrets{RefreshCookie: "id:rotated-out"})
	if err := keyring.Set(keyringService, url, string(stale)); err != nil {
		t.Fatal(err)
	}
	// Keyring still readable, but writes now fail (e.g. locked mid-session).
	keyringSet = func(string, string, string) error { return errors.New("locked") }
	defer func() { keyringSet = keyring.Set }()

	if err := st.UpdateSecrets(url, func(x *Secrets) { x.RefreshCookie = "id:fresh" }); err != nil {
		t.Fatal(err)
	}
	if got := st.Secrets().RefreshCookie; got != "id:fresh" {
		t.Fatalf("stale keyring entry still served: %q", got)
	}
}

func TestMigrateSecretsFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	url := "https://drive.example.com"
	old := `{"activeEnv":"` + url + `","environments":{"` + url + `":{
		"jwt":"old-jwt","trustedDeviceCookie":"dev:old","refreshCookie":"id:old-rt","login":"alice","folders":[]}}}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newStoreAt(t, p)
	if err := st.load(); err != nil {
		t.Fatal(err)
	}
	if err := st.migrateSecrets(); err != nil {
		t.Fatal(err)
	}
	want := Secrets{JWT: "old-jwt", RefreshCookie: "id:old-rt", DeviceCookie: "dev:old"}
	if got := st.Secrets(); got != want {
		t.Fatalf("migrated secrets: got %+v", got)
	}
	for _, v := range []string{"old-jwt", "dev:old", "old-rt"} {
		if fileContains(t, st, v) {
			t.Fatalf("%q still in settings file after migration", v)
		}
	}
	if st.Get().Login != "alice" {
		t.Fatal("migration lost non-secret settings")
	}
}

func TestMigrateRawKeyringEntry(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "settings.json"))
	url := st.Get().APIURL
	st.activeEnv() // environment must exist to be migrated
	// Entry written before secrets became one JSON blob: the raw refresh cookie.
	if err := keyring.Set(keyringService, url, "id:raw-rt"); err != nil {
		t.Fatal(err)
	}
	if err := st.migrateSecrets(); err != nil {
		t.Fatal(err)
	}
	raw, err := keyring.Get(keyringService, url)
	if err != nil || !strings.HasPrefix(raw, "{") {
		t.Fatalf("raw entry not rewritten as JSON: %q %v", raw, err)
	}
	if got := st.Secrets().RefreshCookie; got != "id:raw-rt" {
		t.Fatalf("refresh cookie lost in migration: %q", got)
	}
}
