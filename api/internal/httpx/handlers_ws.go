package httpx

import (
	"time"

	"github.com/gofiber/websocket/v2"
	"github.com/yann/mist-drive/api/internal/auth"
)

// wsAuthTimeout bounds how long we wait for the client's first-message
// auth frame before dropping the connection.
const wsAuthTimeout = 10 * time.Second

// wsHandler holds a websocket open for the caller's user id and pushes
// every event the hub publishes for them. The handshake carries no
// credentials (the JWT is neither a header nor a query param on a ws
// upgrade), so the client must authenticate by sending
// {"type":"auth","token":"<jwt>"} as its first frame. Only after that
// validates do we subscribe it to the hub.
func (s *Server) wsHandler(c *websocket.Conn) {
	uid, ok := s.authenticateWS(c)
	if !ok {
		return
	}
	ch, unsub := s.Events.Subscribe(uid)
	defer unsub()

	// Drain inbound frames in a goroutine so client-side closes unblock
	// the write loop. After auth this is a one-way push channel; we don't
	// care what the client sends.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if err := c.WriteJSON(e); err != nil {
				return
			}
		}
	}
}

// authenticateWS reads and validates the first-message auth frame. It
// mirrors AuthMiddleware: signature + algorithm (via auth.Parse), the
// boot-time issued-at check, and the token-version revocation check.
// Returns the authenticated uid, or ok=false (caller closes the socket).
func (s *Server) authenticateWS(c *websocket.Conn) (uid string, ok bool) {
	_ = c.SetReadDeadline(time.Now().Add(wsAuthTimeout))
	var msg struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := c.ReadJSON(&msg); err != nil || msg.Type != "auth" || msg.Token == "" {
		return "", false
	}
	claims, err := auth.Parse(s.Cfg.JWTSecret, msg.Token)
	if err != nil {
		return "", false
	}
	if claims.IssuedAt == nil || claims.IssuedAt.Time.Before(s.bootTime) {
		return "", false
	}
	u, err := s.Users.GetByID(claims.UID)
	if err != nil || claims.Ver < u.TokenVersion {
		return "", false
	}
	// Authenticated — clear the deadline; the push channel is long-lived.
	_ = c.SetReadDeadline(time.Time{})
	return claims.UID, true
}
