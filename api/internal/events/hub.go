// Package events is a tiny per-user fan-out hub for "something
// changed in your bucket" notifications. It exists so the web UI and
// desktop app can replace polling with a push model without us needing
// a real pub/sub system like NATS or Redis — the only consumers live
// in the same process, so an in-memory map of channels is plenty.
//
// Messages are intentionally tiny: Type is the only field. Receivers
// react by re-fetching authoritative state (listFiles + me). Sending
// full deltas would mean the hub has to stay perfectly in sync with
// the store, which is exactly the kind of complexity we don't need for
// a "refresh your view" signal.
package events

import "sync"

type Type string

const (
	FilesChanged Type = "files-changed"
)

type Event struct {
	Type Type `json:"type"`
}

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{} // userID → set of channels
}

func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan Event]struct{}{}}
}

// Subscribe returns a buffered channel that receives every event
// published for `userID`, plus an unsubscribe function the caller
// MUST invoke (typically via defer) when it disconnects. Buffer of 4
// absorbs bursty deletes without blocking the publisher; if it still
// overflows we drop — the receiver will re-list on any message anyway
// so missing one "please refresh" is harmless.
func (h *Hub) Subscribe(userID string) (<-chan Event, func()) {
	ch := make(chan Event, 4)
	h.mu.Lock()
	if _, ok := h.subs[userID]; !ok {
		h.subs[userID] = map[chan Event]struct{}{}
	}
	h.subs[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set, ok := h.subs[userID]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, userID)
			}
		}
		close(ch)
	}
}

// Publish fans an event out to every current subscriber of `userID`.
// Non-blocking: slow receivers get dropped events, not the whole hub.
func (h *Hub) Publish(userID string, e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[userID] {
		select {
		case ch <- e:
		default:
			// receiver is behind — drop. They'll refresh on the next
			// event they do receive (or on their 30s idle tick, for
			// the desktop sync engine).
		}
	}
}
