package httpx

import (
	"testing"
	"time"
)

func TestThrottle_LocksAtThreshold(t *testing.T) {
	tr := newLoginThrottle()
	const max = 3
	for i := range max - 1 {
		tr.fail("k", max)
		if locked, _ := tr.locked("k"); locked {
			t.Fatalf("locked too early at attempt %d", i+1)
		}
	}
	tr.fail("k", max) // hits threshold
	locked, retry := tr.locked("k")
	if !locked {
		t.Fatal("want locked after reaching threshold")
	}
	if retry <= 0 {
		t.Fatalf("want positive retry-after, got %v", retry)
	}
}

func TestThrottle_ResetClears(t *testing.T) {
	tr := newLoginThrottle()
	tr.fail("k", 3)
	tr.fail("k", 3)
	tr.reset("k")
	if locked, _ := tr.locked("k"); locked {
		t.Fatal("reset should clear lock state")
	}
	// Counter restarts from zero: one more fail must not lock (threshold 3).
	tr.fail("k", 3)
	if locked, _ := tr.locked("k"); locked {
		t.Fatal("counter should have restarted after reset")
	}
}

func TestThrottle_KeysAreIndependent(t *testing.T) {
	tr := newLoginThrottle()
	for range 5 {
		tr.fail("login:alice", loginMaxFails)
	}
	if locked, _ := tr.locked("login:alice"); !locked {
		t.Fatal("alice should be locked")
	}
	if locked, _ := tr.locked("login:bob"); locked {
		t.Fatal("bob must not be affected by alice's failures")
	}
}

func TestThrottle_PruneDropsStaleEntries(t *testing.T) {
	tr := newLoginThrottle()
	tr.ttl = 10 * time.Millisecond
	tr.fail("k", 100) // single fail, never locked
	time.Sleep(20 * time.Millisecond)
	// Force a prune via a fail on a different key past the ttl window.
	tr.lastPrune = time.Now().Add(-time.Hour)
	tr.fail("other", 100)
	tr.mu.Lock()
	_, present := tr.byKey["k"]
	tr.mu.Unlock()
	if present {
		t.Fatal("stale entry should have been pruned")
	}
}
