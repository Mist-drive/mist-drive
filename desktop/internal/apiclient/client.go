// Package apiclient is the desktop app's thin wrapper around the
// mist-drive HTTP API. It mirrors the endpoints the web UI uses, but
// lives on the Go side so the sync engine (which has no DOM) can share
// the same call paths as the UI.
package apiclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// Optional outbound byte-rate limiter applied to every multipart
	// part PUT. Shared across all concurrent parts so the total throughput
	// across the process stays under the cap. nil = unlimited.
	uploadLimiter *rate.Limiter
}

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

type PublicUser struct {
	ID         string `json:"id"`
	Login      string `json:"login"`
	Role       string `json:"role"`
	QuotaBytes int64  `json:"quotaBytes"`
	UsedBytes  int64  `json:"usedBytes"`
}

type loginReq struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}
type loginResp struct {
	Token string     `json:"token"`
	User  PublicUser `json:"user"`
}

// New builds a client against the given base URL. `insecureTLS` lets
// the user opt in to self-signed certs — required during local dev
// because the HTTPS enforcement of the original plan was relaxed.
func New(baseURL, token string, insecureTLS bool) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureTLS},
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
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
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
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

// Login exchanges credentials for a JWT + user. The returned token
// should be persisted by the caller.
func (c *Client) Login(login, password string) (string, PublicUser, error) {
	var r loginResp
	if err := c.do("POST", "/auth/login", loginReq{login, password}, &r); err != nil {
		return "", PublicUser{}, err
	}
	c.token = r.Token
	return r.Token, r.User, nil
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
}

func (c *Client) ListFiles() ([]ObjectInfo, error) {
	var out []ObjectInfo
	if err := c.do("GET", "/api/files?prefix=", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
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
// local file. Concurrency and rate limits come from settings; for now
// we honour maxConcurrentParts (falls back to 4). Rate limiting is a
// phase-3 concern (it applies across the whole sync engine, not just
// one-off UI uploads), so we leave a TODO hook here.
func (c *Client) UploadFile(localPath, remoteKey string, maxConcurrentParts int) error {
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
			if firstErr != nil {
				return
			}
			start := int64(i) * initResp.PartSize
			end := start + initResp.PartSize
			if end > size {
				end = size
			}
			// Give each part its own SectionReader so concurrent reads
			// don't fight over the underlying file offset.
			sr := io.NewSectionReader(f, start, end-start)
			etag, err := c.putPart(p.URL, sr, end-start)
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
		_ = c.do("POST", "/api/files/upload/abort", map[string]string{"uploadId": initResp.UploadID}, nil)
		return firstErr
	}
	return c.do("POST", "/api/files/upload/complete",
		completeReq{UploadID: initResp.UploadID, Parts: parts}, nil)
}

func (c *Client) putPart(url string, body io.Reader, size int64) (string, error) {
	if c.uploadLimiter != nil {
		body = &limitedReader{r: body, lim: c.uploadLimiter}
	}
	req, err := http.NewRequest("PUT", url, body)
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
