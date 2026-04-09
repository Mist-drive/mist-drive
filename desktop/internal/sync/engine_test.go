package sync

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"mist-drive-desktop/internal/apiclient"
	"mist-drive-desktop/internal/settings"
)

// fakeAPI implements the API interface for engine tests. It records
// every call and returns whatever remote listing the test plants.
type fakeAPI struct {
	mu        sync.Mutex
	remote    []apiclient.ObjectInfo
	uploaded  []string
	downloads []string
	deleted   []string
	rate      int
}

func (f *fakeAPI) ListFiles() ([]apiclient.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]apiclient.ObjectInfo, len(f.remote))
	copy(out, f.remote)
	return out, nil
}
func (f *fakeAPI) UploadFile(local, key string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploaded = append(f.uploaded, key)
	// Simulate the server side: fresh objects now exist remotely.
	info, _ := os.Stat(local)
	f.remote = append(f.remote, apiclient.ObjectInfo{Key: key, Size: info.Size()})
	return nil
}
func (f *fakeAPI) DownloadFile(key, dest string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads = append(f.downloads, key)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, []byte("remote"), 0o644)
}
func (f *fakeAPI) DeleteFile(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, key)
	out := f.remote[:0]
	for _, o := range f.remote {
		if o.Key != key {
			out = append(out, o)
		}
	}
	f.remote = out
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
		Local: local, RemotePrefix: "p/", Upload: true, Download: true, Enabled: true,
	}})
	eng.reconcileAll(context.Background())
	if len(api.uploaded) != 1 || api.uploaded[0] != "p/a.txt" {
		t.Fatalf("uploaded = %v", api.uploaded)
	}
	if got := eng.Status().Uploaded; got != 1 {
		t.Errorf("Uploaded counter = %d", got)
	}
}

func TestReconcileDownloadsRemoteOnly(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	api := &fakeAPI{remote: []apiclient.ObjectInfo{{Key: "p/b.txt", Size: 6}}}
	eng := newTestEngine(t, api, []settings.SyncFolder{{
		Local: local, RemotePrefix: "p/", Upload: true, Download: true, Enabled: true,
	}})
	eng.reconcileAll(context.Background())
	if len(api.downloads) != 1 || api.downloads[0] != "p/b.txt" {
		t.Fatalf("downloads = %v", api.downloads)
	}
}

func TestReconcileSkipsDisabledFolder(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	_ = os.MkdirAll(local, 0o755)
	_ = os.WriteFile(filepath.Join(local, "a.txt"), []byte("x"), 0o644)
	api := &fakeAPI{}
	eng := newTestEngine(t, api, []settings.SyncFolder{{
		Local: local, Upload: true, Download: true, Enabled: false,
	}})
	eng.reconcileAll(context.Background())
	if len(api.uploaded) != 0 {
		t.Fatalf("disabled folder still uploaded: %v", api.uploaded)
	}
}

func TestReconcileSizeMismatchReuploads(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	_ = os.MkdirAll(local, 0o755)
	_ = os.WriteFile(filepath.Join(local, "a.txt"), []byte("hellohello"), 0o644)
	api := &fakeAPI{remote: []apiclient.ObjectInfo{{Key: "a.txt", Size: 5}}}
	eng := newTestEngine(t, api, []settings.SyncFolder{{
		Local: local, Upload: true, Download: true, Enabled: true,
	}})
	eng.reconcileAll(context.Background())
	if len(api.uploaded) != 1 {
		t.Fatalf("expected reupload, got %v", api.uploaded)
	}
}

func TestReconcileUploadOnlyDeletesRemoteWhenLocalGone(t *testing.T) {
	// Regression: user deletes a file from their file manager in an
	// upload-only folder — the remote copy must be removed to match.
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	_ = os.MkdirAll(local, 0o755)
	api := &fakeAPI{remote: []apiclient.ObjectInfo{{Key: "pics/shot.png", Size: 12}}}
	eng := newTestEngine(t, api, []settings.SyncFolder{{
		Local: local, RemotePrefix: "pics/", Upload: true, Download: false, Enabled: true,
	}})
	eng.reconcileAll(context.Background())
	if len(api.deleted) != 1 || api.deleted[0] != "pics/shot.png" {
		t.Fatalf("deleted = %v", api.deleted)
	}
	if len(api.downloads) != 0 {
		t.Fatalf("should not have downloaded: %v", api.downloads)
	}
}

func TestReconcileBidirectionalDownloadsRemoteOnly(t *testing.T) {
	// In bidirectional mode we can't distinguish a local delete from a
	// new remote file without an index, so remote-only files are still
	// downloaded (safer default).
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	_ = os.MkdirAll(local, 0o755)
	api := &fakeAPI{remote: []apiclient.ObjectInfo{{Key: "x.txt", Size: 6}}}
	eng := newTestEngine(t, api, []settings.SyncFolder{{
		Local: local, Upload: true, Download: true, Enabled: true,
	}})
	eng.reconcileAll(context.Background())
	if len(api.deleted) != 0 {
		t.Fatalf("should not have deleted in bidir mode: %v", api.deleted)
	}
	if len(api.downloads) != 1 {
		t.Fatalf("should have downloaded: %v", api.downloads)
	}
}

func TestReconcileDownloadOnlyFolderDoesNotUpload(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "root")
	_ = os.MkdirAll(local, 0o755)
	_ = os.WriteFile(filepath.Join(local, "a.txt"), []byte("x"), 0o644)
	api := &fakeAPI{}
	eng := newTestEngine(t, api, []settings.SyncFolder{{
		Local: local, Upload: false, Download: true, Enabled: true,
	}})
	eng.reconcileAll(context.Background())
	if len(api.uploaded) != 0 {
		t.Fatalf("uploaded despite upload=false: %v", api.uploaded)
	}
	if eng.Status().Skipped == 0 {
		t.Error("expected skipped counter to bump")
	}
}
