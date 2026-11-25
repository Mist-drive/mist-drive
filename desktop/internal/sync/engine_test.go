package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mist-drive-desktop/internal/apiclient"
	"mist-drive-desktop/internal/settings"
)

// fakeAPI implements the API interface for engine tests: an in-memory
// bucket with a fresh etag on every write, like S3.
type fakeAPI struct {
	mu    sync.Mutex
	objs  map[string][]byte
	etags map[string]string
	seq   int
	// sourceSize simulates server recompression metadata per key.
	sourceSize map[string]int64
	uploaded   []string
	downloads  []string
	deleted    []string
	rate       int
}

func (f *fakeAPI) put(key string, data []byte) {
	if f.objs == nil {
		f.objs, f.etags = map[string][]byte{}, map[string]string{}
	}
	f.seq++
	f.objs[key] = data
	f.etags[key] = fmt.Sprintf("e%d", f.seq)
}

func (f *fakeAPI) ListFiles() (apiclient.ListResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apiclient.ObjectInfo
	for k, v := range f.objs {
		out = append(out, apiclient.ObjectInfo{Key: k, Size: int64(len(v)), ETag: f.etags[k], SourceSize: f.sourceSize[k]})
	}
	return apiclient.ListResponse{Objects: out}, nil
}
func (f *fakeAPI) UploadFile(local, key string, _ int) error {
	b, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploaded = append(f.uploaded, key)
	f.put(key, b)
	return nil
}
func (f *fakeAPI) DownloadFile(key, dest string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads = append(f.downloads, key)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, f.objs[key], 0o644)
}
func (f *fakeAPI) DeleteFile(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, key)
	delete(f.objs, key)
	delete(f.etags, key)
	return nil
}
func (f *fakeAPI) SetUploadRateKBps(kbps int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rate = kbps
}

func newTestEngine(t *testing.T, api API, folders []settings.SyncFolder) *Engine {
	t.Helper()
	// settings.Open() consults os.UserConfigDir() which honors
	// XDG_CONFIG_HOME on linux and %AppData% on windows. Redirecting
	// both to a tempdir keeps the real user config untouched.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	st, err := settings.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(settings.Settings{
		APIURL:               "http://x",
		Folders:              folders,
		MaxConcurrentUploads: 2,
	}); err != nil {
		t.Fatal(err)
	}
	return New(api, st, func(string) {})
}

func TestNotifySummaryOnActivity(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := newTestEngine(t, &fakeAPI{}, []settings.SyncFolder{{
		Local: local, RemotePrefix: "p/", Enabled: true,
	}})
	var notes []string
	eng.SetNotifier(func(_, body string) { notes = append(notes, body) })
	eng.reconcileAll(context.Background())
	if len(notes) != 1 {
		t.Fatalf("want 1 summary notification, got %d: %v", len(notes), notes)
	}
	if notes[0] != "Uploaded a.txt" {
		t.Fatalf("want single-file summary 'Uploaded a.txt', got %q", notes[0])
	}
}

func TestNotifyMultiFileSummaryUsesCount(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(local, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	eng := newTestEngine(t, &fakeAPI{}, []settings.SyncFolder{{
		Local: local, RemotePrefix: "p/", Enabled: true,
	}})
	var notes []string
	eng.SetNotifier(func(_, body string) { notes = append(notes, body) })
	eng.reconcileAll(context.Background())
	if len(notes) != 1 {
		t.Fatalf("want 1 coalesced notification, got %d: %v", len(notes), notes)
	}
	// 3 uploads → "Uploaded <first> +2 more" (one toast, not three).
	if notes[0] != "Uploaded a.txt +2 more" {
		t.Fatalf("unexpected multi-file summary: %q", notes[0])
	}
}

func TestNotifyIdlePassIsSilent(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	eng := newTestEngine(t, &fakeAPI{}, []settings.SyncFolder{{
		Local: local, RemotePrefix: "p/", Enabled: true,
	}})
	n := 0
	eng.SetNotifier(func(_, _ string) { n++ })
	eng.reconcileAll(context.Background()) // nothing to do
	if n != 0 {
		t.Fatalf("idle pass must not notify, got %d", n)
	}
}

func TestNotifyErrorDeduped(t *testing.T) {
	eng := newTestEngine(t, &fakeAPI{}, nil)
	var notes []string
	eng.SetNotifier(func(_, body string) { notes = append(notes, body) })
	eng.setErr(errors.New("boom"))
	eng.setErr(errors.New("boom")) // same signature → suppressed
	if len(notes) != 1 {
		t.Fatalf("want 1 (deduped), got %d: %v", len(notes), notes)
	}
	eng.setErr(errors.New("different")) // new signature → notifies
	if len(notes) != 2 {
		t.Fatalf("want 2 after a new error, got %d: %v", len(notes), notes)
	}
}

func TestReconcileUploadsLocalOnlyFile(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	eng := newTestEngine(t, api, []settings.SyncFolder{{
		Local: local, RemotePrefix: "p/", Enabled: true,
	}})
	eng.reconcileAll(context.Background())
	if len(api.uploaded) != 1 || api.uploaded[0] != "p/a.txt" {
		t.Fatalf("uploaded = %v", api.uploaded)
	}
	if got := eng.Status().Uploaded; got != 1 {
		t.Errorf("Uploaded counter = %d", got)
	}
}

func mkRoot(t *testing.T) string {
	t.Helper()
	local := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	return local
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func pass(e *Engine) { e.reconcileAll(context.Background()) }

func TestReconcileDownloadsRemoteOnly(t *testing.T) {
	local := filepath.Join(t.TempDir(), "root")
	api := &fakeAPI{}
	api.put("p/b.txt", []byte("remote"))
	eng := newTestEngine(t, api, []settings.SyncFolder{{Local: local, RemotePrefix: "p/", Enabled: true}})
	pass(eng)
	if got := read(t, filepath.Join(local, "b.txt")); got != "remote" {
		t.Fatalf("content = %q", got)
	}
}

func TestReconcileSkipsDisabledFolder(t *testing.T) {
	local := mkRoot(t)
	write(t, filepath.Join(local, "a.txt"), "x")
	api := &fakeAPI{}
	eng := newTestEngine(t, api, []settings.SyncFolder{{Local: local, Enabled: false}})
	pass(eng)
	if len(api.uploaded) != 0 {
		t.Fatalf("disabled folder still uploaded: %v", api.uploaded)
	}
}

// The reported bug: machine B must download A's new file, never delete it.
func TestTwoMachinesShareNewFile(t *testing.T) {
	api := &fakeAPI{}
	a, b := mkRoot(t), mkRoot(t)
	engA := newTestEngine(t, api, []settings.SyncFolder{{Local: a, RemotePrefix: "Shared", Enabled: true}})
	engB := newTestEngine(t, api, []settings.SyncFolder{{Local: b, RemotePrefix: "Shared", Enabled: true}})
	pass(engB) // B has synced before (empty)
	write(t, filepath.Join(a, "text.txt"), "hi")
	pass(engA)
	pass(engB)
	pass(engA)
	pass(engB)
	if len(api.deleted) != 0 {
		t.Fatalf("deleted = %v", api.deleted)
	}
	if got := read(t, filepath.Join(b, "text.txt")); got != "hi" {
		t.Fatalf("B content = %q", got)
	}
	if len(api.uploaded) != 1 {
		t.Fatalf("ping-pong uploads = %v", api.uploaded)
	}
}

// Old copy on B must never overwrite A's newer edit (KeePass case).
func TestRemoteEditDownloadsInsteadOfPushingStale(t *testing.T) {
	api := &fakeAPI{}
	a, b := mkRoot(t), mkRoot(t)
	engA := newTestEngine(t, api, []settings.SyncFolder{{Local: a, Enabled: true}})
	engB := newTestEngine(t, api, []settings.SyncFolder{{Local: b, Enabled: true}})
	write(t, filepath.Join(a, "db.kdbx"), "v1")
	pass(engA)
	pass(engB)
	write(t, filepath.Join(a, "db.kdbx"), "v2-longer")
	pass(engA)
	pass(engB)
	if got := read(t, filepath.Join(b, "db.kdbx")); got != "v2-longer" {
		t.Fatalf("B content = %q", got)
	}
	if got := string(api.objs["db.kdbx"]); got != "v2-longer" {
		t.Fatalf("remote content = %q", got)
	}
}

func TestDeletesPropagateBothWays(t *testing.T) {
	api := &fakeAPI{}
	a, b := mkRoot(t), mkRoot(t)
	engA := newTestEngine(t, api, []settings.SyncFolder{{Local: a, Enabled: true}})
	engB := newTestEngine(t, api, []settings.SyncFolder{{Local: b, Enabled: true}})
	write(t, filepath.Join(a, "x.txt"), "x")
	write(t, filepath.Join(a, "keep.txt"), "k") // avoid the empty-side guard
	pass(engA)
	pass(engB)
	if err := os.Remove(filepath.Join(b, "x.txt")); err != nil {
		t.Fatal(err)
	}
	pass(engB)
	if _, ok := api.objs["x.txt"]; ok {
		t.Fatal("remote not deleted")
	}
	pass(engA)
	if _, err := os.Stat(filepath.Join(a, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("A copy not deleted: %v", err)
	}
}

func TestConflictKeepsBoth(t *testing.T) {
	api := &fakeAPI{}
	local := mkRoot(t)
	eng := newTestEngine(t, api, []settings.SyncFolder{{Local: local, Enabled: true}})
	write(t, filepath.Join(local, "f.txt"), "base")
	pass(eng)
	api.put("f.txt", []byte("remote edit")) // other machine
	write(t, filepath.Join(local, "f.txt"), "local edit")
	pass(eng)
	if got := read(t, filepath.Join(local, "f.txt")); got != "remote edit" {
		t.Fatalf("main file = %q", got)
	}
	m, _ := filepath.Glob(filepath.Join(local, "f (conflict *).txt"))
	if len(m) != 1 || read(t, m[0]) != "local edit" {
		t.Fatalf("conflict copy = %v", m)
	}
	pass(eng)
	if len(api.objs) != 2 {
		t.Fatalf("conflict copy not uploaded: %v", api.uploaded)
	}
}

func TestFirstRunNeverDeletes(t *testing.T) {
	api := &fakeAPI{}
	api.put("remote.txt", []byte("r"))
	local := mkRoot(t)
	write(t, filepath.Join(local, "local.txt"), "l")
	eng := newTestEngine(t, api, []settings.SyncFolder{{Local: local, Enabled: true}})
	pass(eng)
	if len(api.deleted) != 0 {
		t.Fatalf("deleted = %v", api.deleted)
	}
	if _, err := os.Stat(filepath.Join(local, "remote.txt")); err != nil {
		t.Fatal(err)
	}
	if _, ok := api.objs["local.txt"]; !ok {
		t.Fatal("local not uploaded")
	}
}

func TestSameSizeEditUploads(t *testing.T) {
	api := &fakeAPI{}
	local := mkRoot(t)
	p := filepath.Join(local, "f.txt")
	write(t, p, "aaaa")
	eng := newTestEngine(t, api, []settings.SyncFolder{{Local: local, Enabled: true}})
	pass(eng)
	write(t, p, "bbbb")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	pass(eng)
	if got := string(api.objs["f.txt"]); got != "bbbb" {
		t.Fatalf("remote = %q", got)
	}
}

func TestEmptyLocalDoesNotWipeRemote(t *testing.T) {
	api := &fakeAPI{}
	local := mkRoot(t)
	write(t, filepath.Join(local, "a.txt"), "a")
	eng := newTestEngine(t, api, []settings.SyncFolder{{Local: local, Enabled: true}})
	pass(eng)
	if err := os.RemoveAll(local); err != nil { // e.g. unmounted drive
		t.Fatal(err)
	}
	pass(eng)
	if len(api.deleted) != 0 {
		t.Fatalf("mass delete: %v", api.deleted)
	}
}

func TestConflictName(t *testing.T) {
	now := time.Date(2026, 11, 25, 14, 30, 5, 0, time.UTC)
	got := conflictName("/x/db.kdbx", "pc", now)
	if got != "/x/db (conflict pc 2026-11-25 143005).kdbx" {
		t.Fatalf("got %q", got)
	}
}

// Server recompressing an uploaded .jpg must not replace the local original.
func TestServerRecompressDoesNotDownload(t *testing.T) {
	api := &fakeAPI{}
	local := mkRoot(t)
	write(t, filepath.Join(local, "p.jpg"), "original-bytes")
	eng := newTestEngine(t, api, []settings.SyncFolder{{Local: local, Enabled: true}})
	pass(eng)
	pass(eng) // etag adopted
	api.put("p.jpg", []byte("small"))
	api.sourceSize = map[string]int64{"p.jpg": int64(len("original-bytes"))}
	pass(eng)
	if len(api.downloads) != 0 {
		t.Fatalf("downloaded recompressed object: %v", api.downloads)
	}
	if got := read(t, filepath.Join(local, "p.jpg")); got != "original-bytes" {
		t.Fatalf("local = %q", got)
	}
}
