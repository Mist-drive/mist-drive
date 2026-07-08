package compress

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/creativeyann17/go-docstore"
)

type Item struct {
	Bucket  string    `json:"bucket"`
	Key     string    `json:"key"`
	Size    int64     `json:"size"`
	ETag    string    `json:"etag"`
	AddedAt time.Time `json:"added_at"`
}

// Queue is a FIFO of recompression work, persisted as a single
// document ("queue") in the compress collection. Enqueue/Dequeue run
// as docstore transactions, so they're safe across processes.
type Queue struct {
	c *docstore.Collection
}

const queueDoc = "queue"

// NewQueue opens the compress collection. legacyPath points at the old
// single-JSON-file queue (<DATA_DIR>/compress-queue.json) and is
// imported once, then renamed with a .pre-sqlite suffix.
func NewQueue(ds *docstore.Store, legacyPath string) (*Queue, error) {
	c, err := ds.Collection("compress")
	if err != nil {
		return nil, err
	}
	q := &Queue{c: c}

	// Ensure the queue document exists so Dequeue can Update it.
	var items []Item
	if err := c.Get(queueDoc, &items); errors.Is(err, docstore.ErrNotFound) {
		if err := c.Put(queueDoc, []Item{}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	if err := q.migrateLegacy(legacyPath); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *Queue) migrateLegacy(legacyPath string) error {
	if legacyPath == "" {
		return nil
	}
	b, err := os.ReadFile(legacyPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var legacy []Item
	if err := json.Unmarshal(b, &legacy); err != nil {
		return fmt.Errorf("corrupt legacy compress queue: %w", err)
	}
	if len(legacy) > 0 {
		if err := q.c.Put(queueDoc, legacy); err != nil {
			return err
		}
	}
	if err := os.Rename(legacyPath, legacyPath+".pre-sqlite"); err != nil {
		return err
	}
	slog.Info("migrated legacy compress queue to sqlite", "items", len(legacy))
	return nil
}

func (q *Queue) Enqueue(item Item) error {
	return q.c.Update(queueDoc, func(raw []byte) ([]byte, error) {
		var items []Item
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		items = append(items, item)
		return json.Marshal(items)
	})
}

func (q *Queue) Dequeue() (*Item, error) {
	var out *Item
	err := q.c.Update(queueDoc, func(raw []byte) ([]byte, error) {
		var items []Item
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		if len(items) == 0 {
			out = nil
			return raw, nil
		}
		out = &items[0]
		return json.Marshal(items[1:])
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
