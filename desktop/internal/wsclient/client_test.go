package wsclient

import (
	"net/url"
	"testing"
	"time"
)

func TestBuildWSURL(t *testing.T) {
	cases := []struct {
		name, in, wantScheme, wantPath string
	}{
		{"http→ws", "http://localhost:3000", "ws", "/ws"},
		{"https→wss", "https://example.com", "wss", "/ws"},
		{"trailing slash stripped", "http://localhost:3000/", "ws", "/ws"},
		{"custom base path", "https://example.com/api-host", "wss", "/api-host/ws"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildWSURL(tc.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if u.Scheme != tc.wantScheme {
				t.Errorf("scheme = %s, want %s", u.Scheme, tc.wantScheme)
			}
			if u.Path != tc.wantPath {
				t.Errorf("path = %s, want %s", u.Path, tc.wantPath)
			}
			// The token must never appear in the URL — it's sent as the
			// first message instead.
			if u.Query().Get("token") != "" {
				t.Errorf("token must not be in URL: %s", got)
			}
		})
	}
}

func TestBuildWSURLBadInput(t *testing.T) {
	if _, err := buildWSURL("://bad"); err == nil {
		t.Fatal("expected error on malformed url")
	}
}

// TestNextBackoff_GrowsOnRejection guards against the original bug: a
// connection that dies almost immediately after the auth frame (the
// server rejecting a bad/expired/revoked token) must make backoff grow,
// not reset — otherwise a stale token spins the loop hammering /ws as
// fast as TCP handshakes allow.
func TestNextBackoff_GrowsOnRejection(t *testing.T) {
	backoff := 500 * time.Millisecond
	for i, want := range []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		10 * time.Second, 10 * time.Second, // caps at 10s
	} {
		backoff = nextBackoff(backoff, false)
		if backoff != want {
			t.Fatalf("iteration %d: got %v, want %v", i, backoff, want)
		}
	}
}

// TestNextBackoff_ResetsOnSurvivedSession guards the other half: once a
// connection proved itself authenticated (lasted past wsAuthGrace), the
// next reconnect after a real disconnect should be fast again, not stuck
// at whatever backoff a prior rejection streak grew to.
func TestNextBackoff_ResetsOnSurvivedSession(t *testing.T) {
	backoff := 10 * time.Second // simulate a fully backed-off state
	backoff = nextBackoff(backoff, true)
	if want := 500 * time.Millisecond; backoff != want {
		t.Fatalf("got %v, want %v", backoff, want)
	}
}
