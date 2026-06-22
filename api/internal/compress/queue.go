package compress

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type Item struct {
	Bucket  string    `json:"bucket"`
	Key     string    `json:"key"`
	Size    int64     `json:"size"`
	ETag    string    `json:"etag"`
	AddedAt time.Time `json:"added_at"`
}

type Queue struct {
	path string
	mu   sync.Mutex
}

func NewQueue(path string) *Queue {
	return &Queue{path: path}
}

func (q *Queue) Enqueue(item Item) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	items, err := q.load()
	if err != nil {
		items = []Item{}
	}
	items = append(items, item)
	return q.save(items)
}

func (q *Queue) Dequeue() (*Item, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items, err := q.load()
	if err != nil || len(items) == 0 {
		return nil, err
	}
	item := items[0]
	if err := q.save(items[1:]); err != nil {
		return nil, err
	}
	return &item, nil
}

func (q *Queue) load() ([]Item, error) {
	b, err := os.ReadFile(q.path)
	if os.IsNotExist(err) {
		return []Item{}, nil
	}
	if err != nil {
		return nil, err
	}
	var items []Item
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queue) save(items []Item) error {
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, q.path)
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
