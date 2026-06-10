package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// downloadTickets mints short-lived, single-use tickets that authorize
// one streaming zip download. They exist so the browser can trigger a
// zip download via plain navigation (which can't set an Authorization
// header) WITHOUT putting the reusable session JWT in the URL — the JWT
// would otherwise land in proxy/access logs.
//
// A ticket is an opaque random string bound to {userID, prefix}. It is
// consumed (deleted) on first use and expires after ttl. In-memory, no
// DB — same shape as the login throttle / quota reservations.
type downloadTickets struct {
	mu        sync.Mutex
	byTok     map[string]dlTicket
	ttl       time.Duration
	lastPrune time.Time
}

type dlTicket struct {
	uid     string
	prefix  string
	expires time.Time
}

func newDownloadTickets() *downloadTickets {
	return &downloadTickets{byTok: make(map[string]dlTicket), ttl: 60 * time.Second}
}

// issue mints a ticket bound to uid+prefix. The window is short because
// the client navigates to the stream URL immediately after minting.
func (d *downloadTickets) issue(uid, prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneLocked(now)
	d.byTok[tok] = dlTicket{uid: uid, prefix: prefix, expires: now.Add(d.ttl)}
	return tok, nil
}

// consume validates a ticket and returns its bound identity. The ticket
// is deleted unconditionally on lookup (single-use), so a replay — even
// within the TTL — fails.
func (d *downloadTickets) consume(tok string) (uid, prefix string, ok bool) {
	if tok == "" {
		return "", "", false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t, found := d.byTok[tok]
	if !found {
		return "", "", false
	}
	delete(d.byTok, tok)
	if time.Now().After(t.expires) {
		return "", "", false
	}
	return t.uid, t.prefix, true
}

// pruneLocked drops expired tickets. Caller holds the lock. Runs at most
// once per ttl so the common path stays cheap.
func (d *downloadTickets) pruneLocked(now time.Time) {
	if now.Sub(d.lastPrune) < d.ttl {
		return
	}
	d.lastPrune = now
	for k, t := range d.byTok {
		if now.After(t.expires) {
			delete(d.byTok, k)
		}
	}
}
