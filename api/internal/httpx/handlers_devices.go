package httpx

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yann/mist-drive/api/internal/users"
)

const deviceCookieName = "mist_device"
const deviceTTL = 30 * 24 * time.Hour

func generateDeviceToken() (id, plain, hashed string, err error) {
	id = uuid.New().String()
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	plain = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(plain))
	hashed = hex.EncodeToString(h[:])
	return
}

func validateDeviceCookie(val string, devices []users.TrustedDevice) (valid bool, deviceID string) {
	parts := strings.SplitN(val, ":", 2)
	if len(parts) != 2 {
		return false, ""
	}
	id, plain := parts[0], parts[1]
	h := sha256.Sum256([]byte(plain))
	hashed := hex.EncodeToString(h[:])
	now := time.Now()
	for _, d := range devices {
		if d.ID == id && subtle.ConstantTimeCompare([]byte(d.HashedToken), []byte(hashed)) == 1 && now.Before(d.ExpiresAt) {
			return true, id
		}
	}
	return false, ""
}

func pruneExpiredDevices(devices []users.TrustedDevice) []users.TrustedDevice {
	now := time.Now()
	out := devices[:0]
	for _, d := range devices {
		if now.Before(d.ExpiresAt) {
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) registerDevice(c *fiber.Ctx, u *users.User) {
	id, plain, hashed, err := generateDeviceToken()
	if err != nil {
		return
	}
	u.TrustedDevices = pruneExpiredDevices(append([]users.TrustedDevice(nil), u.TrustedDevices...))
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
	c.Cookie(&fiber.Cookie{
		Name:     deviceCookieName,
		Value:    id + ":" + plain,
		MaxAge:   int(deviceTTL.Seconds()),
		HTTPOnly: true,
		SameSite: "Strict",
		Path:     "/",
	})
}

// expireDeviceCookie tells the browser to drop the trusted-device
// cookie immediately. Attributes must match the original so the browser
// targets the same cookie.
func expireDeviceCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     deviceCookieName,
		Value:    "",
		MaxAge:   -1,
		HTTPOnly: true,
		SameSite: "Strict",
		Path:     "/",
	})
}

// currentDeviceID returns the device id embedded in the caller's
// trusted-device cookie, or "" if absent/malformed.
func currentDeviceID(c *fiber.Ctx) string {
	cookie := c.Cookies(deviceCookieName)
	if cookie == "" {
		return ""
	}
	if parts := strings.SplitN(cookie, ":", 2); len(parts) == 2 {
		return parts[0]
	}
	return ""
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
	expireDeviceCookie(c)
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
	if currentDeviceID(c) == id {
		expireDeviceCookie(c)
	}
	return c.JSON(fiber.Map{"ok": true})
}
