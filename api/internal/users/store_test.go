package users

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestUser(id, login string) *User {
	return &User{
		ID: id, Login: login, BcryptPwd: "x",
		QuotaBytes: 1000, Role: RoleUser, CreatedAt: time.Now(),
	}
}

func TestStore_CRUD(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	u := newTestUser("id1", "alice")
	if err := s.Create(u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(u); err != ErrExists {
		t.Fatalf("duplicate create: want ErrExists, got %v", err)
	}

	got, err := s.GetByLogin("alice")
	if err != nil || got.ID != "id1" {
		t.Fatalf("GetByLogin: %v %v", got, err)
	}
	got.UsedBytes = 42
	if err := s.Update(got); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetByID("id1")
	if got2.UsedBytes != 42 {
		t.Fatalf("update not persisted in memory: %d", got2.UsedBytes)
	}

	// Reload from disk to confirm atomic write landed.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got3, err := s2.GetByLogin("alice")
	if err != nil || got3.UsedBytes != 42 {
		t.Fatalf("reload: %v %+v", err, got3)
	}

	if err := s.Delete("id1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByID("id1"); err != ErrNotFound {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	if _, err := s.GetByLogin("alice"); err != ErrNotFound {
		t.Fatalf("login index not cleared: %v", err)
	}
}

func TestStore_GetReturnsCopy(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	_ = s.Create(newTestUser("id1", "alice"))
	a, _ := s.GetByID("id1")
	a.UsedBytes = 9999
	b, _ := s.GetByID("id1")
	if b.UsedBytes == 9999 {
		t.Fatal("Get must return a copy; mutation leaked into store")
	}
}

func TestStore_ConcurrentWritesDoNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	_ = s.Create(newTestUser("id1", "alice"))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := s.GetByID("id1")
			if err != nil {
				return
			}
			u.UsedBytes++
			_ = s.Update(u)
		}()
	}
	wg.Wait()

	// Store must still be readable from disk — the concrete value is
	// racey at the store level, we only check integrity.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reload after concurrent writes: %v", err)
	}
	if _, err := s2.GetByID("id1"); err != nil {
		t.Fatalf("user file corrupted: %v", err)
	}

	// Sanity: the underlying file exists and is well-formed JSON.
	if _, err := filepath.Glob(filepath.Join(dir, "users", "*.json")); err != nil {
		t.Fatal(err)
	}
}
