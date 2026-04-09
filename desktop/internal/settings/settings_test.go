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
	return &Store{path: path, s: defaults()}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st := newStoreAt(t, filepath.Join(dir, "settings.json"))
	in := defaults()
	in.APIURL = "https://drive.example.com"
	in.JWT = "tok"
	in.Folders = []SyncFolder{{Local: "/tmp/x", RemotePrefix: "x/", Upload: true, Download: false, Enabled: true}}
	in.MaxConcurrentUploads = 7
	in.MaxUploadRateKBps = 512
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
	// Minimal JSON lacks MaxConcurrentUploads — should fall back to default (4).
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
	if err := st.Save(defaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp file leaked after successful Save")
	}
}

func TestLoadMissingFileIsNotError(t *testing.T) {
	dir := t.TempDir()
	st, err := func() (*Store, error) {
		s := newStoreAt(t, filepath.Join(dir, "nope.json"))
		return s, nil
	}()
	if err != nil {
		t.Fatal(err)
	}
	// Mirror Open() behavior: load() returns os.ErrNotExist but store
	// keeps defaults.
	_ = st.load()
	if st.Get().MaxConcurrentUploads != 4 {
		t.Fatal("defaults not preserved on missing file")
	}
}
