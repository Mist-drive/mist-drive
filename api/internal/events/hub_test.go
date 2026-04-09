package events

import (
	"testing"
	"time"
)

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
	h.mu.RLock()
	_, ok := h.subs["u"]
	h.mu.RUnlock()
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
