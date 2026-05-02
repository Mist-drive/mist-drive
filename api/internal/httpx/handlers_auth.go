package httpx

import (
	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/users"
)

type loginReq struct {
	Login         string `json:"login"`
	Password      string `json:"password"`
	ClientVersion string `json:"clientVersion,omitempty"`
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
	if s.Version != "dev" && r.ClientVersion != "" && r.ClientVersion != "dev" && r.ClientVersion != s.Version {
		return fiber.NewError(fiber.StatusBadRequest, "client outdated: please download version "+s.Version)
	}
	u, err := s.Users.GetByLogin(r.Login)
	if err != nil || !auth.VerifyPassword(u.BcryptPwd, r.Password) {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}
	tok, err := auth.Issue(s.Cfg.JWTSecret, u.ID, string(u.Role), s.Cfg.JWTTTL)
	if err != nil {
		return err
	}
	return c.JSON(loginResp{Token: tok, User: u.Public()})
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
