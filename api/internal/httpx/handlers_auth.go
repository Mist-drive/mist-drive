package httpx

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/users"
)

type loginReq struct {
	Login          string `json:"login"`
	Password       string `json:"password"`
	ClientVersion  string `json:"clientVersion,omitempty"`
	TOTPCode       string `json:"totpCode,omitempty"`
	RememberDevice bool   `json:"rememberDevice,omitempty"`
}
type loginResp struct {
	Token string           `json:"token"`
	User  users.PublicUser `json:"user"`
}

func (s *Server) login(c *fiber.Ctx) error {
	var r loginReq
	if err := c.BodyParser(&r); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	clientVer := strings.TrimPrefix(r.ClientVersion, "v")
	serverVer := strings.TrimPrefix(s.Version, "v")
	if serverVer != "dev" && clientVer != "" && clientVer != "dev" && clientVer != serverVer {
		return fiber.NewError(fiber.StatusBadRequest, "client outdated: please download version "+s.Version)
	}
	u, err := s.Users.GetByLogin(r.Login)
	if err != nil {
		s.secWarn("auth: unknown user", "ip", c.IP(), "login", r.Login, "ua", c.Get("User-Agent"))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}
	if !auth.VerifyPassword(u.BcryptPwd, r.Password) {
		s.secWarn("auth: wrong password", "ip", c.IP(), "login", r.Login, "ua", c.Get("User-Agent"))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}
	if u.TOTPEnabled {
		skipTOTP := false
		if cookie := c.Cookies(deviceCookieName); cookie != "" {
			if ok, _ := validateDeviceCookie(cookie, u.TrustedDevices); ok {
				skipTOTP = true
			} else {
				s.secWarn("auth: invalid device cookie", "ip", c.IP(), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
			}
		}
		if !skipTOTP {
			if r.TOTPCode == "" {
				return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"totp_required": true})
			}
			ok, backupConsumed := verifyTOTP(u, r.TOTPCode)
			if !ok {
				s.secWarn("auth: invalid TOTP code", "ip", c.IP(), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
				return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
			}
			if backupConsumed {
				s.secWarn("auth: backup code consumed", "ip", c.IP(), "uid", u.ID, "login", u.Login, "remaining", len(u.TOTPBackupCodes))
				_ = s.Users.Update(u) // persist code removal immediately — avoid replay if server crashes before final write
			}
			if r.RememberDevice {
				s.registerDevice(c, u) // registerDevice calls Update; login record added below
			}
		}
	}
	u.AppendLoginRecord(c.IP(), c.Get("User-Agent"))
	_ = s.Users.Update(u)

	tok, err := auth.Issue(s.Cfg.JWTSecret, u.ID, string(u.Role), s.Cfg.JWTTTL)
	if err != nil {
		return err
	}
	return c.JSON(loginResp{Token: tok, User: u.Public()})
}

// GET /api/login-history
func (s *Server) loginHistory(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	if u.LoginHistory == nil {
		return c.JSON([]users.LoginRecord{})
	}
	return c.JSON(u.LoginHistory)
}

func (s *Server) me(c *fiber.Ctx) error {
	u, err := s.Users.GetByID(UID(c))
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	p := u.Public()
	p.ReservedBytes = s.Reservations.Get(u.ID)
	p.DiskFreeBytes = quota.DiskFree(s.Cfg.DataDir)
	return c.JSON(p)
}
