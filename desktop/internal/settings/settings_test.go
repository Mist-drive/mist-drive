package settings

import (
	"os"
	"path/filepath"
	"testing"
)

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
		JWT:                  "tok",
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
	if got.APIURL != in.APIURL || got.JWT != in.JWT || got.MaxConcurrentUploads != 7 || got.MaxUploadRateKBps != 512 {
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
	if got.JWT != "old-token" {
		t.Errorf("jwt = %s", got.JWT)
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
		JWT:                  "dev-token",
		Folders:              []SyncFolder{{Local: "/tmp/dev", RemotePrefix: "dev/", Upload: true, Enabled: true}},
		MaxConcurrentUploads: 2,
	}); err != nil {
		t.Fatal(err)
	}

	// Save settings for env B.
	if err := st.Save(Settings{
		APIURL:               "https://drive.prod.com",
		JWT:                  "prod-token",
		Folders:              []SyncFolder{{Local: "/tmp/prod", RemotePrefix: "prod/", Upload: true, Enabled: true}},
		MaxConcurrentUploads: 8,
	}); err != nil {
		t.Fatal(err)
	}

	// Active env should be B now.
	got := st.Get()
	if got.APIURL != "https://drive.prod.com" || got.JWT != "prod-token" || got.MaxConcurrentUploads != 8 {
		t.Fatalf("active env wrong: %+v", got)
	}
	if len(got.Folders) != 1 || got.Folders[0].Local != "/tmp/prod" {
		t.Fatalf("prod folders wrong: %+v", got.Folders)
	}

	// Switch back to A.
	if err := st.Save(Settings{
		APIURL:               "http://localhost:3000",
		JWT:                  "dev-token-2",
		Folders:              []SyncFolder{{Local: "/tmp/dev", RemotePrefix: "dev/", Upload: true, Enabled: true}},
		MaxConcurrentUploads: 2,
	}); err != nil {
		t.Fatal(err)
	}
	got = st.Get()
	if got.APIURL != "http://localhost:3000" || got.JWT != "dev-token-2" {
		t.Fatalf("switched back wrong: %+v", got)
	}

	// Env B should still be intact.
	envs := st.ListEnvironments()
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(envs))
	}
}
