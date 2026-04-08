package httpx

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/auth"
)

type ctxKey string

const (
	CtxUID  ctxKey = "uid"
	CtxRole ctxKey = "role"
)

func AuthMiddleware(secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Prefer the Authorization header. Fall back to a `token` query
		// param so the browser can point `window.location` at streaming
		// download endpoints (which can't set custom headers). The token
		// is short-lived and only ever appears in the user's own URL bar.
		var tok string
		if h := c.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok = strings.TrimPrefix(h, "Bearer ")
		} else {
			tok = c.Query("token")
		}
		if tok == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "missing bearer token")
		}
		claims, err := auth.Parse(secret, tok)
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid token")
		}
		c.Locals(CtxUID, claims.UID)
		c.Locals(CtxRole, claims.Role)
		return c.Next()
	}
}

func AdminOnly(c *fiber.Ctx) error {
	if c.Locals(CtxRole) != "admin" {
		return fiber.NewError(fiber.StatusForbidden, "admin only")
	}
	return c.Next()
}

func UID(c *fiber.Ctx) string {
	v, _ := c.Locals(CtxUID).(string)
	return v
}
