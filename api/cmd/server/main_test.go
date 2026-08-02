package main

import (
	"path/filepath"
	"testing"

	"github.com/creativeyann17/go-docstore"

	fiberauth "github.com/creativeyann17/go-fiber-auth"
	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/users"
)

func newTestUserStore(t *testing.T) *users.Store {
	t.Helper()
	ds, err := docstore.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("docstore.Open: %v", err)
	}
	t.Cleanup(func() { ds.Close() })
	store, err := users.NewStore(ds, t.TempDir())
	if err != nil {
		t.Fatalf("users.NewStore: %v", err)
	}
	return store
}

// seedAdmin inserts an admin user directly (bypassing bootstrapAdmin's
// create path, which needs a real S3 client) — these tests only cover
// the sync/no-op branches, which never touch s3c.
func seedAdmin(t *testing.T, store *users.Store, login, password string) *users.User {
	t.Helper()
	hash, err := fiberauth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	u := &users.User{ID: "admin-id", Login: login, BcryptPwd: hash, Role: users.RoleAdmin}
	if err := store.Create(u); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	return u
}

func TestBootstrapAdminSyncsPasswordWhenChanged(t *testing.T) {
	store := newTestUserStore(t)
	seedAdmin(t, store, "admin", "firstpass1")
	before, _ := store.GetByLogin("admin")

	cfg := &config.Config{AdminLogin: "admin", AdminPassword: "secondpass2"}
	if err := bootstrapAdmin(cfg, store, nil); err != nil {
		t.Fatalf("bootstrapAdmin (password changed): %v", err)
	}

	after, err := store.GetByLogin("admin")
	if err != nil {
		t.Fatalf("GetByLogin: %v", err)
	}
	if !fiberauth.VerifyPassword(after.BcryptPwd, "secondpass2") {
		t.Fatal("password must match the new ADMIN_PASSWORD after sync")
	}
	if fiberauth.VerifyPassword(after.BcryptPwd, "firstpass1") {
		t.Fatal("old password must no longer verify")
	}
	if after.TokenVersion <= before.TokenVersion {
		t.Fatalf("TokenVersion must bump on an actual password sync (before=%d after=%d)", before.TokenVersion, after.TokenVersion)
	}
	if after.ID != before.ID {
		t.Fatal("sync must update the existing record, not create a second admin account")
	}
}

func TestBootstrapAdminNoopWhenPasswordUnchanged(t *testing.T) {
	store := newTestUserStore(t)
	seedAdmin(t, store, "admin", "samepass1")
	before, _ := store.GetByLogin("admin")

	cfg := &config.Config{AdminLogin: "admin", AdminPassword: "samepass1"}
	if err := bootstrapAdmin(cfg, store, nil); err != nil {
		t.Fatalf("bootstrapAdmin (unchanged password): %v", err)
	}

	after, err := store.GetByLogin("admin")
	if err != nil {
		t.Fatalf("GetByLogin: %v", err)
	}
	if after.TokenVersion != before.TokenVersion {
		t.Fatalf("TokenVersion changed on a no-op boot: before=%d after=%d", before.TokenVersion, after.TokenVersion)
	}
	if after.BcryptPwd != before.BcryptPwd {
		t.Fatal("hash must stay stable (not re-hashed/rewritten) when the password is unchanged")
	}
}
