// Package apiclient is the desktop app's thin wrapper around the
// mist-drive HTTP API. It mirrors the endpoints the web UI uses, but
// lives on the Go side so the sync engine (which has no DOM) can share
// the same call paths as the UI.
package apiclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

type Client struct {
	baseURL string
	token   string
	version string
	http    *http.Client
	// Optional outbound byte-rate limiter applied to every multipart
	// part PUT. Shared across all concurrent parts so the total throughput
	// across the process stays under the cap. nil = unlimited.
	uploadLimiter *rate.Limiter
	deviceCookie  string // trusted-device cookie value (mist_device)
}

func (c *Client) SetDeviceCookie(cookie string) { c.deviceCookie = cookie }

// SetUploadRateKBps installs / replaces the shared upload rate limiter.
// Pass 0 to disable (unlimited).
func (c *Client) SetUploadRateKBps(kbps int) {
	if kbps <= 0 {
		c.uploadLimiter = nil
		return
	}
	bps := rate.Limit(kbps * 1024)
	// Burst = 1s of bytes; small enough to throttle quickly, large
	// enough that we don't spend all our time waiting on tiny allocs.
	c.uploadLimiter = rate.NewLimiter(bps, kbps*1024)
}

// limitedReader applies a byte-rate limiter to an io.Reader. Used to
// throttle multipart part uploads across all in-flight PUTs.
type limitedReader struct {
	r   io.Reader
	lim *rate.Limiter
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	n, err := lr.r.Read(p)
	if n > 0 && lr.lim != nil {
		// WaitN blocks until n tokens are available. Context.Background
		// is fine — we never expect the limiter to be cancelled and the
		// http.Client Timeout will bound wall-clock time anyway.
		_ = lr.lim.WaitN(context.Background(), n)
	}
	return n, err
}

// ProgressFunc reports upload progress. Called concurrently from part
// goroutines; implementations must be safe for concurrent use.
type ProgressFunc func(loaded, total int64)

// progressReader counts bytes read into a shared atomic slot and fires
// a throttled aggregator so concurrent parts collapse into one callback
// no more than once per ~80ms.
type progressReader struct {
	r    io.Reader
	slot *atomic.Int64 // per-part bytes transferred
	fn   func()        // throttled aggregator
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 {
		pr.slot.Add(int64(n))
		pr.fn()
	}
	return n, err
}

type PublicUser struct {
	ID         string `json:"id"`
	Login      string `json:"login"`
	Role       string `json:"role"`
	QuotaBytes int64  `json:"quotaBytes"`
	UsedBytes  int64  `json:"usedBytes"`
}

type Features struct {
	SSO      bool `json:"sso"`
	AuditLog bool `json:"auditLog"`
}

type HealthResponse struct {
	OK       bool     `json:"ok"`
	Version  string   `json:"version"`
	Features Features `json:"features"`
}

func (c *Client) Health() (HealthResponse, error) {
	var r HealthResponse
	if err := c.do("GET", "/health", nil, &r); err != nil {
		return HealthResponse{}, err
	}
	return r, nil
}

type loginReq struct {
	Login          string `json:"login"`
	Password       string `json:"password"`
	ClientVersion  string `json:"clientVersion,omitempty"`
	TotpCode       string `json:"totpCode,omitempty"`
	RememberDevice bool   `json:"rememberDevice,omitempty"`
}

// LoginResult is the union returned by Login.
// When TotpRequired is true, the other fields are zero — caller must re-call with a code.
type LoginResult struct {
	TotpRequired bool       `json:"totp_required,omitempty"`
	Token        string     `json:"token,omitempty"`
	User         PublicUser `json:"user"`
	DeviceCookie string     `json:"-"` // extracted from Set-Cookie, not in body
}

// New builds a client against the given base URL. `insecureTLS` lets
// the user opt in to self-signed certs — required during local dev
// because the HTTPS enforcement of the original plan was relaxed.
func New(baseURL, token, version string, insecureTLS bool) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureTLS},
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		version: version,
		http:    &http.Client{Timeout: 30 * time.Second, Transport: tr},
	}
}

func (c *Client) SetToken(t string) { c.token = t }

func (c *Client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Client", "desktop")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.deviceCookie != "" {
		req.Header.Set("Cookie", "mist_device="+c.deviceCookie)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf("%d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// Login exchanges credentials for a JWT. Pass empty totpCode on first attempt;
// if TotpRequired is true in the response, call again with the user's code.
// rememberDevice=true causes the server to set a 30-day trusted-device cookie.
func (c *Client) Login(login, password, totpCode string, rememberDevice bool) (LoginResult, error) {
	body, err := json.Marshal(loginReq{
		Login: login, Password: password,
		ClientVersion:  c.version,
		TotpCode:       totpCode,
		RememberDevice: rememberDevice,
	})
	if err != nil {
		return LoginResult{}, err
	}
	req, err := http.NewRequest("POST", c.baseURL+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return LoginResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client", "desktop")
	req.Header.Set("User-Agent", "Mist Drive Desktop/"+c.version)
	if c.deviceCookie != "" {
		req.Header.Set("Cookie", "mist_device="+c.deviceCookie)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return LoginResult{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		msg, _ := io.ReadAll(res.Body)
		return LoginResult{}, fmt.Errorf("%d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	var r LoginResult
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return LoginResult{}, err
	}
	if r.TotpRequired {
		return r, nil
	}
	c.token = r.Token
	// Extract trusted-device cookie if server set one
	for _, ck := range res.Cookies() {
		if ck.Name == "mist_device" {
			r.DeviceCookie = ck.Value
			break
		}
	}
	return r, nil
}

// Me returns the currently authenticated user. Used to verify a
// restored token on startup and to keep quota info fresh in the UI.
func (c *Client) Me() (PublicUser, error) {
	if c.token == "" {
		return PublicUser{}, errors.New("not authenticated")
	}
	var u PublicUser
	if err := c.do("GET", "/api/me", nil, &u); err != nil {
		return PublicUser{}, err
	}
	return u, nil
}

// --- files ---

type ObjectInfo struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag"`
	LastModified string `json:"lastModified"`
	SourceSize   int64  `json:"sourceSize,omitempty"`
}

type ListResponse struct {
	Objects    []ObjectInfo `json:"objects"`
	Processing []string     `json:"processing"`
}

func (c *Client) ListFiles() (ListResponse, error) {
	var out ListResponse
	if err := c.do("GET", "/api/files?prefix=", nil, &out); err != nil {
		return ListResponse{}, err
	}
	return out, nil
}

func (c *Client) Rename(path, newName string) error {
	return c.do("POST", "/api/files/rename", map[string]string{"path": path, "newName": newName}, nil)
}

func (c *Client) DeleteFile(key string) error {
	return c.do("DELETE", "/api/files?key="+urlEscape(key), nil, nil)
}

func (c *Client) CreateFolder(path string) error {
	return c.do("POST", "/api/files/mkdir", map[string]string{"path": path}, nil)
}

func (c *Client) DeleteFolder(prefix string) error {
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return c.do("DELETE", "/api/files?prefix="+urlEscape(prefix), nil, nil)
}

// DownloadFile resolves the presigned GET URL from the API and streams
// the object to destPath. We deliberately don't buffer the body in
// memory — big files should flow straight to disk.
func (c *Client) DownloadFile(key, destPath string) error {
	var r struct {
		URL string `json:"url"`
	}
	if err := c.do("GET", "/api/files/download?key="+urlEscape(key), nil, &r); err != nil {
		return err
	}
	req, err := http.NewRequest("GET", r.URL, nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("s3 get %d", res.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, res.Body)
	return err
}

// DownloadFolder streams the API's zip endpoint for `prefix` directly
// to `destPath`. The server builds the archive on the fly so we just
// pipe the body to disk — no memory ceiling and no temp files.
func (c *Client) DownloadFolder(prefix, destPath string) error {
	req, err := http.NewRequest("GET", c.baseURL+"/api/files/download-zip?prefix="+urlEscape(prefix), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Client", "desktop")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("download-zip %d: %s", res.StatusCode, string(b))
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, res.Body)
	return err
}

// PreviewResult is returned by PreviewFile. Type is "image", "text", or "binary".
// Content holds a data-URI (image) or plain text; empty for binary.
type PreviewResult struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// PreviewFile fetches a preview of key from the API. Images are returned
// as data-URIs (base64 JPEG); text as a plain string; binary as an empty
// Content field with Type "binary".
func (c *Client) PreviewFile(key string) (PreviewResult, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/files/preview?key="+url.QueryEscape(key), nil)
	if err != nil {
		return PreviewResult{}, err
	}
	req.Header.Set("X-Client", "desktop")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return PreviewResult{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		msg, _ := io.ReadAll(res.Body)
		return PreviewResult{}, fmt.Errorf("%d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	ptype := res.Header.Get("X-Preview-Type")
	if ptype == "" {
		ptype = "binary"
	}
	switch ptype {
	case "image":
		data, err := io.ReadAll(res.Body)
		if err != nil {
			return PreviewResult{}, err
		}
		return PreviewResult{Type: "image", Content: "data:image/jpeg;base64," + encodeBase64(data)}, nil
	case "text":
		data, err := io.ReadAll(res.Body)
		if err != nil {
			return PreviewResult{}, err
		}
		return PreviewResult{Type: "text", Content: string(data)}, nil
	default:
		return PreviewResult{Type: "binary"}, nil
	}
}

// RecomputeUsage asks the API to rescan the user's bucket and rewrite
// usedBytes from authoritative S3 listings — same endpoint the web UI
// "recompute usage" link hits. Returns the new used-bytes total.
func (c *Client) RecomputeUsage() (int64, error) {
	var r struct {
		OK        bool  `json:"ok"`
		UsedBytes int64 `json:"usedBytes"`
		Count     int   `json:"count"`
	}
	if err := c.do("POST", "/api/files/recompute-usage", nil, &r); err != nil {
		return 0, err
	}
	return r.UsedBytes, nil
}

// --- multipart upload ---

const uploadPartSize = 8 * 1024 * 1024 // matches web uploader

type partURL struct {
	PartNumber int    `json:"partNumber"`
	URL        string `json:"url"`
}
type initUploadReq struct {
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	PartSize int64  `json:"partSize"`
}
type initUploadResp struct {
	UploadID string    `json:"uploadId"`
	PartSize int64     `json:"partSize"`
	URLs     []partURL `json:"urls"`
}
type completedPart struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
}
type completeReq struct {
	UploadID string          `json:"uploadId"`
	Parts    []completedPart `json:"parts"`
}

// UploadFile runs the full init / parallel PUT / complete flow for one
// local file. It satisfies the sync.API interface (no progress callback).
func (c *Client) UploadFile(localPath, remoteKey string, maxConcurrentParts int) error {
	return c.UploadFileWithProgress(context.Background(), localPath, remoteKey, maxConcurrentParts, nil)
}

// UploadFileWithProgress is the same as UploadFile but accepts a context
// for cancellation and an optional progress callback.
// onProgress may be nil. It is called from concurrent goroutines and
// is throttled to at most once per ~80ms to avoid event flooding.
func (c *Client) UploadFileWithProgress(ctx context.Context, localPath, remoteKey string, maxConcurrentParts int, onProgress ProgressFunc) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()

	var initResp initUploadResp
	if err := c.do("POST", "/api/files/upload/init", initUploadReq{
		Key: remoteKey, Size: size, PartSize: uploadPartSize,
	}, &initResp); err != nil {
		return err
	}

	// Per-part atomic counters for progress aggregation.
	slots := make([]atomic.Int64, len(initResp.URLs))
	var throttleMu sync.Mutex
	var lastEmit time.Time
	report := func() {
		if onProgress == nil {
			return
		}
		now := time.Now()
		throttleMu.Lock()
		if now.Sub(lastEmit) < 80*time.Millisecond {
			throttleMu.Unlock()
			return
		}
		lastEmit = now
		throttleMu.Unlock()
		var loaded int64
		for i := range slots {
			loaded += slots[i].Load()
		}
		onProgress(loaded, size)
	}

	parts := make([]completedPart, len(initResp.URLs))
	var mu sync.Mutex
	var firstErr error
	setErr := func(e error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = e
		}
	}

	if maxConcurrentParts <= 0 {
		maxConcurrentParts = 4
	}
	sem := make(chan struct{}, maxConcurrentParts)
	var wg sync.WaitGroup
	for i, p := range initResp.URLs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p partURL) {
			defer wg.Done()
			defer func() { <-sem }()
			if firstErr != nil || ctx.Err() != nil {
				return
			}
			start := int64(i) * initResp.PartSize
			end := min(start+initResp.PartSize, size)
			// Give each part its own SectionReader so concurrent reads
			// don't fight over the underlying file offset.
			var body io.Reader = io.NewSectionReader(f, start, end-start)
			if onProgress != nil {
				body = &progressReader{r: body, slot: &slots[i], fn: report}
			}
			if c.uploadLimiter != nil {
				body = &limitedReader{r: body, lim: c.uploadLimiter}
			}
			etag, err := c.putPart(ctx, p.URL, body, end-start)
			if err != nil {
				setErr(err)
				return
			}
			parts[i] = completedPart{PartNumber: p.PartNumber, ETag: etag}
		}(i, p)
	}
	wg.Wait()

	if firstErr != nil {
		// Best-effort abort so the server releases the quota reservation.
		// Uses a fresh background context — the upload ctx may be cancelled.
		_ = c.do("POST", "/api/files/upload/abort", map[string]string{"uploadId": initResp.UploadID}, nil)
		return firstErr
	}
	if ctx.Err() != nil {
		_ = c.do("POST", "/api/files/upload/abort", map[string]string{"uploadId": initResp.UploadID}, nil)
		return ctx.Err()
	}
	return c.do("POST", "/api/files/upload/complete",
		completeReq{UploadID: initResp.UploadID, Parts: parts}, nil)
}

func (c *Client) putPart(ctx context.Context, url string, body io.Reader, size int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", url, body)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("part %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	etag := strings.Trim(res.Header.Get("ETag"), `"`)
	return etag, nil
}

func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func urlEscape(s string) string {
	// Thin wrapper so import set stays small. Matches the web client's
	// use of encodeURIComponent.
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '~' {
			b.WriteRune(r)
			continue
		}
		for _, bt := range []byte(string(r)) {
			fmt.Fprintf(&b, "%%%02X", bt)
		}
	}
	return b.String()
}
