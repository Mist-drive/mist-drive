// Package sessions persists refresh sessions (see fiberauth.RefreshSession)
// in the docstore collection "refresh_sessions", keyed by session id.
package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/creativeyann17/go-docstore"
	fiberauth "github.com/creativeyann17/go-fiber-auth"
)

// ErrRotated means another request rotated the session first.
var ErrRotated = errors.New("refresh session already rotated")

type Store struct {
	c *docstore.Collection
}

func NewStore(ds *docstore.Store) (*Store, error) {
	c, err := ds.Collection("refresh_sessions")
	if err != nil {
		return nil, fmt.Errorf("open refresh_sessions collection: %w", err)
	}
	return &Store{c: c}, nil
}

func (s *Store) Create(sess fiberauth.RefreshSession) error {
	return s.c.Insert(sess.ID, &sess)
}

func (s *Store) Get(id string) (fiberauth.RefreshSession, error) {
	var sess fiberauth.RefreshSession
	err := s.c.Get(id, &sess)
	return sess, err
}

func (s *Store) Delete(id string) error {
	return s.c.Delete(id)
}

// ListByUID returns every stored session of uid, expired ones included.
// ponytail: json_extract scan, add a generated-column index if the table grows.
func (s *Store) ListByUID(uid string) ([]fiberauth.RefreshSession, error) {
	docs, err := s.c.Find("$.uid", "=", uid, 0)
	if err != nil {
		return nil, fmt.Errorf("find refresh sessions: %w", err)
	}
	out := make([]fiberauth.RefreshSession, 0, len(docs))
	for _, d := range docs {
		var sess fiberauth.RefreshSession
		if json.Unmarshal(d.Raw, &sess) == nil {
			out = append(out, sess)
		}
	}
	return out, nil
}

// DeleteByUID removes every session of uid.
func (s *Store) DeleteByUID(uid string) error {
	list, err := s.ListByUID(uid)
	if err != nil {
		return err
	}
	for _, sess := range list {
		_ = s.c.Delete(sess.ID)
	}
	return nil
}

// Swap persists a rotated session only if the stored token is still
// oldHash, so two concurrent refreshes can't both rotate.
func (s *Store) Swap(next fiberauth.RefreshSession, oldHash string) error {
	return s.c.Update(next.ID, func(raw []byte) ([]byte, error) {
		var cur fiberauth.RefreshSession
		if err := json.Unmarshal(raw, &cur); err != nil {
			return nil, fmt.Errorf("decode refresh session: %w", err)
		}
		if cur.HashedToken != oldHash {
			return nil, ErrRotated
		}
		return json.Marshal(&next)
	})
}

// PruneExpired deletes expired sessions and returns how many.
// ponytail: full scan, fine for a self-hosted user count; index expiresAt if it grows.
func (s *Store) PruneExpired() int {
	now := time.Now()
	var ids []string
	_ = s.c.Each(func(id string, raw []byte) error {
		var sess fiberauth.RefreshSession
		if json.Unmarshal(raw, &sess) == nil && !now.Before(sess.ExpiresAt) {
			ids = append(ids, id)
		}
		return nil
	})
	for _, id := range ids {
		_ = s.c.Delete(id)
	}
	return len(ids)
}
