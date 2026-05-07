package httpx

import (
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/logger"
)

type ctxKey string

const (
	CtxUID  ctxKey = "uid"
	CtxRole ctxKey = "role"
)

func AuthMiddleware(secret string, bootTime time.Time, log *logger.Logger) fiber.Handler {
	warn := func(msg string, args ...any) {
		if log != nil {
			log.LogAttrs(slog.LevelWarn, msg, args...)
		}
	}
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
			switch {
			case errors.Is(err, jwt.ErrTokenSignatureInvalid):
				warn("auth: token with invalid signature", "ip", c.IP(), "ua", c.Get("User-Agent"), "path", c.Path())
			case errors.Is(err, jwt.ErrTokenUnverifiable):
				// ErrTokenUnverifiable wraps our "bad alg" keyfunc error — algorithm confusion attempt.
				warn("auth: token with bad algorithm", "ip", c.IP(), "ua", c.Get("User-Agent"), "path", c.Path())
			case errors.Is(err, jwt.ErrTokenMalformed):
				warn("auth: malformed token", "ip", c.IP(), "ua", c.Get("User-Agent"), "path", c.Path())
			// ErrTokenExpired is normal — user just needs to re-login, no warn.
			}
			return fiber.NewError(fiber.StatusUnauthorized, "invalid token")
		}
		// Reject tokens issued before this server instance started.
		// Forces re-login after a restart/redeployment.
		if claims.IssuedAt == nil || claims.IssuedAt.Time.Before(bootTime) {
			return fiber.NewError(fiber.StatusUnauthorized, "session expired, please log in again")
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
