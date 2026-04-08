// Package quota tracks in-flight upload reservations so that parallel
// uploads cannot collectively exceed a user's quota. It lives in memory
// only — on API restart, pending uploads are aborted by the GC in main,
// so losing the reservation map is safe.
package quota

import "sync"

type Reservations struct {
	mu  sync.Mutex
	byU map[string]int64 // userID -> reserved bytes
}

func New() *Reservations { return &Reservations{byU: map[string]int64{}} }

// TryReserve atomically checks `used + reserved + size <= quota` and
// if so adds `size` to the user's reservation. Returns true on success.
func (r *Reservations) TryReserve(userID string, size, used, quota int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.byU[userID]
	if used+cur+size > quota {
		return false
	}
	r.byU[userID] = cur + size
	return true
}

// Release subtracts `size` from the user's reservation. Safe on over-release.
func (r *Reservations) Release(userID string, size int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.byU[userID] - size
	if cur < 0 {
		cur = 0
	}
	r.byU[userID] = cur
}

// Get returns the current reservation for the user.
func (r *Reservations) Get(userID string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byU[userID]
}
