package httpx

import (
	"sync"
	"time"
)

// loginThrottle is an in-memory lockout for authentication attempts.
// It tracks two independent dimensions, each its own namespaced key:
//
//   - per-login ("login:<name>") — blunts a targeted brute-force against
//     one account. Includes unknown logins, so hammering a guessed
//     username is throttled too.
//   - per-IP ("ip:<addr>") — blunts a password-spray that walks many
//     usernames from one source. Higher threshold since several real
//     users may legitimately share an IP (NAT/office).
//
// A login is locked if *either* dimension is tripped. Both wrong-password
// and wrong-TOTP attempts count as a failure.
//
// No database: the map lives for the process lifetime. Stale entries are
// swept lazily so the map stays bounded even under a spray of random
// logins/IPs — closing the unbounded-growth hole.
type loginThrottle struct {
	mu        sync.Mutex
	byKey     map[string]*attempt
	lockFor   time.Duration // lockout duration once tripped
	ttl       time.Duration // idle entries older than this are pruned
	lastPrune time.Time
}

type attempt struct {
	fails     int
	lockUntil time.Time
	seen      time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{
		byKey:   make(map[string]*attempt),
		lockFor: 15 * time.Minute,
		ttl:     1 * time.Hour,
	}
}

const (
	loginMaxFails = 5  // per-account lockout threshold
	ipMaxFails    = 20 // per-IP lockout threshold (higher: shared NATs)
)

// usableIP reports whether ip is concrete enough to throttle on. The
// clientIP helper returns "unknown" when no address is resolvable; we
// don't want every such caller sharing one bucket.
func usableIP(ip string) bool { return ip != "" && ip != "unknown" }

// loginLocked reports whether either the login or the source IP is
// currently locked out, and the longest retry-after of the two.
func (s *Server) loginLocked(login, ip string) (bool, time.Duration) {
	g := s.loginGuard()
	locked, retry := g.locked("login:" + login)
	if usableIP(ip) {
		if l, d := g.locked("ip:" + ip); l {
			locked = true
			if d > retry {
				retry = d
			}
		}
	}
	return locked, retry
}

// loginFail records a failure against both dimensions and returns the
// running per-login count (used to pace the failed-login admin email).
func (s *Server) loginFail(login, ip string) int {
	g := s.loginGuard()
	count := g.fail("login:"+login, loginMaxFails)
	if usableIP(ip) {
		g.fail("ip:"+ip, ipMaxFails)
	}
	return count
}

// loginSucceeded clears the per-login counter on a successful auth. The
// per-IP counter is intentionally left to age out via TTL so one valid
// credential among a spray can't immediately reset the IP's budget.
func (s *Server) loginSucceeded(login string) {
	s.loginGuard().reset("login:" + login)
}

// locked reports whether key is currently locked out and, if so, how
// long until it may try again.
func (t *loginThrottle) locked(key string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.byKey[key]
	if a == nil {
		return false, 0
	}
	if d := time.Until(a.lockUntil); d > 0 {
		return true, d
	}
	return false, 0
}

// fail records a failed attempt for key and returns the running count.
// Once the count reaches maxFails the key is locked for lockFor.
func (t *loginThrottle) fail(key string, maxFails int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.pruneLocked(now)
	a := t.byKey[key]
	if a == nil {
		a = &attempt{}
		t.byKey[key] = a
	}
	a.fails++
	a.seen = now
	if a.fails >= maxFails {
		a.lockUntil = now.Add(t.lockFor)
	}
	return a.fails
}

// reset clears any failure state for key (called on successful login).
func (t *loginThrottle) reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byKey, key)
}

// pruneLocked drops entries idle longer than ttl. Caller holds the lock.
// Runs at most once per ttl to keep the common path cheap.
func (t *loginThrottle) pruneLocked(now time.Time) {
	if now.Sub(t.lastPrune) < t.ttl {
		return
	}
	t.lastPrune = now
	for k, a := range t.byKey {
		if now.Sub(a.seen) > t.ttl && now.After(a.lockUntil) {
			delete(t.byKey, k)
		}
	}
}
