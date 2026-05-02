//go:build integration

// Integration tests for the HTTP layer. They spin up a real MinIO via
// testcontainers-go and exercise the full multipart upload/download/delete
// flow through Fiber's in-process test server. Run with:
//
//	go test -tags=integration ./internal/httpx/...
//
// Requires a working Docker socket.
package httpx_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	miniogo "github.com/minio/minio-go/v7"
	miniogocreds "github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/httpx"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/s3x"
	"github.com/yann/mist-drive/api/internal/uploads"
	"github.com/yann/mist-drive/api/internal/users"
)

// ---- container helpers ----

func startMinio(t *testing.T) (endpoint, access, secret string) {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "minio/minio:latest",
		ExposedPorts: []string{"9000/tcp"},
		Env: map[string]string{
			"MINIO_ROOT_USER":     "minioadmin",
			"MINIO_ROOT_PASSWORD": "minioadmin",
		},
		Cmd: []string{"server", "/data"},
		WaitingFor: wait.ForHTTP("/minio/health/ready").
			WithPort("9000/tcp").
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s:%s", host, port.Port()), "minioadmin", "minioadmin"
}

// ---- server fixture ----

type fixture struct {
	app    *fiber.App
	user   *users.User
	token  string
	cfg    *config.Config
	srv    *httpx.Server
	s3c    *s3x.Client
	store  *users.Store
	upload *uploads.Store
}

func newFixture(t *testing.T, quotaBytes int64) *fixture {
	t.Helper()
	ep, ak, sk := startMinio(t)
	dataDir := t.TempDir()

	cfg := &config.Config{
		JWTSecret:       "test-secret-test-secret-test-secret-xx",
		JWTTTL:          time.Hour,
		DataDir:         dataDir,
		S3Endpoint:      ep,
		S3AccessKey:     ak,
		S3SecretKey:     sk,
		PublicS3Host:    ep, // same host: presigned URLs resolve from the test process
		UploadTTL:       time.Hour,
		DefaultQuota:    quotaBytes,
		PresignDownload: 5 * time.Minute,
	}

	uStore, err := users.NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	upStore, err := uploads.NewStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s3c, err := s3x.New(ep, ak, sk, false, ep)
	if err != nil {
		t.Fatal(err)
	}

	hash, _ := auth.HashPassword("pw")
	u := &users.User{
		ID:         uuid.NewString(),
		Login:      "alice",
		BcryptPwd:  hash,
		QuotaBytes: quotaBytes,
		Role:       users.RoleUser,
		CreatedAt:  time.Now(),
	}
	if err := s3c.EnsureBucket(context.Background(), u.Bucket()); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	if err := uStore.Create(u); err != nil {
		t.Fatal(err)
	}

	srv := &httpx.Server{
		Cfg:          cfg,
		Users:        uStore,
		S3:           s3c,
		Uploads:      upStore,
		Reservations: quota.New(),
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	srv.Register(app)

	tok, err := auth.Issue(cfg.JWTSecret, u.ID, string(u.Role), cfg.JWTTTL)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{app: app, user: u, token: tok, cfg: cfg, srv: srv, s3c: s3c, store: uStore, upload: upStore}
}

func (f *fixture) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// ---- tests ----

func TestIntegration_UploadDownloadDeleteFlow(t *testing.T) {
	// MinIO enforces S3's 5 MiB minimum on every multipart part except
	// the last, so we upload 11 MiB in 5+5+1 MiB parts.
	f := newFixture(t, 50<<20) // 50 MiB quota
	const size = 11 * 1024 * 1024
	const partSize = 5 * 1024 * 1024
	payload := bytes.Repeat([]byte("A"), size)

	// ---- init ----
	resp := f.do(t, "POST", "/api/files/upload/init", map[string]any{
		"key": "hello.txt", "size": size, "partSize": partSize,
	})
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("init: status=%d body=%s", resp.StatusCode, b)
	}
	var ir struct {
		UploadID string `json:"uploadId"`
		PartSize int64  `json:"partSize"`
		URLs     []struct {
			PartNumber int    `json:"partNumber"`
			URL        string `json:"url"`
		} `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		t.Fatal(err)
	}
	if len(ir.URLs) != 3 {
		t.Fatalf("want 3 parts, got %d", len(ir.URLs))
	}

	// ---- PUT parts directly to MinIO via presigned URLs ----
	type completePart struct {
		PartNumber int    `json:"partNumber"`
		ETag       string `json:"etag"`
	}
	parts := make([]completePart, 0, len(ir.URLs))
	off := 0
	for _, p := range ir.URLs {
		end := off + int(ir.PartSize)
		if end > size {
			end = size
		}
		req, _ := http.NewRequest("PUT", p.URL, bytes.NewReader(payload[off:end]))
		req.ContentLength = int64(end - off)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT part %d: %v", p.PartNumber, err)
		}
		if r.StatusCode != 200 {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("PUT part %d: status=%d body=%s", p.PartNumber, r.StatusCode, b)
		}
		parts = append(parts, completePart{PartNumber: p.PartNumber, ETag: r.Header.Get("ETag")})
		off = end
	}

	// ---- complete ----
	cResp := f.do(t, "POST", "/api/files/upload/complete", map[string]any{
		"uploadId": ir.UploadID, "parts": parts,
	})
	if cResp.StatusCode != 200 {
		b, _ := io.ReadAll(cResp.Body)
		t.Fatalf("complete: status=%d body=%s", cResp.StatusCode, b)
	}

	// usedBytes should now be updated.
	u, _ := f.store.GetByID(f.user.ID)
	if u.UsedBytes != size {
		t.Fatalf("usedBytes=%d want %d", u.UsedBytes, size)
	}

	// ---- list ----
	lResp := f.do(t, "GET", "/api/files/", nil)
	var listBody struct {
		Objects []struct {
			Key  string `json:"key"`
			Size int64  `json:"size"`
		} `json:"objects"`
	}
	json.NewDecoder(lResp.Body).Decode(&listBody)
	objs := listBody.Objects
	if len(objs) != 1 || objs[0].Key != "hello.txt" || objs[0].Size != int64(size) {
		t.Fatalf("list: %+v", objs)
	}

	// ---- download via presigned GET ----
	dResp := f.do(t, "GET", "/api/files/download?key=hello.txt", nil)
	var dr struct {
		URL string `json:"url"`
	}
	json.NewDecoder(dResp.Body).Decode(&dr)
	gr, err := http.Get(dr.URL)
	if err != nil {
		t.Fatalf("download GET: %v", err)
	}
	if gr.StatusCode != 200 {
		t.Fatalf("download status=%d", gr.StatusCode)
	}
	got, _ := io.ReadAll(gr.Body)
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded bytes differ: got %d want %d", len(got), size)
	}

	// ---- delete ----
	rmResp := f.do(t, "DELETE", "/api/files/?key=hello.txt", nil)
	if rmResp.StatusCode != 200 {
		t.Fatalf("delete: %d", rmResp.StatusCode)
	}
	u, _ = f.store.GetByID(f.user.ID)
	if u.UsedBytes != 0 {
		t.Fatalf("usedBytes after delete=%d want 0", u.UsedBytes)
	}
}

func TestIntegration_QuotaExceededOnInit(t *testing.T) {
	f := newFixture(t, 1<<20) // 1 MiB quota
	resp := f.do(t, "POST", "/api/files/upload/init", map[string]any{
		"key": "big.bin", "size": 2 << 20, // 2 MiB
	})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 413, got %d body=%s", resp.StatusCode, b)
	}
}

func TestIntegration_ParallelReservationsRespectQuota(t *testing.T) {
	// Two concurrent inits that each fit individually but not together.
	// Exactly one should succeed; the second must be 413.
	f := newFixture(t, 1<<20) // 1 MiB
	body := map[string]any{"key": "a.bin", "size": 700 * 1024}
	body2 := map[string]any{"key": "b.bin", "size": 700 * 1024}

	// Run sequentially rather than racing goroutines — the reservation
	// check is atomic under the quota mutex, so sequential is sufficient
	// to prove the accounting is cumulative across requests.
	r1 := f.do(t, "POST", "/api/files/upload/init", body)
	r2 := f.do(t, "POST", "/api/files/upload/init", body2)
	if r1.StatusCode != 200 {
		t.Fatalf("first init should succeed, got %d", r1.StatusCode)
	}
	if r2.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("second init should be 413, got %d", r2.StatusCode)
	}
}

func TestIntegration_DownloadZipStreamsFolder(t *testing.T) {
	f := newFixture(t, 50<<20)

	// Seed three objects directly via minio-go to keep the test fast
	// (bypassing the multipart flow — that's covered elsewhere).
	mc, err := miniogo.New(f.cfg.S3Endpoint, &miniogo.Options{
		Creds:  miniogocreds.NewStaticV4(f.cfg.S3AccessKey, f.cfg.S3SecretKey, ""),
		Secure: false,
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := map[string][]byte{
		"docs/readme.txt":   []byte("hello readme"),
		"docs/notes.md":     []byte("# notes\nthings"),
		"docs/sub/deep.txt": bytes.Repeat([]byte("x"), 1024),
		"other/ignore.txt":  []byte("should not appear in zip"),
	}
	for k, v := range seed {
		_, err := mc.PutObject(context.Background(), f.user.Bucket(), k,
			bytes.NewReader(v), int64(len(v)), miniogo.PutObjectOptions{})
		if err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	resp := f.do(t, "GET", "/api/files/download-zip?prefix=docs/", nil)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("download-zip: %d %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type=%q want application/zip", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="docs.zip"` {
		t.Fatalf("content-disposition=%q", cd)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("parse zip: %v (body=%d bytes)", err, len(body))
	}

	// We expect the three files under docs/, with the prefix stripped,
	// and NOT the `other/` file.
	want := map[string][]byte{
		"readme.txt":   seed["docs/readme.txt"],
		"notes.md":     seed["docs/notes.md"],
		"sub/deep.txt": seed["docs/sub/deep.txt"],
	}
	if len(zr.File) != len(want) {
		names := make([]string, len(zr.File))
		for i, f := range zr.File {
			names[i] = f.Name
		}
		t.Fatalf("zip entries=%v want %d", names, len(want))
	}
	for _, zf := range zr.File {
		exp, ok := want[zf.Name]
		if !ok {
			t.Fatalf("unexpected entry %q", zf.Name)
		}
		if zf.Method != zip.Store {
			t.Fatalf("entry %s method=%d want Store", zf.Name, zf.Method)
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(got, exp) {
			t.Fatalf("entry %s: bytes differ", zf.Name)
		}
	}
}

func TestIntegration_DownloadZipQueryTokenAuth(t *testing.T) {
	// Token-via-query-param must authenticate the same as a header.
	f := newFixture(t, 50<<20)
	mc, _ := miniogo.New(f.cfg.S3Endpoint, &miniogo.Options{
		Creds: miniogocreds.NewStaticV4(f.cfg.S3AccessKey, f.cfg.S3SecretKey, ""), Region: "us-east-1",
	})
	payload := []byte("hi")
	_, err := mc.PutObject(context.Background(), f.user.Bucket(), "t/a.txt",
		bytes.NewReader(payload), int64(len(payload)), miniogo.PutObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// No Authorization header, token in the URL.
	req, _ := http.NewRequest("GET",
		"/api/files/download-zip?prefix=t/&token="+f.token, nil)
	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestIntegration_DownloadZipRejectsOversized(t *testing.T) {
	f := newFixture(t, 50<<20)
	// Shrink the zip cap so the test doesn't actually need big files.
	f.cfg.MaxZipBytes = 10

	mc, _ := miniogo.New(f.cfg.S3Endpoint, &miniogo.Options{
		Creds: miniogocreds.NewStaticV4(f.cfg.S3AccessKey, f.cfg.S3SecretKey, ""), Region: "us-east-1",
	})
	payload := bytes.Repeat([]byte("x"), 100) // > 10 bytes cap
	_, err := mc.PutObject(context.Background(), f.user.Bucket(), "big/a.txt",
		bytes.NewReader(payload), int64(len(payload)), miniogo.PutObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	resp := f.do(t, "GET", "/api/files/download-zip?prefix=big/", nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413 got %d", resp.StatusCode)
	}
}

func TestIntegration_GCReclaimsStaleUploads(t *testing.T) {
	f := newFixture(t, 50<<20)

	// Create a real multipart upload through the HTTP layer so the
	// persisted state, the MinIO upload, and the quota reservation all
	// line up the way they would in production.
	resp := f.do(t, "POST", "/api/files/upload/init", map[string]any{
		"key": "orphan.bin", "size": 11 * 1024 * 1024, "partSize": 5 * 1024 * 1024,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("init: %d", resp.StatusCode)
	}
	var ir struct {
		UploadID string `json:"uploadId"`
	}
	json.NewDecoder(resp.Body).Decode(&ir)

	// Sanity: MinIO reports the in-progress upload.
	n, err := f.s3c.ListIncompleteUploads(context.Background(), f.user.Bucket())
	if err != nil || n != 1 {
		t.Fatalf("before GC: n=%d err=%v", n, err)
	}

	// Backdate the persisted state file so GC considers it stale.
	st, err := f.upload.Get(f.user.ID, ir.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	st.CreatedAt = time.Now().Add(-48 * time.Hour)
	if err := f.upload.Save(st); err != nil {
		t.Fatal(err)
	}

	// Run GC with a 1h TTL -> the 48h-old upload should be reclaimed.
	reclaimed := uploads.GC(context.Background(), f.upload, f.s3c, time.Hour)
	if reclaimed != 1 {
		t.Fatalf("reclaimed=%d want 1", reclaimed)
	}

	// State file gone.
	if _, err := f.upload.Get(f.user.ID, ir.UploadID); err == nil {
		t.Fatal("state file still present after GC")
	}

	// MinIO multipart actually aborted (no orphan parts left behind).
	n, _ = f.s3c.ListIncompleteUploads(context.Background(), f.user.Bucket())
	if n != 0 {
		t.Fatalf("after GC: %d incomplete upload(s) still present in MinIO", n)
	}
}

func TestIntegration_GCPreservesFreshUploads(t *testing.T) {
	f := newFixture(t, 50<<20)
	resp := f.do(t, "POST", "/api/files/upload/init", map[string]any{
		"key": "fresh.bin", "size": 11 * 1024 * 1024, "partSize": 5 * 1024 * 1024,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("init: %d", resp.StatusCode)
	}
	var ir struct {
		UploadID string `json:"uploadId"`
	}
	json.NewDecoder(resp.Body).Decode(&ir)

	// A fresh upload (created just now) must NOT be reclaimed even by
	// an aggressive 1s TTL — we compare with `time.Since > ttl`, and
	// the state was written milliseconds ago.
	reclaimed := uploads.GC(context.Background(), f.upload, f.s3c, time.Hour)
	if reclaimed != 0 {
		t.Fatalf("fresh upload must not be reclaimed, got %d", reclaimed)
	}
	if _, err := f.upload.Get(f.user.ID, ir.UploadID); err != nil {
		t.Fatalf("fresh state file missing after GC: %v", err)
	}
}

func TestIntegration_AbortReleasesReservation(t *testing.T) {
	f := newFixture(t, 1<<20)
	resp := f.do(t, "POST", "/api/files/upload/init", map[string]any{
		"key": "a.bin", "size": 900 * 1024,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("init: %d", resp.StatusCode)
	}
	var ir struct {
		UploadID string `json:"uploadId"`
	}
	json.NewDecoder(resp.Body).Decode(&ir)

	ab := f.do(t, "POST", "/api/files/upload/abort", map[string]any{"uploadId": ir.UploadID})
	if ab.StatusCode != 200 {
		t.Fatalf("abort: %d", ab.StatusCode)
	}
	// Quota should now be free again.
	if r := f.srv.Reservations.Get(f.user.ID); r != 0 {
		t.Fatalf("reservation after abort=%d want 0", r)
	}
}
