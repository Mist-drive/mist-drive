package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPutPartEmptySendsContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// S3 presigned PUTs answer 411 to a chunked body without length.
		if len(r.TransferEncoding) > 0 || r.Header.Get("Content-Length") != "0" {
			w.WriteHeader(http.StatusLengthRequired)
			return
		}
		w.Header().Set("ETag", `"empty"`)
	}))
	defer srv.Close()
	c := New(srv.URL, "", "dev")
	// Non-nil body wrapper, like Upload's progress/limit readers.
	body := &progressReader{r: strings.NewReader(""), slot: new(atomic.Int64), fn: func() {}}
	etag, err := c.putPart(context.Background(), srv.URL, body, 0)
	if err != nil || etag != "empty" {
		t.Fatalf("empty part: etag=%q err=%v", etag, err)
	}
}
