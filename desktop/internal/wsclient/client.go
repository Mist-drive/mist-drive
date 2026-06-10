// Package wsclient is the desktop side of the /ws push channel.
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
	"encoding/json"
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
	on     func(eventType, message, path string)
	log    func(string)
}

func New(onEvent func(eventType, message, path string), log func(string)) *Client {
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
	wsURL, err := buildWSURL(apiURL)
	if err != nil {
		c.log("ws url: " + err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	go c.loop(ctx, wsURL, token)
}

func (c *Client) Stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.mu.Unlock()
}

func (c *Client) loop(ctx context.Context, wsURL, token string) {
	backoff := 500 * time.Millisecond
	// InsecureSkipVerify mirrors the apiclient — dev uses self-signed or
	// plain http. The JWT is sent as the first message, not in the URL.
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
		// First-message auth: the server validates this before pushing
		// anything. If the write fails the read loop below will error out
		// immediately and we reconnect.
		if authMsg, mErr := json.Marshal(map[string]string{"type": "auth", "token": token}); mErr == nil {
			_ = conn.Write(ctx, websocket.MessageText, authMsg)
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
		if c.on == nil {
			continue
		}
		var envelope struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Path    string `json:"path"`
		}
		if jsonErr := json.Unmarshal(data, &envelope); jsonErr == nil {
			c.on(envelope.Type, envelope.Message, envelope.Path)
		} else {
			c.on("files-changed", "", "")
		}
	}
}

func buildWSURL(apiURL string) (string, error) {
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
	// Top-level /ws (not /api/ws): the route lives outside the JWT-auth
	// group and authenticates via the first message instead of the URL.
	u.Path = strings.TrimRight(u.Path, "/") + "/ws"
	return u.String(), nil
}
