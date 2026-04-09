package httpx

import (
	"github.com/gofiber/websocket/v2"
)

// wsHandler holds a websocket open for the caller's user id and pushes
// every event the hub publishes for them. Auth already ran via ?token=
// in the URL (browsers can't set Authorization headers on the
// handshake), so we just read the uid back out of fiber locals.
func (s *Server) wsHandler(c *websocket.Conn) {
	uid, _ := c.Locals("uid").(string)
	if uid == "" {
		return
	}
	ch, unsub := s.Events.Subscribe(uid)
	defer unsub()

	// Drain inbound frames in a goroutine so client-side closes unblock
	// the write loop. One-way push channel; we don't care what the
	// client sends.
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
