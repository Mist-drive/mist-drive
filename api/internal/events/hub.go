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
//
// Publishes are coalesced per-user with a trailing debounce so a burst
// of uploads (e.g. 1000 small files) produces at most one fan-out per
// CoalesceWindow instead of one per mutation. Receivers re-list on any
// event, so dropping intermediate "please refresh" signals is safe and
// desirable — it prevents clients from hammering ListFiles while S3 is
// already busy serving the uploads themselves.
package events

import (
	"sync"
	"time"
)

type Type string

const (
	FilesChanged Type = "files-changed"
	RenameError  Type = "rename-error"
)

// CoalesceWindow is the trailing-debounce delay applied to Publish.
// Small enough to feel live, large enough to collapse a burst of
// small-file uploads into a single client refresh.
var CoalesceWindow = 750 * time.Millisecond

type Event struct {
	Type    Type   `json:"type"`
	Message string `json:"message,omitempty"`
	Path    string `json:"path,omitempty"`
}

type Hub struct {
	mu      sync.Mutex
	subs    map[string]map[chan Event]struct{} // userID → set of channels
	pending map[string]*pendingPublish         // userID → trailing-debounce timer
}

type pendingPublish struct {
	timer *time.Timer
	last  Event
}

func NewHub() *Hub {
	return &Hub{
		subs:    map[string]map[chan Event]struct{}{},
		pending: map[string]*pendingPublish{},
	}
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

// Publish schedules a trailing-debounced fan-out for userID. If a
// window is already pending, the event just updates the payload and
// the existing timer stands — so a burst of N calls within the window
// results in exactly one delivery per subscriber.
//
// When CoalesceWindow is 0 (tests), Publish delivers synchronously.
func (h *Hub) Publish(userID string, e Event) {
	if CoalesceWindow <= 0 {
		h.fanOut(userID, e)
		return
	}
	h.mu.Lock()
	if p, ok := h.pending[userID]; ok {
		p.last = e
		h.mu.Unlock()
		return
	}
	p := &pendingPublish{last: e}
	p.timer = time.AfterFunc(CoalesceWindow, func() {
		h.mu.Lock()
		ev := p.last
		delete(h.pending, userID)
		h.mu.Unlock()
		h.fanOut(userID, ev)
	})
	h.pending[userID] = p
	h.mu.Unlock()
}

// fanOut delivers e to every current subscriber of userID. Non-blocking:
// slow receivers get dropped events, not the whole hub.
func (h *Hub) fanOut(userID string, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
