package users

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/creativeyann17/go-docstore"
)

// Error identities are shared with docstore so existing errors.Is
// checks across handlers keep working unchanged.
var ErrNotFound = docstore.ErrNotFound
var ErrExists = docstore.ErrExists

// Store persists users as JSON documents in SQLite (see github.com/creativeyann17/go-docstore).
// There is no in-memory cache anymore: SQLite's page cache serves hot
// reads, and every read unmarshals a fresh copy — the old cloneUser
// deep-copy dance is unnecessary by construction. Read-modify-write
// sequences run through docstore.Update (a write transaction), which
// replaces both the per-user mutexes and the flock: correct even with
// a second process on the same database.
type Store struct {
	c *docstore.Collection
}

// NewStore opens the users collection. dataDir is only used to migrate
// a legacy JSON-file store (pre-SQLite layout) on first run.
func NewStore(ds *docstore.Store, dataDir string) (*Store, error) {
	c, err := ds.Collection("users",
		docstore.WithUniqueIndex("login", "$.login"),
		docstore.WithIndex("email", "$.email", true),
	)
	if err != nil {
		return nil, err
	}
	s := &Store{c: c}
	if err := s.migrateLegacy(dataDir); err != nil {
		return nil, err
	}
	return s, nil
}

// migrateLegacy imports <dataDir>/users/*.json once (only when the
// collection is empty), then renames the directory to users.pre-sqlite
// as a rollback-friendly backup. A corrupt legacy file aborts with a
// clear error — same fail-fast behavior the old loadAll had.
func (s *Store) migrateLegacy(dataDir string) error {
	legacy := filepath.Join(dataDir, "users")
	if _, err := os.Stat(legacy); os.IsNotExist(err) {
		return nil
	}
	if n, err := s.c.Count(); err != nil || n > 0 {
		return err
	}

	entries, err := os.ReadDir(legacy)
	if err != nil {
		return err
	}
	imported := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(legacy, e.Name()))
		if err != nil {
			return err
		}
		var u User
		if err := json.Unmarshal(b, &u); err != nil {
			return fmt.Errorf("corrupt user file %s: %w", e.Name(), err)
		}
		if err := s.c.Put(u.ID, &u); err != nil {
			return fmt.Errorf("migrate user %s: %w", u.ID, err)
		}
		imported++
	}
	if err := os.Rename(legacy, legacy+".pre-sqlite"); err != nil {
		return err
	}
	slog.Info("migrated legacy user store to sqlite", "users", imported, "backup", legacy+".pre-sqlite")
	return nil
}

// Create stores a new user. Login uniqueness is enforced by the unique
// index — a concurrent duplicate signup loses with ErrExists, no global
// lock needed.
func (s *Store) Create(u *User) error {
	return s.c.Insert(u.ID, u)
}

// AddUsedBytes atomically adds delta (which may be negative) to the
// user's UsedBytes. The read-modify-write runs in a single write
// transaction, so concurrent completes/deletes for the same user can't
// clobber the total. Clamps at zero on over-subtract (defensive: we'd
// rather show 0 than a negative number if accounting slips).
func (s *Store) AddUsedBytes(id string, delta int64) error {
	return s.c.Update(id, func(raw []byte) ([]byte, error) {
		var u User
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, err
		}
		u.UsedBytes += delta
		if u.UsedBytes < 0 {
			u.UsedBytes = 0
		}
		return json.Marshal(&u)
	})
}

// SetUsedBytes overwrites UsedBytes with an authoritative value (e.g.
// recomputed from a full S3 listing). Same transactional rules as
// AddUsedBytes.
func (s *Store) SetUsedBytes(id string, v int64) error {
	if v < 0 {
		v = 0
	}
	return s.c.Update(id, func(raw []byte) ([]byte, error) {
		var u User
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, err
		}
		u.UsedBytes = v
		return json.Marshal(&u)
	})
}

// Update persists the given user record. Callers fetch via GetByID,
// mutate a field, and call Update — but that snapshot may be stale for
// UsedBytes, which is updated by concurrent independent flows (uploads
// completing, deletes, recounts). No Update caller legitimately sets
// UsedBytes, so the live value always wins over the caller's snapshot;
// the transaction makes the merge atomic.
func (s *Store) Update(u *User) error {
	return s.c.Update(u.ID, func(raw []byte) ([]byte, error) {
		var current User
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		u.UsedBytes = current.UsedBytes
		return json.Marshal(u)
	})
}

func (s *Store) Delete(id string) error {
	return s.c.Delete(id)
}

func (s *Store) GetByID(id string) (*User, error) {
	var u User
	if err := s.c.Get(id, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetByLogin(login string) (*User, error) {
	var u User
	if err := s.c.GetBy("login", login, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// EmailTaken reports whether email is already used by a user other than
// exceptID (pass "" when creating a new user). Case-insensitive via the
// NOCASE index; an empty email is never considered taken.
func (s *Store) EmailTaken(email, exceptID string) bool {
	if email == "" {
		return false
	}
	taken, err := s.c.ExistsBy("email", email, exceptID)
	if err != nil {
		slog.Error("email lookup failed", "err", err)
		return false
	}
	return taken
}

// List returns every user. Errors are logged, not returned, to keep the
// historical signature; the collection is small (admin UI listing).
func (s *Store) List() []*User {
	out := []*User{}
	err := s.c.Each(func(id string, raw []byte) error {
		var u User
		if err := json.Unmarshal(raw, &u); err != nil {
			return fmt.Errorf("corrupt user %s: %w", id, err)
		}
		out = append(out, &u)
		return nil
	})
	if err != nil {
		slog.Error("user list failed", "err", err)
	}
	return out
}
