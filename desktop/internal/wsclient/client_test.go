package wsclient

import (
	"net/url"
	"testing"
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
