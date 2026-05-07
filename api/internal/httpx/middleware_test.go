package httpx_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/httpx"
)

const middlewareSecret = "test-secret-32bytesxxxxxxxxxx!!"

func newMiddlewareApp(secret string) *fiber.App {
	// zero bootTime so all freshly-issued tokens are considered post-boot
	return newMiddlewareAppWithBoot(secret, time.Time{})
}

func newMiddlewareAppWithBoot(secret string, bootTime time.Time) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/protected", httpx.AuthMiddleware(secret, bootTime), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true, "uid": httpx.UID(c)})
	})
	app.Get("/admin", httpx.AuthMiddleware(secret, bootTime), httpx.AdminOnly, func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})
	return app
}

func issueToken(t *testing.T, secret, uid, role string) string {
	t.Helper()
	tok, err := auth.Issue(secret, uid, role, time.Hour)
	if err != nil {
		t.Fatalf("auth.Issue: %v", err)
	}
	return tok
}

func TestAuthMiddleware_NoToken(t *testing.T) {
	app := newMiddlewareApp(middlewareSecret)
	req, _ := http.NewRequest("GET", "/protected", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	app := newMiddlewareApp(middlewareSecret)
	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer this-is-garbage")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_ValidHeader(t *testing.T) {
	app := newMiddlewareApp(middlewareSecret)
	tok := issueToken(t, middlewareSecret, "uid1", "user")

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) == "" {
		t.Fatal("empty body")
	}
	// UID must be propagated — the handler returns it in the JSON response.
	if !contains(body, "uid1") {
		t.Fatalf("UID not propagated in response: %s", body)
	}
}

func TestAuthMiddleware_QueryParam(t *testing.T) {
	app := newMiddlewareApp(middlewareSecret)
	tok := issueToken(t, middlewareSecret, "uid1", "user")

	req, _ := http.NewRequest("GET", "/protected?token="+tok, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestAdminOnly_UserRole(t *testing.T) {
	app := newMiddlewareApp(middlewareSecret)
	tok := issueToken(t, middlewareSecret, "uid1", "user")

	req, _ := http.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_PreBootToken(t *testing.T) {
	tok := issueToken(t, middlewareSecret, "uid1", "user")
	// Boot time set 1s in the future so the just-issued token looks pre-boot.
	app := newMiddlewareAppWithBoot(middlewareSecret, time.Now().Add(time.Second))

	req, _ := http.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("want 401 for pre-boot token, got %d", resp.StatusCode)
	}
}

func TestAdminOnly_AdminRole(t *testing.T) {
	app := newMiddlewareApp(middlewareSecret)
	tok := issueToken(t, middlewareSecret, "uid1", "admin")

	req, _ := http.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func contains(body []byte, sub string) bool {
	return len(body) > 0 && string(body) != "" && bodyContains(body, sub)
}

func bodyContains(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
