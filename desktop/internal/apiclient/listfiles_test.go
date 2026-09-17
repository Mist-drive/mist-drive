package apiclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestListFilesRevalidatesWithETag(t *testing.T) {
	var version, bodies, notModified atomic.Int32
	version.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := version.Load()
		etag := fmt.Sprintf(`"v%d"`, v)
		if r.Header.Get("If-None-Match") == etag {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		bodies.Add(1)
		w.Header().Set("ETag", etag)
		fmt.Fprintf(w, `{"objects":[{"key":"v%d"}],"processing":[]}`, v)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "dev")

	if first, err := c.ListFiles(); err != nil || len(first.Objects) != 1 {
		t.Fatalf("first list: %+v %v", first, err)
	}
	second, err := c.ListFiles()
	if err != nil || len(second.Objects) != 1 || second.Objects[0].Key != "v1" {
		t.Fatalf("304 must replay cached list: %+v %v", second, err)
	}
	if bodies.Load() != 1 || notModified.Load() != 1 {
		t.Fatalf("want 1 body + 1 304, got %d bodies %d 304s", bodies.Load(), notModified.Load())
	}

	version.Store(2) // bucket changed server-side
	if third, err := c.ListFiles(); err != nil || third.Objects[0].Key != "v2" {
		t.Fatalf("changed etag must return fresh list: %+v %v", third, err)
	}
	if _, err := c.ListFiles(); err != nil || notModified.Load() != 2 {
		t.Fatalf("new etag not stored: 304s=%d err=%v", notModified.Load(), err)
	}
}
