package quota

import (
	"sync"
	"testing"
)

func TestTryReserve_HappyPath(t *testing.T) {
	r := New()
	if !r.TryReserve("u", 100, 0, 500) {
		t.Fatal("first reserve should succeed")
	}
	if !r.TryReserve("u", 300, 0, 500) {
		t.Fatal("cumulative 400 <= 500 should succeed")
	}
	if r.TryReserve("u", 200, 0, 500) {
		t.Fatal("cumulative 600 > 500 must be rejected")
	}
	if got := r.Get("u"); got != 400 {
		t.Fatalf("Get=%d want 400", got)
	}
}

func TestTryReserve_AccountsForUsedBytes(t *testing.T) {
	r := New()
	// quota 500, already used 400 -> only 100 free
	if !r.TryReserve("u", 100, 400, 500) {
		t.Fatal("100 into 100 free should succeed")
	}
	if r.TryReserve("u", 1, 400, 500) {
		t.Fatal("any extra should be rejected")
	}
}

func TestRelease_ClampsToZero(t *testing.T) {
	r := New()
	_ = r.TryReserve("u", 100, 0, 500)
	r.Release("u", 9999)
	if r.Get("u") != 0 {
		t.Fatalf("over-release should clamp to 0, got %d", r.Get("u"))
	}
}

func TestTryReserve_Concurrent(t *testing.T) {
	r := New()
	const (
		quota   = 1000
		size    = 100
		workers = 50
	)
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for range workers {
		wg.Go(func() {
			if r.TryReserve("u", size, 0, quota) {
				mu.Lock()
				success++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if success != quota/size {
		t.Fatalf("want exactly %d successes, got %d", quota/size, success)
	}
	if r.Get("u") != quota {
		t.Fatalf("reserved=%d want %d", r.Get("u"), quota)
	}
}

func TestTryReserve_ExactLimit(t *testing.T) {
	r := New()
	if !r.TryReserve("u", 100, 0, 100) {
		t.Fatal("reserve of exactly quota must succeed")
	}
	if r.TryReserve("u", 1, 0, 100) {
		t.Fatal("reserve beyond quota must fail")
	}
}

func TestTryReserve_MultiUserIndependent(t *testing.T) {
	r := New()
	if !r.TryReserve("userA", 500, 0, 500) {
		t.Fatal("userA should reach limit")
	}
	if r.TryReserve("userA", 1, 0, 500) {
		t.Fatal("userA at limit must be rejected")
	}
	if !r.TryReserve("userB", 500, 0, 500) {
		t.Fatal("userB should not be affected by userA's limit")
	}
}

func TestRelease_BelowZeroClamps(t *testing.T) {
	r := New()
	_ = r.TryReserve("u", 50, 0, 500)
	r.Release("u", 9999)
	got := r.Get("u")
	if got != 0 {
		t.Fatalf("over-release should clamp to 0, got %d", got)
	}
}
