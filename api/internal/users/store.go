package users

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gofrs/flock"
)

var ErrNotFound = errors.New("user not found")
var ErrExists = errors.New("user already exists")

type Store struct {
	dir   string
	mu    sync.RWMutex
	byID  map[string]*User
	byLog map[string]string // login -> id

	// userLocksMu guards userLocks itself (map of map of mutexes is not
	// safe for concurrent map access otherwise). userLocks holds one
	// mutex per user id, used to serialize that user's own
	// read-modify-write-persist sequence end to end without blocking
	// every other user's requests on the same global lock — see
	// lockUser.
	userLocksMu sync.Mutex
	userLocks   map[string]*sync.Mutex
}

func NewStore(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "users")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, byID: map[string]*User{}, byLog: map[string]string{}, userLocks: map[string]*sync.Mutex{}}
	if err := s.loadAll(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadAll() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return err
		}
		var u User
		if err := json.Unmarshal(b, &u); err != nil {
			return fmt.Errorf("corrupt user file %s: %w", e.Name(), err)
		}
		s.byID[u.ID] = &u
		s.byLog[u.Login] = u.ID
	}
	return nil
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

// cloneUser returns a deep copy of u's slice fields on top of a struct
// copy. A plain `cp := *u` only copies slice headers — TOTPBackupCodes,
// TrustedDevices and LoginHistory would still share the canonical entry's
// backing array. Callers (handlers_totp.go, handlers_devices.go) mutate
// those slices in place (element removal via re-slicing), which would
// otherwise corrupt data visible to concurrent readers of the same user
// before Update() ever runs. Every read path (GetByID, GetByLogin, List)
// must go through this, not a bare struct copy.
func cloneUser(u *User) *User {
	cp := *u
	if u.TOTPBackupCodes != nil {
		cp.TOTPBackupCodes = append([]string(nil), u.TOTPBackupCodes...)
	}
	if u.TrustedDevices != nil {
		cp.TrustedDevices = append([]TrustedDevice(nil), u.TrustedDevices...)
	}
	if u.LoginHistory != nil {
		cp.LoginHistory = append([]LoginRecord(nil), u.LoginHistory...)
	}
	return &cp
}

// lockUser returns an unlock func that serializes all writes for one user
// id. Each user gets its own *sync.Mutex (created lazily), so two users'
// writes — including the disk flock+write+rename in writeLocked, which
// used to run under the single store-wide s.mu — proceed fully in
// parallel. A single user's operations remain strictly ordered end to
// end, which is what actually prevents lost updates; the global s.mu
// below is only ever held for brief in-memory map access, never I/O.
func (s *Store) lockUser(id string) func() {
	s.userLocksMu.Lock()
	l, ok := s.userLocks[id]
	if !ok {
		l = &sync.Mutex{}
		s.userLocks[id] = l
	}
	s.userLocksMu.Unlock()
	l.Lock()
	return l.Unlock
}

func (s *Store) writeLocked(u *User) error {
	p := s.path(u.ID)
	lk := flock.New(p + ".lock")
	if err := lk.Lock(); err != nil {
		return err
	}
	defer lk.Unlock()
	tmp := p + ".tmp"
	b, _ := json.MarshalIndent(u, "", "  ")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Create is not on the per-user-lock fast path: lockUser is keyed by ID,
// but uniqueness here is by Login, which doesn't exist as a key yet for a
// brand-new user. Two concurrent signups with different IDs but the same
// desired login would otherwise both pass the existence check under
// separate per-user locks. Account creation is rare (admin-driven, not a
// hot path), so it stays under the single store-wide lock for its whole
// body — correctness over parallelism here.
func (s *Store) Create(u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byLog[u.Login]; ok {
		return ErrExists
	}
	if err := s.writeLocked(u); err != nil {
		return err
	}
	s.byID[u.ID] = u
	s.byLog[u.Login] = u.ID
	return nil
}

// AddUsedBytes atomically adds delta (which may be negative) to the
// user's UsedBytes and persists the change. The whole read-modify-write
// runs under that one user's lock (lockUser) so concurrent completes or
// deletes for the SAME user can't race each other and clobber the total —
// a plain GetByID/Update pattern in a handler would, because GetByID
// returns a copy of the user taken before the lock is released. Other
// users' writes are unaffected — they hold a different per-user lock —
// so this never blocks on unrelated users' disk I/O.
//
// Clamps at zero on over-subtract (defensive: the reservation layer
// should already make UsedBytes monotonically consistent with reality,
// but we'd rather show 0 than a negative number if anything slips).
func (s *Store) AddUsedBytes(id string, delta int64) error {
	unlock := s.lockUser(id)
	defer unlock()

	s.mu.RLock()
	orig, ok := s.byID[id]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}

	u := cloneUser(orig)
	u.UsedBytes += delta
	if u.UsedBytes < 0 {
		u.UsedBytes = 0
	}
	if err := s.writeLocked(u); err != nil {
		return err
	}

	s.mu.Lock()
	s.byID[id] = u
	s.mu.Unlock()
	return nil
}

// SetUsedBytes overwrites the user's UsedBytes with an authoritative
// value (e.g. recomputed from a full S3 listing). Same locking rules as
// AddUsedBytes — must not race concurrent completes/deletes for the same
// user, must not block other users' writes.
func (s *Store) SetUsedBytes(id string, v int64) error {
	unlock := s.lockUser(id)
	defer unlock()

	s.mu.RLock()
	orig, ok := s.byID[id]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}

	if v < 0 {
		v = 0
	}
	u := cloneUser(orig)
	u.UsedBytes = v
	if err := s.writeLocked(u); err != nil {
		return err
	}

	s.mu.Lock()
	s.byID[id] = u
	s.mu.Unlock()
	return nil
}

// Update persists the given user record, replacing the in-memory copy.
// Callers fetch via GetByID, mutate a field, and call Update — but that
// copy may be stale by the time it lands here. UsedBytes has dedicated
// atomic accessors (AddUsedBytes/SetUsedBytes) precisely because it's
// updated from concurrent, independent flows (uploads completing,
// deletes, recounts) that race ordinary Get-then-Update callers (e.g.
// device revoke, TOTP enable). No Update caller legitimately sets
// UsedBytes, so we always keep the live value rather than the caller's
// possibly-stale snapshot — otherwise a concurrent upload's accounting
// gets silently overwritten. The whole sequence runs under this user's
// lock so it can't interleave with that user's own AddUsedBytes/
// SetUsedBytes calls; other users' I/O is unaffected.
func (s *Store) Update(u *User) error {
	unlock := s.lockUser(u.ID)
	defer unlock()

	s.mu.RLock()
	current, ok := s.byID[u.ID]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}

	u.UsedBytes = current.UsedBytes
	if err := s.writeLocked(u); err != nil {
		return err
	}

	s.mu.Lock()
	s.byID[u.ID] = u
	s.mu.Unlock()
	return nil
}

func (s *Store) Delete(id string) error {
	unlock := s.lockUser(id)
	defer unlock()

	s.mu.RLock()
	u, ok := s.byID[id]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}

	if err := os.Remove(s.path(id)); err != nil && !os.IsNotExist(err) {
		return err
	}

	s.mu.Lock()
	delete(s.byID, id)
	delete(s.byLog, u.Login)
	s.mu.Unlock()
	return nil
}

func (s *Store) GetByID(id string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u, ok := s.byID[id]; ok {
		return cloneUser(u), nil
	}
	return nil, ErrNotFound
}

// EmailTaken reports whether email is already used by a user other than
// exceptID (pass "" when creating a new user). Comparison is
// case-insensitive; an empty email is never considered taken. Email is
// only an index in memory — there's no DB — so we scan; the user set is
// small.
func (s *Store) EmailTaken(email, exceptID string) bool {
	if email == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, u := range s.byID {
		if id == exceptID {
			continue
		}
		if strings.EqualFold(u.Email, email) {
			return true
		}
	}
	return false
}

func (s *Store) GetByLogin(login string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byLog[login]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneUser(s.byID[id]), nil
}

func (s *Store) List() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.byID))
	for _, u := range s.byID {
		out = append(out, cloneUser(u))
	}
	return out
}
