package httpx

import (
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

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
	// Reject early if this login or source IP is locked out from too
	// many failures.
	if locked, retry := s.loginLocked(r.Login, clientIP(c)); locked {
		s.secWarn("auth: login locked out", "ip", clientIP(c), "login", r.Login, "ua", c.Get("User-Agent"), "retryAfter", retry.Round(time.Second).String())
		c.Set("Retry-After", strconv.Itoa(int(retry.Round(time.Second).Seconds())))
		return fiber.NewError(fiber.StatusTooManyRequests, "too many failed attempts, try again later")
	}
	u, err := s.Users.GetByLogin(r.Login)
	if err != nil {
		// Spend a bcrypt comparison on a dummy hash so unknown-user
		// responses take the same time as wrong-password ones — no
		// timing oracle for username enumeration.
		auth.DummyVerify(r.Password)
		s.loginFail(r.Login, clientIP(c))
		s.secWarn("auth: unknown user", "ip", clientIP(c), "login", r.Login, "ua", c.Get("User-Agent"))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}
	if !auth.VerifyPassword(u.BcryptPwd, r.Password) {
		s.secWarn("auth: wrong password", "ip", clientIP(c), "login", r.Login, "ua", c.Get("User-Agent"))
		count := s.loginFail(u.Login, clientIP(c))
		if s.Mailer != nil && s.Mailer.Enabled() && count%3 == 0 {
			if admin, err2 := s.Users.GetByLogin(s.Cfg.AdminLogin); err2 == nil && admin.Email != "" {
				ip, ua, login, to := clientIP(c), c.Get("User-Agent"), r.Login, admin.Email
				s.Log.LogAttrs(slog.LevelInfo, "email: sending failed-login alert", "login", login, "ip", ip, "count", count, "to", to)
				go func() {
					if err3 := s.Mailer.SendFailedLogin(to, login, ip, ua, int64(count), time.Now()); err3 != nil {
						s.secWarn("email: SendFailedLogin failed", "err", err3)
					} else {
						s.Log.LogAttrs(slog.LevelInfo, "email: failed-login alert sent", "login", login, "to", to)
					}
				}()
			}
		}
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}
	if u.TOTPEnabled {
		skipTOTP := false
		if cookie := c.Cookies(deviceCookieName); cookie != "" {
			if ok, _ := validateDeviceCookie(cookie, u.TrustedDevices); ok {
				skipTOTP = true
			} else {
				s.secWarn("auth: invalid device cookie", "ip", clientIP(c), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
			}
		}
		if !skipTOTP {
			if r.TOTPCode == "" {
				return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"totp_required": true})
			}
			ok, backupConsumed := verifyTOTP(u, r.TOTPCode)
			if !ok {
				s.loginFail(u.Login, clientIP(c))
				s.secWarn("auth: invalid TOTP code", "ip", clientIP(c), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
				return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
			}
			if backupConsumed {
				s.secWarn("auth: backup code consumed", "ip", clientIP(c), "uid", u.ID, "login", u.Login, "remaining", len(u.TOTPBackupCodes))
				_ = s.Users.Update(u) // persist code removal immediately — avoid replay if server crashes before final write
			}
			if r.RememberDevice {
				s.registerDevice(c, u) // registerDevice calls Update; login record added below
			}
		}
	}
	newIP := isNewIP(clientIP(c), u.LoginHistory)
	u.AppendLoginRecord(clientIP(c), c.Get("User-Agent"))
	_ = s.Users.Update(u)
	s.loginSucceeded(u.Login)
	if s.Mailer != nil && s.Mailer.Enabled() && newIP && u.Email != "" {
		ip, ua, uid, to := clientIP(c), c.Get("User-Agent"), u.ID, u.Email
		s.Log.LogAttrs(slog.LevelInfo, "email: sending new-IP notification", "uid", uid, "ip", ip, "to", to)
		go func() {
			if err := s.Mailer.SendNewIP(to, ip, ua, time.Now()); err != nil {
				s.secWarn("email: SendNewIP failed", "err", err)
			} else {
				s.Log.LogAttrs(slog.LevelInfo, "email: new-IP notification sent", "uid", uid, "to", to)
			}
		}()
	}

	tok, err := auth.Issue(s.Cfg.JWTSecret, u.ID, string(u.Role), u.TokenVersion, s.Cfg.JWTTTL)
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

// clientIP returns the best available client IP.
// In production clientIP(c) reads X-Forwarded-For (set by Traefik via ProxyHeader config).
// In local dev that header is absent, so we fall back to the raw TCP remote address.
func clientIP(c *fiber.Ctx) string {
	if ip := c.IP(); ip != "" {
		return ip
	}
	if addr := c.Context().RemoteAddr(); addr != nil {
		if host, _, err := net.SplitHostPort(addr.String()); err == nil && host != "" {
			return host
		}
	}
	return "unknown"
}

func isNewIP(ip string, history []users.LoginRecord) bool {
	if len(history) == 0 {
		return false
	}
	for _, r := range history {
		if r.IP == ip {
			return false
		}
	}
	return true
}

type updateEmailReq struct {
	Email string `json:"email"`
}

func (s *Server) updateEmail(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	var r updateEmailReq
	if err := c.BodyParser(&r); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	if r.Email != "" && !strings.Contains(r.Email, "@") {
		return fiber.NewError(fiber.StatusBadRequest, "invalid email")
	}
	if s.Users.EmailTaken(r.Email, u.ID) {
		return fiber.NewError(fiber.StatusConflict, "email already in use")
	}
	u.Email = r.Email
	if err := s.Users.Update(u); err != nil {
		return s.serverError("auth: update email", err)
	}
	return c.JSON(u.Public())
}

type changePasswordReq struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
	TOTPCode        string `json:"totpCode,omitempty"`
}

func (s *Server) changePassword(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	var r changePasswordReq
	if err := c.BodyParser(&r); err != nil || r.CurrentPassword == "" || r.NewPassword == "" {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	if !auth.VerifyPassword(u.BcryptPwd, r.CurrentPassword) {
		s.secWarn("change-password: wrong current password", "ip", clientIP(c), "uid", u.ID)
		return fiber.NewError(fiber.StatusUnauthorized, "wrong current password")
	}
	if u.TOTPEnabled {
		if r.TOTPCode == "" {
			return fiber.NewError(fiber.StatusBadRequest, "totp_required")
		}
		ok, backupConsumed := verifyTOTP(u, r.TOTPCode)
		if !ok {
			s.secWarn("change-password: invalid TOTP", "ip", clientIP(c), "uid", u.ID)
			return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
		}
		if backupConsumed {
			_ = s.Users.Update(u)
		}
	}
	hash, err := auth.HashPassword(r.NewPassword)
	if err != nil {
		return err
	}
	u.BcryptPwd = hash
	if err := s.Users.Update(u); err != nil {
		return s.serverError("auth: change password", err)
	}
	return c.JSON(fiber.Map{"ok": true})
}

type logoutAllReq struct {
	Password string `json:"password,omitempty"`
	TOTPCode string `json:"totpCode,omitempty"`
}

func (s *Server) logoutAll(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	var r logoutAllReq
	_ = c.BodyParser(&r)
	if u.TOTPEnabled {
		if r.TOTPCode == "" {
			return fiber.NewError(fiber.StatusBadRequest, "totp_required")
		}
		ok, backupConsumed := verifyTOTP(u, r.TOTPCode)
		if !ok {
			s.secWarn("logout-all: invalid TOTP", "ip", clientIP(c), "uid", u.ID)
			return fiber.NewError(fiber.StatusUnauthorized, "invalid TOTP code")
		}
		if backupConsumed {
			_ = s.Users.Update(u)
		}
	} else {
		if !auth.VerifyPassword(u.BcryptPwd, r.Password) {
			s.secWarn("logout-all: wrong password", "ip", clientIP(c), "uid", u.ID)
			return fiber.NewError(fiber.StatusUnauthorized, "wrong password")
		}
	}
	u.TokenVersion++
	if err := s.Users.Update(u); err != nil {
		return s.serverError("auth: logout-all", err)
	}
	return c.JSON(fiber.Map{"ok": true})
}
