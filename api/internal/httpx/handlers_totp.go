package httpx

import (
	fiberauth "github.com/creativeyann17/go-fiber-auth"
	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/users"
)

const backupCodeCount = 8

// verifyTOTP checks a code against the user's TOTP secret or backup codes.
// Returns (valid, backupConsumed). When backupConsumed is true the caller must
// persist u so the used code is removed from the stored slice.
func verifyTOTP(u *users.User, code string) (ok bool, backupConsumed bool) {
	if fiberauth.ValidateTOTPCode(u.TOTPSecret, code) {
		return true, false
	}
	if i, ok := fiberauth.CheckBackupCode(u.TOTPBackupCodes, code); ok {
		u.TOTPBackupCodes = append(u.TOTPBackupCodes[:i], u.TOTPBackupCodes[i+1:]...)
		return true, true
	}
	return false, false
}

// GET /api/totp/setup — generate a new secret + QR URI without saving.
func (s *Server) totpSetup(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	issuer := "Mist Drive"
	if s.Version == "dev" {
		issuer += " (dev)"
	}
	secret, uri, err := fiberauth.GenerateTOTPSecret(fiberauth.TOTPConfig{Issuer: issuer}, u.Login)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{
		"secret": secret,
		"uri":    uri,
	})
}

type totpEnableReq struct {
	Secret   string `json:"secret"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

// POST /api/totp/enable — verify password + code works for secret, then save + return backup codes.
func (s *Server) totpEnable(c *fiber.Ctx) error {
	var r totpEnableReq
	if err := c.BodyParser(&r); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	if r.Secret == "" || r.Code == "" || r.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "password, secret and code required")
	}
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	// Re-authenticate with the password so a leaked/stolen session token
	// alone can't enrol an attacker-controlled 2FA secret (account lockout).
	if !fiberauth.VerifyPassword(u.BcryptPwd, r.Password) {
		s.secWarn("totp: wrong password on enable", "ip", clientIP(c), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid password")
	}
	if !fiberauth.ValidateTOTPCode(r.Secret, r.Code) {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
	}
	plain, hashed, err := fiberauth.GenerateBackupCodes(backupCodeCount)
	if err != nil {
		return err
	}
	u.TOTPSecret = r.Secret
	u.TOTPEnabled = true
	u.TOTPBackupCodes = hashed
	if err := s.Users.Update(u); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"backupCodes": plain})
}

type totpDisableReq struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// DELETE /api/totp/disable — requires password + TOTP code (or backup code).
func (s *Server) totpDisable(c *fiber.Ctx) error {
	var r totpDisableReq
	if err := c.BodyParser(&r); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	if !fiberauth.VerifyPassword(u.BcryptPwd, r.Password) {
		s.secWarn("totp: wrong password on disable", "ip", c.IP(), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid password")
	}
	ok, _ := verifyTOTP(u, r.Code)
	if !ok {
		s.secWarn("totp: invalid code on disable", "ip", c.IP(), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
	}
	u.TOTPSecret = ""
	u.TOTPEnabled = false
	u.TOTPBackupCodes = nil
	u.TrustedDevices = nil
	if err := s.Users.Update(u); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true})
}

type totpRegenReq struct {
	Code string `json:"code"`
}

// POST /api/totp/regen-backup — generate fresh backup codes (requires valid TOTP code).
func (s *Server) totpRegenBackup(c *fiber.Ctx) error {
	var r totpRegenReq
	if err := c.BodyParser(&r); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	if !u.TOTPEnabled {
		return fiber.NewError(fiber.StatusBadRequest, "TOTP not enabled")
	}
	if !fiberauth.ValidateTOTPCode(u.TOTPSecret, r.Code) {
		s.secWarn("totp: invalid code on regen-backup", "ip", c.IP(), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
	}
	plain, hashed, err := fiberauth.GenerateBackupCodes(backupCodeCount)
	if err != nil {
		return err
	}
	u.TOTPBackupCodes = hashed
	if err := s.Users.Update(u); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"backupCodes": plain})
}
