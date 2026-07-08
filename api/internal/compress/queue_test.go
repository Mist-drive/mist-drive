package compress

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/creativeyann17/go-docstore"
)

// newTestQueue opens a queue over a fresh docstore; the returned dir
// can be reused with openQueueAt to simulate a restart.
func openQueueAt(t *testing.T, dir string) *Queue {
	t.Helper()
	ds, err := docstore.Open(filepath.Join(dir, "mist.db"))
	if err != nil {
		t.Fatalf("docstore open: %v", err)
	}
	t.Cleanup(func() { ds.Close() })
	q, err := NewQueue(ds, filepath.Join(dir, "compress-queue.json"))
	if err != nil {
		t.Fatalf("queue init: %v", err)
	}
	return q
}

func newTestQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	dir := t.TempDir()
	return openQueueAt(t, dir), dir
}

func TestQueue_EnqueueDequeue(t *testing.T) {
	q, _ := newTestQueue(t)

	item := Item{Bucket: "b", Key: "test.zip", Size: 1024, ETag: "abc123", AddedAt: time.Now()}
	if err := q.Enqueue(item); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if got == nil {
		t.Fatal("expected item, got nil")
	}
	if got.Key != item.Key || got.ETag != item.ETag || got.Size != item.Size {
		t.Errorf("got %+v, want %+v", got, item)
	}

	// Queue should be empty now
	empty, err := q.Dequeue()
	if err != nil {
		t.Fatalf("second dequeue: %v", err)
	}
	if empty != nil {
		t.Errorf("expected nil, got %+v", empty)
	}
}

func TestQueue_FIFO(t *testing.T) {
	q, _ := newTestQueue(t)

	keys := []string{"a.zip", "b.zip", "c.zip"}
	for _, k := range keys {
		if err := q.Enqueue(Item{Key: k, AddedAt: time.Now()}); err != nil {
			t.Fatalf("enqueue %s: %v", k, err)
		}
	}

	for _, want := range keys {
		got, err := q.Dequeue()
		if err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if got == nil || got.Key != want {
			t.Errorf("want key=%s, got %v", want, got)
		}
	}
}

func TestQueue_PersistAcrossInstances(t *testing.T) {
	q1, dir := newTestQueue(t)
	if err := q1.Enqueue(Item{Key: "persist.zip", Size: 9999, AddedAt: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// New Queue instance over the same database.
	q2 := openQueueAt(t, dir)
	got, err := q2.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if got == nil || got.Key != "persist.zip" || got.Size != 9999 {
		t.Errorf("unexpected item: %+v", got)
	}
}

func TestQueue_EmptyDequeue(t *testing.T) {
	q, _ := newTestQueue(t)

	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("unexpected error on empty queue: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil from empty queue, got %+v", got)
	}
}

func TestQueue_ConcurrentEnqueue(t *testing.T) {
	q, _ := newTestQueue(t)

	n := 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_ = q.Enqueue(Item{Key: "file.zip", Size: int64(i), AddedAt: time.Now()})
		}(i)
	}
	wg.Wait()

	count := 0
	for {
		item, err := q.Dequeue()
		if err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if item == nil {
			break
		}
		count++
	}
	if count != n {
		t.Errorf("expected %d items, got %d", n, count)
	}
}

// TestQueue_MigratesLegacyFile seeds the old single-file layout and
// asserts the one-time import + rename to .pre-sqlite.
func TestQueue_MigratesLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "compress-queue.json")
	items := []Item{{Key: "old.zip", Size: 7}}
	b, _ := json.Marshal(items)
	if err := os.WriteFile(legacy, b, 0o600); err != nil {
		t.Fatal(err)
	}

	q := openQueueAt(t, dir)
	got, err := q.Dequeue()
	if err != nil || got == nil || got.Key != "old.zip" {
		t.Fatalf("migrated item not found: %v %+v", err, got)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy file should be renamed away")
	}
	if _, err := os.Stat(legacy + ".pre-sqlite"); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
	}
	for _, tc := range cases {
		got := formatBytes(tc.n)
		if got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
