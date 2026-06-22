package compress

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestQueue_EnqueueDequeue(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(filepath.Join(dir, "queue.json"))

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
	dir := t.TempDir()
	q := NewQueue(filepath.Join(dir, "queue.json"))

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
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")

	q1 := NewQueue(path)
	if err := q1.Enqueue(Item{Key: "persist.zip", Size: 9999, AddedAt: time.Now()}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// New Queue instance reads same file
	q2 := NewQueue(path)
	got, err := q2.Dequeue()
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if got == nil || got.Key != "persist.zip" || got.Size != 9999 {
		t.Errorf("unexpected item: %+v", got)
	}

	// File should still exist but be empty array
	b, _ := os.ReadFile(path)
	if string(b) != "[]" {
		t.Errorf("expected empty array file, got: %s", b)
	}
}

func TestQueue_EmptyDequeue(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(filepath.Join(dir, "queue.json"))

	got, err := q.Dequeue()
	if err != nil {
		t.Fatalf("unexpected error on empty queue: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil from empty queue, got %+v", got)
	}
}

func TestQueue_ConcurrentEnqueue(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(filepath.Join(dir, "queue.json"))

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
