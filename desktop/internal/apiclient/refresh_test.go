package apiclient

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeRefreshServer accepts only the current token on /api/me and
// rotates "rt1" into "rt2" on /auth/refresh.
func fakeRefreshServer(t *testing.T, refreshes *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/refresh":
			ck, err := r.Cookie("mist_rt")
			if err != nil || ck.Value != "rt1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			refreshes.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "mist_rt", Value: "rt2", Path: "/auth"})
			w.Write([]byte(`{"token":"fresh","user":{"login":"alice"}}`))
		case "/api/me":
			if r.Header.Get("Authorization") != "Bearer fresh" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"login":"alice"}`))
		}
	}))
}

func TestRefreshOn401(t *testing.T) {
	var refreshes atomic.Int32
	srv := fakeRefreshServer(t, &refreshes)
	defer srv.Close()

	c := New(srv.URL, "expired", "dev")
	var savedTok, savedRT string
	c.SetRefresh("rt1", func(tok, rt string) { savedTok, savedRT = tok, rt })

	u, err := c.Me()
	if err != nil || u.Login != "alice" {
		t.Fatalf("Me after refresh: u=%+v err=%v", u, err)
	}
	if savedTok != "fresh" || savedRT != "rt2" {
		t.Fatalf("rotation not persisted: tok=%q rt=%q", savedTok, savedRT)
	}
}

func TestRefreshSingleFlight(t *testing.T) {
	var refreshes atomic.Int32
	srv := fakeRefreshServer(t, &refreshes)
	defer srv.Close()

	c := New(srv.URL, "expired", "dev")
	c.SetRefresh("rt1", nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Me(); err != nil {
				t.Errorf("Me: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := refreshes.Load(); n != 1 {
		t.Fatalf("want 1 refresh for concurrent 401s, got %d", n)
	}
}

func TestNoRefreshCookieKeeps401(t *testing.T) {
	var refreshes atomic.Int32
	srv := fakeRefreshServer(t, &refreshes)
	defer srv.Close()

	c := New(srv.URL, "expired", "dev")
	if _, err := c.Me(); err == nil {
		t.Fatal("want 401 error without a refresh session")
	}
	if refreshes.Load() != 0 {
		t.Fatal("refresh called without a refresh cookie")
	}
}

func TestIsLoopbackURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://localhost:3000":     true,
		"https://drive.localhost":    true,
		"https://127.0.0.1":          true,
		"https://[::1]:8443":         true,
		"https://drive.example.com":  false,
		"https://localhost.evil.com": false,
		"https://192.168.1.10":       false,
		"not a url\x7f":              false,
	} {
		if got := IsLoopbackURL(url); got != want {
			t.Errorf("%q: got %v want %v", url, got, want)
		}
	}
}
