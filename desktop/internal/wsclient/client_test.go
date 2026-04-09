package wsclient

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildWSURL(t *testing.T) {
	cases := []struct {
		name, in, wantScheme, wantPath string
	}{
		{"http→ws", "http://localhost:3000", "ws", "/api/ws"},
		{"https→wss", "https://example.com", "wss", "/api/ws"},
		{"trailing slash stripped", "http://localhost:3000/", "ws", "/api/ws"},
		{"custom base path", "https://example.com/api-host", "wss", "/api-host/api/ws"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildWSURL(tc.in, "tok123")
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
			if u.Query().Get("token") != "tok123" {
				t.Errorf("token missing: %s", got)
			}
		})
	}
}

func TestBuildWSURLBadInput(t *testing.T) {
	if _, err := buildWSURL("://bad", "t"); err == nil {
		t.Fatal("expected error on malformed url")
	}
}

func TestBuildWSURLTokenEscaped(t *testing.T) {
	got, err := buildWSURL("http://localhost", "a b&c")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "a b&c") {
		t.Fatalf("token not query-escaped: %s", got)
	}
}
