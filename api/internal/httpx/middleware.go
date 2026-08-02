package httpx

import (
	"log/slog"
	"time"

	fiberauth "github.com/creativeyann17/go-fiber-auth"
	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/logger"
)

// Thin wrappers around go-fiber-auth so the rest of the package keeps
// its existing names. Security-warn logging that used to live inline in
// AuthMiddleware now flows through the lib's onReject callback.

func AuthMiddleware(secret string, bootTime time.Time, log *logger.Logger) fiber.Handler {
	return fiberauth.AuthMiddleware(secret, bootTime, func(reason string, c *fiber.Ctx) {
		if log != nil {
			log.LogAttrs(slog.LevelWarn, "auth: "+reason, "ip", c.IP(), "ua", c.Get("User-Agent"), "path", c.Path())
		}
	})
}

func AdminOnly(c *fiber.Ctx) error { return fiberauth.AdminOnly(c) }

func UID(c *fiber.Ctx) string { return fiberauth.UID(c) }

func tokenVer(c *fiber.Ctx) int64 { return fiberauth.Ver(c) }
