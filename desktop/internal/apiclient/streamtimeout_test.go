package apiclient

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A slow body must kill a short JSON call but not a stream: the old
// client-wide Timeout aborted a folder zip mid-transfer, which the
// desktop UI reported as "connection lost" and bounced to login.
func TestStreamingOutlivesCallTimeout(t *testing.T) {
	old := callTimeout
	callTimeout = 100 * time.Millisecond
	defer func() { callTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for i := 0; i < 5; i++ {
			time.Sleep(50 * time.Millisecond)
			w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "dev")

	if err := c.do("GET", "/api/me", nil, &PublicUser{}); err == nil {
		t.Fatal("short JSON call must hit its deadline")
	}
	dest := filepath.Join(t.TempDir(), "f.zip")
	if err := c.DownloadFolder("x/", dest); err != nil {
		t.Fatalf("stream must survive past callTimeout: %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "chunkchunkchunkchunkchunk" {
		t.Fatalf("truncated stream: %q", b)
	}
}
