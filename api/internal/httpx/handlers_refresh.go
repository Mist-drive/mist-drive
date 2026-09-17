package httpx

import (
	"errors"
	"slices"
	"time"

	fiberauth "github.com/creativeyann17/go-fiber-auth"
	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/sessions"
	"github.com/yann/mist-drive/api/internal/users"
)

const (
	refreshCookieName = "mist_rt"
	// Covers /auth/refresh and /auth/logout, never rides /api calls.
	refreshCookiePath = "/auth"
	// Concurrent tabs refreshing at once must not trip reuse detection.
	refreshGrace = 10 * time.Second
	// maxRefreshSessions caps live sessions per user; login evicts the least recently used.
	maxRefreshSessions = 10
)

// refreshEnabled: opt-in per client (login body "refresh": true) and
// server-wide via REFRESH_TTL>0. Desktop never asks, keeps JWT_TTL tokens.
func (s *Server) refreshEnabled(requested bool) bool {
	return requested && s.Sessions != nil && s.Cfg.RefreshTTL > 0
}

// startRefreshSession persists a new refresh session for u, sets its
// cookie and returns its id (lets the client flag "this session").
func (s *Server) startRefreshSession(c *fiber.Ctx, u *users.User) (string, error) {
	s.Sessions.PruneExpired()
	if existing, err := s.Sessions.ListByUID(u.ID); err == nil {
		for _, id := range fiberauth.RefreshSessionsToEvict(existing, maxRefreshSessions) {
			_ = s.Sessions.Delete(id)
		}
	}
	sess, plain, err := fiberauth.NewRefreshSession(u.ID, u.TokenVersion, c.Get("User-Agent"), s.Cfg.RefreshTTL)
	if err != nil {
		return "", s.serverError("auth: new refresh session", err)
	}
	if err := s.Sessions.Create(sess); err != nil {
		return "", s.serverError("auth: store refresh session", err)
	}
	c.Cookie(fiberauth.RefreshCookie(refreshCookieName, refreshCookiePath, sess.ID, plain, s.Cfg.RefreshTTL))
	return sess.ID, nil
}

// revokeOtherSessions bumps TokenVersion, killing every access token and
// refresh session of u, persists u, then hands the caller a fresh token
// (plus a refresh session when asked) so only it stays logged in.
func (s *Server) revokeOtherSessions(c *fiber.Ctx, u *users.User, refresh bool) (fiber.Map, error) {
	u.TokenVersion++
	if err := s.Users.Update(u); err != nil {
		return nil, s.serverError("auth: bump token version", err)
	}
	ttl, sid := s.Cfg.JWTTTL, ""
	if s.Sessions != nil {
		// Ver check already rejects them, this just drops the dead rows.
		_ = s.Sessions.DeleteByUID(u.ID)
		if s.refreshEnabled(refresh) {
			var err error
			if sid, err = s.startRefreshSession(c, u); err != nil {
				return nil, err
			}
			ttl = s.Cfg.AccessTTL
		}
	}
	tok, err := fiberauth.Issue(s.Cfg.JWTSecret, u.ID, []string{string(u.Role)}, u.TokenVersion, ttl)
	if err != nil {
		return nil, s.serverError("auth: issue access token", err)
	}
	return fiber.Map{"token": tok, "sessionId": sid}, nil
}

// POST /auth/refresh: exchanges the refresh cookie for a fresh access token.
func (s *Server) refresh(c *fiber.Ctx) error {
	if s.Sessions == nil {
		return fiber.NewError(fiber.StatusNotFound, "refresh disabled")
	}
	id, plain, ok := fiberauth.SplitRefreshCookie(c.Cookies(refreshCookieName))
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "no refresh session")
	}
	reject := func() error {
		_ = s.Sessions.Delete(id)
		c.Cookie(fiberauth.ExpiredRefreshCookie(refreshCookieName, refreshCookiePath))
		return fiber.NewError(fiber.StatusUnauthorized, "session expired, please log in again")
	}
	sess, err := s.Sessions.Get(id)
	if err != nil {
		return reject()
	}
	u, err := s.Users.GetByID(sess.UID)
	if err != nil {
		return reject()
	}
	switch fiberauth.CheckRefresh(sess, plain, u.TokenVersion, refreshGrace) {
	case fiberauth.RefreshOK:
		next, newPlain, err := fiberauth.RotateRefreshSession(sess)
		if err != nil {
			return s.serverError("auth: rotate refresh session", err)
		}
		switch err := s.Sessions.Swap(next, sess.HashedToken); {
		case err == nil:
			c.Cookie(fiberauth.RefreshCookie(refreshCookieName, refreshCookiePath, next.ID, newPlain, time.Until(next.ExpiresAt)))
		case errors.Is(err, sessions.ErrRotated):
			// Lost the race to another tab: same as grace, no new cookie.
		default:
			return s.serverError("auth: persist refresh rotation", err)
		}
	case fiberauth.RefreshGrace:
	case fiberauth.RefreshReuse:
		s.secWarn("auth: refresh token reuse, session revoked", "ip", clientIP(c), "uid", u.ID, "login", u.Login, "ua", c.Get("User-Agent"))
		return reject()
	default: // RefreshExpired, RefreshRevoked
		return reject()
	}
	tok, err := fiberauth.Issue(s.Cfg.JWTSecret, u.ID, []string{string(u.Role)}, u.TokenVersion, s.Cfg.AccessTTL)
	if err != nil {
		return s.serverError("auth: issue access token", err)
	}
	return c.JSON(loginResp{Token: tok, User: u.Public(), SessionID: sess.ID})
}

type publicSession struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// GET /api/sessions: the caller's live refresh sessions, most recent first.
func (s *Server) listSessions(c *fiber.Ctx) error {
	out := []publicSession{}
	if s.Sessions == nil {
		return c.JSON(out)
	}
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	list, err := s.Sessions.ListByUID(u.ID)
	if err != nil {
		return s.serverError("sessions: list", err)
	}
	now := time.Now()
	for _, sess := range list {
		// Revoked (older TokenVersion) sessions are dead even if not yet deleted.
		if now.Before(sess.ExpiresAt) && sess.Ver >= u.TokenVersion {
			out = append(out, publicSession{sess.ID, sess.Label, sess.CreatedAt, sess.RotatedAt, sess.ExpiresAt})
		}
	}
	slices.SortFunc(out, func(a, b publicSession) int { return b.LastUsedAt.Compare(a.LastUsedAt) })
	return c.JSON(out)
}

// DELETE /api/sessions/:id: revoke one of the caller's sessions.
func (s *Server) revokeSession(c *fiber.Ctx) error {
	if s.Sessions == nil {
		return fiber.NewError(fiber.StatusNotFound, "session not found")
	}
	sess, err := s.Sessions.Get(c.Params("id"))
	// Same 404 for someone else's session: don't confirm it exists.
	if err != nil || sess.UID != UID(c) {
		return fiber.NewError(fiber.StatusNotFound, "session not found")
	}
	if err := s.Sessions.Delete(sess.ID); err != nil {
		return s.serverError("sessions: revoke", err)
	}
	return c.JSON(fiber.Map{"ok": true})
}

// POST /auth/logout: drops this browser's refresh session. Always 200.
func (s *Server) logout(c *fiber.Ctx) error {
	if id, _, ok := fiberauth.SplitRefreshCookie(c.Cookies(refreshCookieName)); ok && s.Sessions != nil {
		_ = s.Sessions.Delete(id)
	}
	c.Cookie(fiberauth.ExpiredRefreshCookie(refreshCookieName, refreshCookiePath))
	return c.JSON(fiber.Map{"ok": true})
}
