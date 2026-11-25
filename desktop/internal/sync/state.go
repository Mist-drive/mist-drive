package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mist-drive-desktop/internal/settings"
)

// baseEntry is what a file looked like on both sides at its last
// successful sync. Without it the engine can't tell "deleted here" from
// "created on another machine".
type baseEntry struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // local mtime, unix nano
	ETag  string `json:"etag"`  // remote etag; "" = adopt on next pass (upload doesn't return it)
}

// statePath is per (server, local dir, remote prefix) so re-pointing a
// folder or switching environments starts from a clean base.
func statePath(apiURL, local, prefix string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(apiURL + "|" + local + "|" + prefix))
	return filepath.Join(dir, "mist-drive", "sync-state", hex.EncodeToString(sum[:8])+".json"), nil
}

// loadState returns an empty map when the file doesn't exist yet (first
// run): the reconciler then never deletes, only uploads/downloads.
func loadState(path string) (map[string]baseEntry, error) {
	m := map[string]baseEntry{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse sync state %s: %w", path, err)
	}
	return m, nil
}

// saveState writes atomically (temp + rename) so a crash never leaves a
// truncated base that would look like "everything was deleted".
func saveState(path string, m map[string]baseEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode sync state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ForgetFolder drops a folder's base so re-adding it later behaves like a
// first run (never deletes) instead of replaying stale deletions.
func ForgetFolder(apiURL string, f settings.SyncFolder) error {
	p, err := statePath(apiURL, f.Local, f.RemotePrefix)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
