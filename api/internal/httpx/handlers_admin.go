package httpx

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/users"
)

type createUserReq struct {
	Login      string `json:"login"`
	Password   string `json:"password"`
	QuotaBytes int64  `json:"quotaBytes"`
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
	if err := c.BodyParser(&r); err != nil || r.Login == "" || r.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
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
	}
	if err := s.S3.EnsureBucket(c.Context(), u.Bucket()); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
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
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
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
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}
