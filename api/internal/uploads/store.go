package uploads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
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

type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(dataDir string) (*Store, error) {
	p := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(p, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: p}, nil
}

func (s *Store) userDir(uid string) string { return filepath.Join(s.root, uid) }

func (s *Store) Save(st *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.userDir(st.UserID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	p := filepath.Join(d, st.UploadID+".json")
	tmp := p + ".tmp"
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Store) Get(uid, uploadID string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(s.userDir(uid), uploadID+".json"))
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) Delete(uid, uploadID string) error {
	return os.Remove(filepath.Join(s.userDir(uid), uploadID+".json"))
}

// WalkAll returns all persisted upload states.
func (s *Store) WalkAll() ([]*State, error) {
	out := []*State{}
	err := filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var st State
		if json.Unmarshal(b, &st) == nil {
			out = append(out, &st)
		}
		return nil
	})
	return out, err
}
