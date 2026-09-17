package httpx_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// postWithCookie POSTs to path carrying the refresh cookie value (if any).
func postWithCookie(t *testing.T, app *fiber.App, path, cookie string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "mist_rt", Value: cookie})
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// rtCookie returns the mist_rt value set by resp, or "".
func rtCookie(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == "mist_rt" {
			return c.Value
		}
	}
	return ""
}

func refreshLogin(t *testing.T, f *unitFixture) string {
	t.Helper()
	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw", "refresh": true,
	}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("login: want 200, got %d", resp.StatusCode)
	}
	c := rtCookie(resp)
	if c == "" {
		t.Fatal("login with refresh=true set no refresh cookie")
	}
	return c
}

func TestLogin_NoRefreshByDefault(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{"login": "alice", "password": "pw"}, "")
	if resp.StatusCode != 200 || rtCookie(resp) != "" {
		t.Fatalf("plain login must not start a refresh session: status=%d cookie=%q", resp.StatusCode, rtCookie(resp))
	}
}

func TestRefresh_RotatesAndIssuesToken(t *testing.T) {
	f := newUnitFixture(t)
	first := refreshLogin(t, f)

	resp := postWithCookie(t, f.app, "/auth/refresh", first)
	if resp.StatusCode != 200 {
		t.Fatalf("refresh: want 200, got %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		t.Fatalf("refresh returned no token: %v", err)
	}
	if me := doUnit(t, f.app, "GET", "/api/me", nil, out.Token); me.StatusCode != 200 {
		t.Fatalf("refreshed token rejected by /api/me: %d", me.StatusCode)
	}
	second := rtCookie(resp)
	if second == "" || second == first {
		t.Fatal("refresh did not rotate the cookie")
	}

	// Old cookie within grace (concurrent tab): token, no new cookie.
	if r := postWithCookie(t, f.app, "/auth/refresh", first); r.StatusCode != 200 || rtCookie(r) != "" {
		t.Fatalf("grace refresh: status=%d cookie=%q", r.StatusCode, rtCookie(r))
	}
	// New cookie still works.
	if r := postWithCookie(t, f.app, "/auth/refresh", second); r.StatusCode != 200 {
		t.Fatalf("rotated cookie rejected: %d", r.StatusCode)
	}
}

func TestRefresh_ReuseRevokesSession(t *testing.T) {
	f := newUnitFixture(t)
	c := refreshLogin(t, f)
	id := c[:len(c)-64]
	if r := postWithCookie(t, f.app, "/auth/refresh", id+"deadbeef"); r.StatusCode != 401 {
		t.Fatalf("forged token: want 401, got %d", r.StatusCode)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", c); r.StatusCode != 401 {
		t.Fatalf("session must be revoked after reuse, got %d", r.StatusCode)
	}
}

func TestRefresh_LogoutAllRevokes(t *testing.T) {
	f := newUnitFixture(t)
	c := refreshLogin(t, f)
	if r := doUnit(t, f.app, "POST", "/api/me/logout-all", map[string]any{"password": "pw"}, f.userToken); r.StatusCode != 200 {
		t.Fatalf("logout-all: %d", r.StatusCode)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", c); r.StatusCode != 401 {
		t.Fatalf("refresh after logout-all: want 401, got %d", r.StatusCode)
	}
}

func TestLogout_DropsSession(t *testing.T) {
	f := newUnitFixture(t)
	c := refreshLogin(t, f)
	if r := postWithCookie(t, f.app, "/auth/logout", c); r.StatusCode != 200 {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", c); r.StatusCode != 401 {
		t.Fatalf("refresh after logout: want 401, got %d", r.StatusCode)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", ""); r.StatusCode != 401 {
		t.Fatalf("refresh without cookie: want 401, got %d", r.StatusCode)
	}
}

func TestChangePassword_RevokesOtherSessionsKeepsCaller(t *testing.T) {
	f := newUnitFixture(t)
	other := refreshLogin(t, f) // e.g. a stolen session elsewhere

	resp := doUnit(t, f.app, "PUT", "/api/me/password", map[string]any{
		"currentPassword": "pw", "newPassword": "pw2", "refresh": true,
	}, f.userToken)
	if resp.StatusCode != 200 {
		t.Fatalf("change password: %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		t.Fatalf("no replacement token: %v", err)
	}
	mine := rtCookie(resp)
	if mine == "" {
		t.Fatal("caller got no new refresh session")
	}

	if r := doUnit(t, f.app, "GET", "/api/me", nil, f.userToken); r.StatusCode != 401 {
		t.Fatalf("old access token: want 401, got %d", r.StatusCode)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", other); r.StatusCode != 401 {
		t.Fatalf("other refresh session: want 401, got %d", r.StatusCode)
	}
	if r := doUnit(t, f.app, "GET", "/api/me", nil, out.Token); r.StatusCode != 200 {
		t.Fatalf("replacement token: want 200, got %d", r.StatusCode)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", mine); r.StatusCode != 200 {
		t.Fatalf("caller refresh session: want 200, got %d", r.StatusCode)
	}
}

func TestRefresh_SessionsCappedPerUser(t *testing.T) {
	f := newUnitFixture(t)
	first := refreshLogin(t, f)
	for range 10 { // maxRefreshSessions more logins push the first one out
		refreshLogin(t, f)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", first); r.StatusCode != 401 {
		t.Fatalf("oldest session should be evicted, got %d", r.StatusCode)
	}
}

func TestRefreshCookie_Secure(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw", "refresh": true,
	}, "")
	for _, c := range resp.Cookies() {
		if c.Name == "mist_rt" && (!c.Secure || !c.HttpOnly || c.Path != "/auth") {
			t.Fatalf("refresh cookie attributes: %+v", c)
		}
	}
}

func TestSessions_ListAndRevoke(t *testing.T) {
	f := newUnitFixture(t)
	c := refreshLogin(t, f)
	refreshLogin(t, f)

	list := func(tok string) []struct {
		ID string `json:"id"`
	} {
		t.Helper()
		resp := doUnit(t, f.app, "GET", "/api/sessions", nil, tok)
		var out []struct {
			ID string `json:"id"`
		}
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
			t.Fatalf("list sessions: %d", resp.StatusCode)
		}
		return out
	}
	if got := list(f.userToken); len(got) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(got))
	}

	id := c[:36]
	if r := doUnit(t, f.app, "DELETE", "/api/sessions/"+id, nil, f.adminToken); r.StatusCode != 404 {
		t.Fatalf("other user's session: want 404, got %d", r.StatusCode)
	}
	if r := doUnit(t, f.app, "DELETE", "/api/sessions/"+id, nil, f.userToken); r.StatusCode != 200 {
		t.Fatalf("revoke own session: %d", r.StatusCode)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", c); r.StatusCode != 401 {
		t.Fatalf("revoked session still refreshes: %d", r.StatusCode)
	}
	if got := list(f.userToken); len(got) != 1 {
		t.Fatalf("want 1 session after revoke, got %d", len(got))
	}
}

func TestAdminRevokeAllSessions(t *testing.T) {
	f := newUnitFixture(t)
	c := refreshLogin(t, f)
	if r := doUnit(t, f.app, "POST", "/api/admin/sessions/revoke-all", nil, f.userToken); r.StatusCode != 403 {
		t.Fatalf("non-admin: want 403, got %d", r.StatusCode)
	}
	if r := doUnit(t, f.app, "POST", "/api/admin/sessions/revoke-all", nil, f.adminToken); r.StatusCode != 200 {
		t.Fatalf("revoke-all: %d", r.StatusCode)
	}
	if r := postWithCookie(t, f.app, "/auth/refresh", c); r.StatusCode != 401 {
		t.Fatalf("refresh after revoke-all: want 401, got %d", r.StatusCode)
	}
	for _, tok := range []string{f.userToken, f.adminToken} {
		if r := doUnit(t, f.app, "GET", "/api/me", nil, tok); r.StatusCode != 401 {
			t.Fatalf("access token after revoke-all: want 401, got %d", r.StatusCode)
		}
	}
}
