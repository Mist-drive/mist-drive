package httpx

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gofiber/fiber/v2"
	"github.com/pquerna/otp/totp"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/users"
	"golang.org/x/crypto/bcrypt"
)

const totpIssuer = "Mist Drive"
const backupCodeCount = 8

// verifyTOTP checks a code against the user's TOTP secret or backup codes.
// Returns (valid, backupConsumed). When backupConsumed is true the caller must
// persist u so the used code is removed from the stored slice.
func verifyTOTP(u *users.User, code string) (ok bool, backupConsumed bool) {
	if totp.Validate(code, u.TOTPSecret) {
		return true, false
	}
	for i, hashed := range u.TOTPBackupCodes {
		if bcrypt.CompareHashAndPassword([]byte(hashed), []byte(code)) == nil {
			u.TOTPBackupCodes = append(u.TOTPBackupCodes[:i], u.TOTPBackupCodes[i+1:]...)
			return true, true
		}
	}
	return false, false
}

func generateBackupCodes() (plain []string, hashed []string, err error) {
	for range backupCodeCount {
		b := make([]byte, 5)
		if _, err = rand.Read(b); err != nil {
			return
		}
		code := hex.EncodeToString(b) // 10 hex chars
		h, e := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
		if e != nil {
			err = e
			return
		}
		plain = append(plain, code)
		hashed = append(hashed, string(h))
	}
	return
}

// GET /api/totp/setup — generate a new secret + QR URI without saving.
func (s *Server) totpSetup(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: u.Login,
	})
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{
		"secret": key.Secret(),
		"uri":    key.URL(),
	})
}

type totpEnableReq struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

// POST /api/totp/enable — verify code works for secret, then save + return backup codes.
func (s *Server) totpEnable(c *fiber.Ctx) error {
	var r totpEnableReq
	if err := c.BodyParser(&r); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	if r.Secret == "" || r.Code == "" {
		return fiber.NewError(fiber.StatusBadRequest, "secret and code required")
	}
	if !totp.Validate(r.Code, r.Secret) {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
	}
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	plain, hashed, err := generateBackupCodes()
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
	if !auth.VerifyPassword(u.BcryptPwd, r.Password) {
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
	if !totp.Validate(r.Code, u.TOTPSecret) {
		s.secWarn("totp: invalid code on regen-backup", "ip", c.IP(), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
	}
	plain, hashed, err := generateBackupCodes()
	if err != nil {
		return err
	}
	u.TOTPBackupCodes = hashed
	if err := s.Users.Update(u); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"backupCodes": plain})
}
