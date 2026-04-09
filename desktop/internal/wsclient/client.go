// Package wsclient is the desktop side of the /api/ws push channel.
// It keeps a reconnecting websocket open to the API and invokes the
// provided callback on every "files-changed" envelope — the envelope
// carries no deltas, so the callback is just a "refresh your view"
// signal. Two consumers wire into it: the sync engine (via Nudge()
// to trigger an immediate reconcile) and the Wails frontend (via a
// runtime event so the Files screen re-fetches).
package wsclient

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Client struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	on     func()
	log    func(string)
}

func New(onEvent func(), log func(string)) *Client {
	if log == nil {
		log = func(string) {}
	}
	return &Client{on: onEvent, log: log}
}

// Start opens the ws connection in the background and keeps it alive
// with capped exponential backoff. Calling Start while already running
// is a no-op, mirroring the engine's idempotency.
func (c *Client) Start(apiURL, token string) {
	c.Stop()
	if apiURL == "" || token == "" {
		return
	}
	wsURL, err := buildWSURL(apiURL, token)
	if err != nil {
		c.log("ws url: " + err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	go c.loop(ctx, wsURL)
}

func (c *Client) Stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.mu.Unlock()
}

func (c *Client) loop(ctx context.Context, wsURL string) {
	backoff := 500 * time.Millisecond
	// InsecureSkipVerify mirrors the apiclient — dev uses self-signed or
	// plain http, and the JWT in the URL is the real authenticator.
	httpc := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	for {
		if ctx.Err() != nil {
			return
		}
		conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpc})
		if err != nil {
			c.log("ws dial: " + err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 500 * time.Millisecond
		c.read(ctx, conn)
		conn.Close(websocket.StatusNormalClosure, "")
	}
}

func (c *Client) read(ctx context.Context, conn *websocket.Conn) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		// Envelope is just {"type":"files-changed"}; we don't bother
		// unmarshalling — any message means "re-fetch".
		_ = data
		if c.on != nil {
			c.on()
		}
	}
}

func buildWSURL(apiURL, token string) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/ws"
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
