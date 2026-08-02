package httpx

import (
	"time"

	fiberauth "github.com/creativeyann17/go-fiber-auth"
	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/users"
)

const deviceCookieName = "mist_device"
const deviceTTL = 30 * 24 * time.Hour

func (s *Server) registerDevice(c *fiber.Ctx, u *users.User) {
	id, plain, hashed, err := fiberauth.NewTrustedDeviceToken()
	if err != nil {
		return
	}
	u.TrustedDevices = fiberauth.PruneExpiredDevices(append([]users.TrustedDevice(nil), u.TrustedDevices...))
	label := c.Get("User-Agent")
	if len(label) > 120 {
		label = label[:120]
	}
	u.TrustedDevices = append(u.TrustedDevices, users.TrustedDevice{
		ID:          id,
		HashedToken: hashed,
		Label:       label,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(deviceTTL),
	})
	_ = s.Users.Update(u)
	c.Cookie(fiberauth.DeviceCookie(deviceCookieName, id, plain, deviceTTL))
}

// GET /api/devices
func (s *Server) listDevices(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	return c.JSON(u.PublicDevices())
}

// DELETE /api/devices — revoke all
func (s *Server) revokeAllDevices(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	u.TrustedDevices = nil
	if err := s.Users.Update(u); err != nil {
		return err
	}
	// This browser's cookie is now dead too — clear it so it doesn't
	// linger for 30 days as a stale (always-rejected) credential.
	c.Cookie(fiberauth.ExpiredDeviceCookie(deviceCookieName))
	return c.JSON(fiber.Map{"ok": true})
}

// DELETE /api/devices/:id — revoke one
func (s *Server) revokeDevice(c *fiber.Ctx) error {
	id := c.Params("id")
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	filtered := u.TrustedDevices[:0]
	for _, d := range u.TrustedDevices {
		if d.ID != id {
			filtered = append(filtered, d)
		}
	}
	u.TrustedDevices = filtered
	if err := s.Users.Update(u); err != nil {
		return err
	}
	// If the caller just revoked the device they're sitting on, clear
	// its now-defunct cookie.
	if fiberauth.CookieDeviceID(c.Cookies(deviceCookieName)) == id {
		c.Cookie(fiberauth.ExpiredDeviceCookie(deviceCookieName))
	}
	return c.JSON(fiber.Map{"ok": true})
}
