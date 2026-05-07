package httpx_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/httpx"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/uploads"
	"github.com/yann/mist-drive/api/internal/users"
)

const unitSecret = "test-secret-32bytesxxxxxxxxxx!!"

type unitFixture struct {
	app       *fiber.App
	alice     *users.User
	userToken string
	adminID   string
	adminToken string
}

func newUnitFixture(t *testing.T) *unitFixture {
	t.Helper()
	dataDir := t.TempDir()

	cfg := &config.Config{
		JWTSecret:    unitSecret,
		JWTTTL:       time.Hour,
		DataDir:      dataDir,
		DefaultQuota: 10 << 30,
	}

	uStore, err := users.NewStore(dataDir)
	if err != nil {
		t.Fatalf("users.NewStore: %v", err)
	}
	upStore, err := uploads.NewStore(dataDir)
	if err != nil {
		t.Fatalf("uploads.NewStore: %v", err)
	}

	hash, err := auth.HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	alice := &users.User{
		ID:         uuid.NewString(),
		Login:      "alice",
		BcryptPwd:  hash,
		QuotaBytes: 10 << 30,
		Role:       users.RoleUser,
		CreatedAt:  time.Now(),
	}
	if err := uStore.Create(alice); err != nil {
		t.Fatal(err)
	}

	adminHash, err := auth.HashPassword("adminpw")
	if err != nil {
		t.Fatal(err)
	}
	adminID := uuid.NewString()
	admin := &users.User{
		ID:         adminID,
		Login:      "admin",
		BcryptPwd:  adminHash,
		QuotaBytes: 10 << 30,
		Role:       users.RoleAdmin,
		CreatedAt:  time.Now(),
	}
	if err := uStore.Create(admin); err != nil {
		t.Fatal(err)
	}

	srv := &httpx.Server{
		Cfg:          cfg,
		Users:        uStore,
		Uploads:      upStore,
		Reservations: quota.New(),
		Version:      "dev",
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	srv.Register(app)

	userTok, err := auth.Issue(unitSecret, alice.ID, string(alice.Role), 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	adminTok, err := auth.Issue(unitSecret, adminID, string(users.RoleAdmin), 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	return &unitFixture{
		app:        app,
		alice:      alice,
		userToken:  userTok,
		adminID:    adminID,
		adminToken: adminTok,
	}
}

func doUnit(t *testing.T, app *fiber.App, method, path string, body any, token string) *http.Response {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// ---- Login ----

func TestLogin_Success(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "pw",
	}, "")
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Token string `json:"token"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" {
		t.Fatal("token is empty")
	}
	if out.User.Login != "alice" {
		t.Fatalf("user.login: got %q want %q", out.User.Login, "alice")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "wrong",
	}, "")
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestLogin_MissingFields(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/auth/login", map[string]any{}, "")
	if resp.StatusCode != 401 {
		// Empty login/password → user not found → 401
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestLogin_VersionMismatch(t *testing.T) {
	// Build a fresh app with a pinned server version so the version check fires.
	dataDir := t.TempDir()
	uStore, _ := users.NewStore(dataDir)
	hash, _ := auth.HashPassword("pw")
	_ = uStore.Create(&users.User{
		ID: uuid.NewString(), Login: "bob", BcryptPwd: hash,
		QuotaBytes: 10 << 30, Role: users.RoleUser, CreatedAt: time.Now(),
	})
	upStore, _ := uploads.NewStore(dataDir)
	srv := &httpx.Server{
		Cfg: &config.Config{
			JWTSecret: unitSecret,
			JWTTTL:    time.Hour,
			DataDir:   dataDir,
		},
		Users:        uStore,
		Uploads:      upStore,
		Reservations: quota.New(),
		Version:      "1.2.3",
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	srv.Register(app)

	resp := doUnit(t, app, "POST", "/auth/login", map[string]any{
		"login": "bob", "password": "pw", "clientVersion": "9.9.9",
	}, "")
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// ---- Me ----

func TestMe_ReturnsUser(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "GET", "/api/me", nil, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Login != "alice" {
		t.Fatalf("login: got %q want %q", out.Login, "alice")
	}
}

// ---- Admin list users ----

func TestAdminListUsers_AdminOnly(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "GET", "/api/admin/users", nil, f.userToken)
	if resp.StatusCode != 403 {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func TestAdminListUsers_Success(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "GET", "/api/admin/users", nil, f.adminToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var out []struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range out {
		if u.Login == "alice" {
			found = true
		}
	}
	if !found {
		t.Fatalf("alice not in user list: %+v", out)
	}
}

// ---- Admin patch quota ----

func TestAdminPatchQuota_Success(t *testing.T) {
	f := newUnitFixture(t)
	const fiveGiB = 5 * 1024 * 1024 * 1024
	resp := doUnit(t, f.app, "PATCH", "/api/admin/users/"+f.alice.ID+"/quota",
		map[string]any{"quotaBytes": fiveGiB}, f.adminToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		QuotaBytes int64 `json:"quotaBytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.QuotaBytes != fiveGiB {
		t.Fatalf("quotaBytes: got %d want %d", out.QuotaBytes, fiveGiB)
	}
}

func TestAdminPatchQuota_BadBody(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "PATCH", "/api/admin/users/"+f.alice.ID+"/quota",
		map[string]any{"quotaBytes": 0}, f.adminToken)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestAdminPatchQuota_NotFound(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "PATCH", "/api/admin/users/does-not-exist/quota",
		map[string]any{"quotaBytes": 1024}, f.adminToken)
	if resp.StatusCode != 404 {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// ---- Update email ----

func TestUpdateEmail_Success(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "PUT", "/api/me/email",
		map[string]any{"email": "alice@example.com"}, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Email != "alice@example.com" {
		t.Fatalf("email: got %q want %q", out.Email, "alice@example.com")
	}
}

func TestUpdateEmail_Clear(t *testing.T) {
	f := newUnitFixture(t)
	// Set then clear
	doUnit(t, f.app, "PUT", "/api/me/email", map[string]any{"email": "alice@example.com"}, f.userToken)
	resp := doUnit(t, f.app, "PUT", "/api/me/email", map[string]any{"email": ""}, f.userToken)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestUpdateEmail_InvalidEmail(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "PUT", "/api/me/email",
		map[string]any{"email": "not-an-email"}, f.userToken)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestUpdateEmail_Unauthenticated(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "PUT", "/api/me/email", map[string]any{"email": "x@x.com"}, "")
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

// ---- Change password ----

func TestChangePassword_Success(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "PUT", "/api/me/password", map[string]any{
		"currentPassword": "pw",
		"newPassword":     "newpw123",
	}, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	// Old token still valid (TokenVersion unchanged). Login with new pwd.
	resp2 := doUnit(t, f.app, "POST", "/auth/login", map[string]any{
		"login": "alice", "password": "newpw123",
	}, "")
	if resp2.StatusCode != 200 {
		t.Fatal("login with new password should succeed")
	}
}

func TestChangePassword_WrongCurrent(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "PUT", "/api/me/password", map[string]any{
		"currentPassword": "wrong",
		"newPassword":     "newpw123",
	}, f.userToken)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestChangePassword_MissingFields(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "PUT", "/api/me/password", map[string]any{
		"currentPassword": "pw",
	}, f.userToken)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// ---- Logout all (token version) ----

func TestLogoutAll_Success(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/api/me/logout-all",
		map[string]any{"password": "pw"}, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestLogoutAll_WrongPassword(t *testing.T) {
	f := newUnitFixture(t)
	resp := doUnit(t, f.app, "POST", "/api/me/logout-all",
		map[string]any{"password": "wrong"}, f.userToken)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestLogoutAll_RevokesOldToken(t *testing.T) {
	f := newUnitFixture(t)
	// Revoke all sessions with current password
	resp := doUnit(t, f.app, "POST", "/api/me/logout-all",
		map[string]any{"password": "pw"}, f.userToken)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("logout-all want 200, got %d: %s", resp.StatusCode, body)
	}
	// Old token must now return 401
	resp2 := doUnit(t, f.app, "GET", "/api/me", nil, f.userToken)
	if resp2.StatusCode != 401 {
		t.Fatalf("old token should be revoked (want 401), got %d", resp2.StatusCode)
	}
}

func TestLogoutAll_NewTokenStillValid(t *testing.T) {
	f := newUnitFixture(t)
	// Revoke all
	doUnit(t, f.app, "POST", "/api/me/logout-all",
		map[string]any{"password": "pw"}, f.userToken)
	// Re-login to get a fresh token
	resp := doUnit(t, f.app, "POST", "/auth/login",
		map[string]any{"login": "alice", "password": "pw"}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("re-login want 200, got %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// New token must work
	resp2 := doUnit(t, f.app, "GET", "/api/me", nil, out.Token)
	if resp2.StatusCode != 200 {
		t.Fatalf("new token should be valid, got %d", resp2.StatusCode)
	}
}

// ---- Processing state ----

func TestProcessingState(t *testing.T) {
	srv := &httpx.Server{}

	srv.AddProcessing("u1", "docs")
	if !srv.IsProcessingBlocked("u1", "docs/readme.txt") {
		t.Fatal("docs/readme.txt should be blocked under docs")
	}
	if srv.IsProcessingBlocked("u1", "other/file.txt") {
		t.Fatal("other/file.txt should not be blocked")
	}

	srv.RemoveProcessing("u1", "docs")
	if srv.IsProcessingBlocked("u1", "docs/readme.txt") {
		t.Fatal("docs/readme.txt should be unblocked after remove")
	}
}
