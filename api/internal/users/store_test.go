package users

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/creativeyann17/go-docstore"
)

func newTestUser(id, login string) *User {
	return &User{
		ID: id, Login: login, BcryptPwd: "x",
		QuotaBytes: 1000, Role: RoleUser, CreatedAt: time.Now(),
	}
}

// openStoreAt opens (or reopens) a store over dir — reopening the same
// dir simulates a process restart against the same mist.db.
func openStoreAt(t *testing.T, dir string) *Store {
	t.Helper()
	ds, err := docstore.Open(filepath.Join(dir, "mist.db"))
	if err != nil {
		t.Fatalf("docstore open: %v", err)
	}
	t.Cleanup(func() { ds.Close() })
	s, err := NewStore(ds, dir)
	if err != nil {
		t.Fatalf("store init: %v", err)
	}
	return s
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	return openStoreAt(t, dir), dir
}

func TestStore_CRUD(t *testing.T) {
	s, dir := newTestStore(t)

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
	// UsedBytes is intentionally not settable via Update — see
	// TestStore_UpdatePreservesUsedBytes — so exercise a regular field.
	got.Email = "alice@example.com"
	if err := s.Update(got); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUsedBytes("id1", 42); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetByID("id1")
	if got2.Email != "alice@example.com" || got2.UsedBytes != 42 {
		t.Fatalf("update not persisted: email=%q usedBytes=%d", got2.Email, got2.UsedBytes)
	}

	// Reopen against the same db to confirm the write landed on disk.
	s2 := openStoreAt(t, dir)
	got3, err := s2.GetByLogin("alice")
	if err != nil || got3.Email != "alice@example.com" || got3.UsedBytes != 42 {
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

func TestStore_EmailTaken(t *testing.T) {
	s, _ := newTestStore(t)
	alice := newTestUser("id1", "alice")
	alice.Email = "shared@example.com"
	if err := s.Create(alice); err != nil {
		t.Fatal(err)
	}
	bob := newTestUser("id2", "bob")
	if err := s.Create(bob); err != nil {
		t.Fatal(err)
	}

	if !s.EmailTaken("shared@example.com", "") {
		t.Fatal("want taken for a new user")
	}
	if !s.EmailTaken("SHARED@Example.com", "") {
		t.Fatal("want case-insensitive match")
	}
	// alice owns it — not "taken" relative to herself (idempotent re-save).
	if s.EmailTaken("shared@example.com", "id1") {
		t.Fatal("owner should not collide with themselves")
	}
	// bob trying to claim it is a collision.
	if !s.EmailTaken("shared@example.com", "id2") {
		t.Fatal("another user claiming the email should collide")
	}
	if s.EmailTaken("", "") {
		t.Fatal("empty email is never taken")
	}
	if s.EmailTaken("free@example.com", "") {
		t.Fatal("unused email should be free")
	}
}

func TestStore_GetReturnsCopy(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Create(newTestUser("id1", "alice"))
	a, _ := s.GetByID("id1")
	a.UsedBytes = 9999
	b, _ := s.GetByID("id1")
	if b.UsedBytes == 9999 {
		t.Fatal("Get must return a copy; mutation leaked into store")
	}
}

// TestStore_GetReturnsDeepCopyOfSlices guards the historical bug where
// reads shared slice backing arrays with a cache. With SQLite every read
// unmarshals fresh, so this is safe by construction — the test stays as
// a regression guard should a cache ever come back.
func TestStore_GetReturnsDeepCopyOfSlices(t *testing.T) {
	s, _ := newTestStore(t)
	u := newTestUser("id1", "alice")
	u.TOTPBackupCodes = []string{"a", "b", "c"}
	u.TrustedDevices = []TrustedDevice{{ID: "dev1"}, {ID: "dev2"}}
	u.LoginHistory = []LoginRecord{{IP: "1.1.1.1"}}
	_ = s.Create(u)

	got, _ := s.GetByID("id1")
	// In-place element removal, same pattern as verifyTOTP/revokeDevice.
	got.TOTPBackupCodes = append(got.TOTPBackupCodes[:1], got.TOTPBackupCodes[2:]...)
	got.TrustedDevices = got.TrustedDevices[:0]
	got.LoginHistory[0].IP = "mutated"

	again, _ := s.GetByID("id1")
	if len(again.TOTPBackupCodes) != 3 {
		t.Fatalf("TOTPBackupCodes leaked in-place mutation: %v", again.TOTPBackupCodes)
	}
	if len(again.TrustedDevices) != 2 {
		t.Fatalf("TrustedDevices leaked in-place mutation: %v", again.TrustedDevices)
	}
	if again.LoginHistory[0].IP != "1.1.1.1" {
		t.Fatalf("LoginHistory leaked in-place mutation: %v", again.LoginHistory)
	}
}

// TestStore_UpdatePreservesUsedBytes is the regression test for the
// lost-update race: a handler that Get-then-mutates-then-Updates a user
// (e.g. revoking a device) must not be able to clobber UsedBytes that
// changed concurrently via AddUsedBytes/SetUsedBytes (e.g. an upload
// completing) in between.
func TestStore_UpdatePreservesUsedBytes(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Create(newTestUser("id1", "alice"))

	stale, _ := s.GetByID("id1") // snapshot taken before the concurrent write below
	if err := s.AddUsedBytes("id1", 500); err != nil {
		t.Fatal(err)
	}

	stale.Email = "alice@example.com" // unrelated field, legitimately changed
	if err := s.Update(stale); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetByID("id1")
	if got.UsedBytes != 500 {
		t.Fatalf("Update clobbered concurrent UsedBytes: got %d, want 500", got.UsedBytes)
	}
	if got.Email != "alice@example.com" {
		t.Fatalf("Update should still persist other fields: got email %q", got.Email)
	}
}

func TestStore_ConcurrentWritesDoNotCorrupt(t *testing.T) {
	s, dir := newTestStore(t)
	_ = s.Create(newTestUser("id1", "alice"))

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			u, err := s.GetByID("id1")
			if err != nil {
				return
			}
			u.UsedBytes++
			_ = s.Update(u)
		})
	}
	wg.Wait()

	// Store must still be readable after reopening — the concrete value
	// is racey at the store level, we only check integrity.
	s2 := openStoreAt(t, dir)
	if _, err := s2.GetByID("id1"); err != nil {
		t.Fatalf("store corrupted: %v", err)
	}
}

// TestStore_AddUsedBytesConcurrentSameUserNoLostUpdates proves the
// transactional read-modify-write (docstore.Update) fully serializes
// concurrent writers for the SAME user — the guarantee the old per-user
// mutexes provided, now valid across processes too.
func TestStore_AddUsedBytesConcurrentSameUserNoLostUpdates(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Create(newTestUser("id1", "alice"))

	var wg sync.WaitGroup
	const n = 100
	for range n {
		wg.Go(func() {
			_ = s.AddUsedBytes("id1", 1)
		})
	}
	wg.Wait()

	got, _ := s.GetByID("id1")
	if got.UsedBytes != n {
		t.Fatalf("lost updates: got UsedBytes=%d, want %d", got.UsedBytes, n)
	}
}

func TestStore_DifferentUsersWriteInParallel(t *testing.T) {
	s, _ := newTestStore(t)
	ids := []string{"id1", "id2", "id3", "id4", "id5"}
	for i, id := range ids {
		_ = s.Create(newTestUser(id, fmt.Sprintf("user%d", i)))
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			for range 20 {
				_ = s.AddUsedBytes(id, 1)
			}
		})
	}
	wg.Wait()

	for _, id := range ids {
		got, _ := s.GetByID(id)
		if got.UsedBytes != 20 {
			t.Fatalf("user %s: lost updates, got UsedBytes=%d, want 20", id, got.UsedBytes)
		}
	}
}

func TestStore_ListReturnsAllUsers(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Create(newTestUser("id-a", "user-a"))
	_ = s.Create(newTestUser("id-b", "user-b"))
	_ = s.Create(newTestUser("id-c", "user-c"))

	all := s.List()
	if len(all) != 3 {
		t.Fatalf("List() returned %d users, want 3", len(all))
	}
}

func TestStore_GetByIDNotFound(t *testing.T) {
	s, _ := newTestStore(t)
	_, err := s.GetByID("nope")
	if err != ErrNotFound {
		t.Fatalf("GetByID(unknown) = %v, want ErrNotFound", err)
	}
}

func TestStore_UpdateNonExistent(t *testing.T) {
	s, _ := newTestStore(t)
	ghost := newTestUser("ghost-id", "ghost")
	err := s.Update(ghost)
	if err == nil {
		t.Fatal("Update on non-existent user should return error, got nil")
	}
}

// TestStore_MigratesLegacyJSON seeds the pre-SQLite layout
// (<dir>/users/<id>.json) and asserts the one-time import: users become
// queryable, the legacy dir is renamed to users.pre-sqlite, and a
// reopen does not re-import.
func TestStore_MigratesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "users")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, u := range []*User{newTestUser("id1", "alice"), newTestUser("id2", "bob")} {
		b, _ := json.MarshalIndent(u, "", "  ")
		if err := os.WriteFile(filepath.Join(legacy, u.ID+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := openStoreAt(t, dir)
	if u, err := s.GetByLogin("alice"); err != nil || u.ID != "id1" {
		t.Fatalf("migrated user not found: %v %+v", err, u)
	}
	if len(s.List()) != 2 {
		t.Fatalf("want 2 migrated users, got %d", len(s.List()))
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy dir should be renamed away")
	}
	if _, err := os.Stat(legacy + ".pre-sqlite"); err != nil {
		t.Fatalf("backup dir missing: %v", err)
	}

	// Reopen: no legacy dir anymore, no double import.
	s2 := openStoreAt(t, dir)
	if n := len(s2.List()); n != 2 {
		t.Fatalf("reopen re-imported or lost users: %d", n)
	}
}

// TestStore_MigrationAbortsOnCorruptFile keeps the old loadAll's
// fail-fast contract: a broken user file must abort startup loudly, not
// silently drop an account.
func TestStore_MigrationAbortsOnCorruptFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "users")
	os.MkdirAll(legacy, 0o755)
	os.WriteFile(filepath.Join(legacy, "bad.json"), []byte("{nope"), 0o600)

	ds, err := docstore.Open(filepath.Join(dir, "mist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()
	if _, err := NewStore(ds, dir); err == nil {
		t.Fatal("want migration error on corrupt file, got nil")
	}
}
