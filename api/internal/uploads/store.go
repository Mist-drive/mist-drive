package uploads

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/creativeyann17/go-docstore"
)

type State struct {
	UserID    string    `json:"userId"`
	UploadID  string    `json:"uploadId"`
	Bucket    string    `json:"bucket"`
	Key       string    `json:"key"`
	Size      int64     `json:"size"`
	PartSize  int64     `json:"partSize"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store persists in-flight multipart upload state as documents keyed
// "userID/uploadID" (the old <uploads>/<uid>/<id>.json path, as an id).
type Store struct {
	c *docstore.Collection
}

// NewStore opens the uploads collection. dataDir is only used to
// migrate the legacy JSON layout on first run.
func NewStore(ds *docstore.Store, dataDir string) (*Store, error) {
	c, err := ds.Collection("uploads")
	if err != nil {
		return nil, err
	}
	s := &Store{c: c}
	if err := s.migrateLegacy(dataDir); err != nil {
		return nil, err
	}
	return s, nil
}

// migrateLegacy imports <dataDir>/uploads/<uid>/<id>.json once (when
// the collection is empty), then renames the tree to uploads.pre-sqlite.
// Unreadable/corrupt files are skipped, matching the old WalkAll's
// best-effort behavior — upload state is ephemeral (hours) by nature.
func (s *Store) migrateLegacy(dataDir string) error {
	legacy := filepath.Join(dataDir, "uploads")
	if _, err := os.Stat(legacy); os.IsNotExist(err) {
		return nil
	}
	if n, err := s.c.Count(); err != nil || n > 0 {
		return err
	}

	imported := 0
	filepath.Walk(legacy, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var st State
		if json.Unmarshal(b, &st) != nil {
			return nil
		}
		if s.c.Put(key(st.UserID, st.UploadID), &st) == nil {
			imported++
		}
		return nil
	})
	if err := os.Rename(legacy, legacy+".pre-sqlite"); err != nil {
		return err
	}
	slog.Info("migrated legacy upload store to sqlite", "uploads", imported, "backup", legacy+".pre-sqlite")
	return nil
}

func key(uid, uploadID string) string { return uid + "/" + uploadID }

func (s *Store) Save(st *State) error {
	return s.c.Put(key(st.UserID, st.UploadID), st)
}

func (s *Store) Get(uid, uploadID string) (*State, error) {
	var st State
	if err := s.c.Get(key(uid, uploadID), &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) Delete(uid, uploadID string) error {
	return s.c.Delete(key(uid, uploadID))
}

// WalkAll returns all persisted upload states.
func (s *Store) WalkAll() ([]*State, error) {
	out := []*State{}
	err := s.c.Each(func(id string, raw []byte) error {
		var st State
		if json.Unmarshal(raw, &st) == nil {
			out = append(out, &st)
		}
		return nil
	})
	return out, err
}
