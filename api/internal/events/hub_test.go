package events

import (
	"testing"
	"time"
)

func init() {
	// Deliver synchronously in tests so assertions don't race the
	// trailing-debounce timer.
	CoalesceWindow = 0
}

func TestHubPublishDelivers(t *testing.T) {
	h := NewHub()
	ch, unsub := h.Subscribe("u1")
	defer unsub()
	h.Publish("u1", Event{Type: FilesChanged})
	select {
	case e := <-ch:
		if e.Type != FilesChanged {
			t.Fatalf("got %v", e.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestHubIsolatesUsers(t *testing.T) {
	h := NewHub()
	a, unsubA := h.Subscribe("a")
	defer unsubA()
	b, unsubB := h.Subscribe("b")
	defer unsubB()
	h.Publish("a", Event{Type: FilesChanged})
	select {
	case <-a:
	case <-time.After(time.Second):
		t.Fatal("a did not receive")
	}
	select {
	case <-b:
		t.Fatal("b received foreign event")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubUnsubscribe(t *testing.T) {
	h := NewHub()
	_, unsub := h.Subscribe("u")
	unsub()
	h.mu.Lock()
	_, ok := h.subs["u"]
	h.mu.Unlock()
	if ok {
		t.Fatal("user entry not cleaned up")
	}
	// Publishing after unsubscribe must not panic.
	h.Publish("u", Event{Type: FilesChanged})
}

func TestHubOverflowDrops(t *testing.T) {
	h := NewHub()
	_, unsub := h.Subscribe("u")
	defer unsub()
	// Buffer is 4 — fire 100 and ensure no goroutine blocks.
	done := make(chan struct{})
	go func() {
		for range 100 {
			h.Publish("u", Event{Type: FilesChanged})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked on slow subscriber")
	}
}

func TestHubCoalescesBurst(t *testing.T) {
	// Flip coalescing back on for this test only.
	prev := CoalesceWindow
	CoalesceWindow = 50 * time.Millisecond
	defer func() { CoalesceWindow = prev }()

	h := NewHub()
	ch, unsub := h.Subscribe("u")
	defer unsub()

	// 50 publishes inside one window must collapse to a single delivery.
	for range 50 {
		h.Publish("u", Event{Type: FilesChanged})
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("coalesced event never delivered")
	}
	// Nothing else should land for at least another full window.
	select {
	case <-ch:
		t.Fatal("burst was not coalesced")
	case <-time.After(2 * CoalesceWindow):
	}
}
