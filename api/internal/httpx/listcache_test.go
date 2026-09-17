package httpx

import (
	"testing"
	"time"

	"github.com/yann/mist-drive/api/internal/s3x"
)

func TestListCache(t *testing.T) {
	var lc listCache
	now := time.Now()
	if _, ok := lc.get("u1", 0, now); ok {
		t.Fatal("empty cache hit")
	}
	lc.put("u1", cachedList{version: 3, objects: []s3x.ObjectInfo{{Key: "a"}}, at: now})
	if e, ok := lc.get("u1", 3, now.Add(time.Minute)); !ok || len(e.objects) != 1 {
		t.Fatal("fresh entry missed")
	}
	if _, ok := lc.get("u1", 4, now); ok {
		t.Fatal("entry served after a change (version bumped)")
	}
	if _, ok := lc.get("u1", 3, now.Add(listCacheTTL)); ok {
		t.Fatal("entry served past TTL")
	}
	if _, ok := lc.get("u2", 3, now); ok {
		t.Fatal("entry served to another user")
	}
}

func TestListETag(t *testing.T) {
	now := time.Now()
	base := cachedList{version: 1, at: now}
	tag := listETag("u1", base, nil)
	if listETag("u1", base, nil) != tag {
		t.Fatal("etag not stable")
	}
	for name, other := range map[string]string{
		"other user":  listETag("u2", base, nil),
		"new version": listETag("u1", cachedList{version: 2, at: now}, nil),
		"relisted":    listETag("u1", cachedList{version: 1, at: now.Add(time.Second)}, nil),
		"processing":  listETag("u1", base, []string{"photos"}),
	} {
		if other == tag {
			t.Errorf("%s: etag unchanged", name)
		}
	}
	if listETag("u1", base, []string{"a", "b"}) != listETag("u1", base, []string{"b", "a"}) {
		t.Error("processing order must not change the etag")
	}
}
