package users

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

func NewStore(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "users")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, byID: map[string]*User{}, byLog: map[string]string{}}
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
// user's UsedBytes and persists the change. The read-modify-write
// happens under the store's write lock so concurrent completes or
// deletes can't race each other and clobber the total — a plain
// GetByID/Update pattern in a handler would, because GetByID returns a
// copy of the user taken before the lock is released.
//
// Clamps at zero on over-subtract (defensive: the reservation layer
// should already make UsedBytes monotonically consistent with reality,
// but we'd rather show 0 than a negative number if anything slips).
func (s *Store) AddUsedBytes(id string, delta int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	u.UsedBytes += delta
	if u.UsedBytes < 0 {
		u.UsedBytes = 0
	}
	return s.writeLocked(u)
}

func (s *Store) Update(u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[u.ID]; !ok {
		return ErrNotFound
	}
	if err := s.writeLocked(u); err != nil {
		return err
	}
	s.byID[u.ID] = u
	return nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	if err := os.Remove(s.path(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.byID, id)
	delete(s.byLog, u.Login)
	return nil
}

func (s *Store) GetByID(id string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u, ok := s.byID[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, ErrNotFound
}

func (s *Store) GetByLogin(login string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byLog[login]
	if !ok {
		return nil, ErrNotFound
	}
	u := s.byID[id]
	cp := *u
	return &cp, nil
}

func (s *Store) List() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.byID))
	for _, u := range s.byID {
		cp := *u
		out = append(out, &cp)
	}
	return out
}
