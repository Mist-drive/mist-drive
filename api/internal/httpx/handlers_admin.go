package httpx

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/users"
)

const minPasswordLen = 8

// validLogin enforces a conservative login charset/length. The login is
// only an in-memory index key (user files on disk are named by UUID, not
// login), so the constraint is about sane input, not path safety. We
// allow email-style logins: letters, digits, '.', '-', '_', '@', '+'.
func validLogin(login string) bool {
	if len(login) < 3 || len(login) > 64 {
		return false
	}
	for _, r := range login {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '@' || r == '+':
		default:
			return false
		}
	}
	return true
}

type createUserReq struct {
	Login      string `json:"login"`
	Password   string `json:"password"`
	QuotaBytes int64  `json:"quotaBytes"`
	Email      string `json:"email,omitempty"`
}

type patchQuotaReq struct {
	QuotaBytes int64 `json:"quotaBytes"`
}

func (s *Server) adminListUsers(c *fiber.Ctx) error {
	all := s.Users.List()
	out := make([]users.PublicUser, 0, len(all))
	for _, u := range all {
		out = append(out, u.Public())
	}
	return c.JSON(out)
}

func (s *Server) adminCreateUser(c *fiber.Ctx) error {
	var r createUserReq
	if err := c.BodyParser(&r); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	r.Login = strings.TrimSpace(r.Login)
	if !validLogin(r.Login) {
		return fiber.NewError(fiber.StatusBadRequest, "login must be 3-64 chars: letters, digits, or . - _ @ +")
	}
	if len(r.Password) < minPasswordLen {
		return fiber.NewError(fiber.StatusBadRequest, "password must be at least 8 characters")
	}
	if r.Email != "" && !strings.Contains(r.Email, "@") {
		return fiber.NewError(fiber.StatusBadRequest, "invalid email")
	}
	if s.Users.EmailTaken(r.Email, "") {
		return fiber.NewError(fiber.StatusConflict, "email already in use")
	}
	if r.QuotaBytes <= 0 {
		r.QuotaBytes = s.Cfg.DefaultQuota
	}
	hash, err := auth.HashPassword(r.Password)
	if err != nil {
		return err
	}
	id := uuid.NewString()
	u := &users.User{
		ID: id, Login: r.Login, BcryptPwd: hash,
		QuotaBytes: r.QuotaBytes,
		Role:       users.RoleUser, CreatedAt: time.Now(),
		Email: r.Email,
	}
	if err := s.S3.EnsureBucket(c.Context(), u.Bucket()); err != nil {
		return s.serverError("admin: ensure bucket", err)
	}
	if err := s.Users.Create(u); err != nil {
		return fiber.NewError(fiber.StatusConflict, err.Error())
	}
	return c.JSON(u.Public())
}

func (s *Server) adminPatchQuota(c *fiber.Ctx) error {
	id := c.Params("id")
	u, err := s.Users.GetByID(id)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "not found")
	}
	var r patchQuotaReq
	if err := c.BodyParser(&r); err != nil || r.QuotaBytes <= 0 {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	u.QuotaBytes = r.QuotaBytes
	if err := s.Users.Update(u); err != nil {
		return s.serverError("admin: patch quota", err)
	}
	return c.JSON(u.Public())
}

func (s *Server) adminDeleteUser(c *fiber.Ctx) error {
	id := c.Params("id")
	u, err := s.Users.GetByID(id)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "not found")
	}
	if u.Role == users.RoleAdmin {
		return fiber.NewError(fiber.StatusForbidden, "cannot delete admin")
	}
	_ = s.S3.RemoveBucket(c.Context(), u.Bucket())
	if err := s.Users.Delete(id); err != nil {
		return s.serverError("admin: delete user", err)
	}
	return c.JSON(fiber.Map{"ok": true})
}
