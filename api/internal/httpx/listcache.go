package httpx

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/s3x"
)

// listCacheTTL bounds how stale a cached listing can get when the bucket
// is changed outside the API.
// ponytail: edits via mc or the MinIO console show up within 5 min; have
// those tools publish a change event if that ever matters.
const listCacheTTL = 5 * time.Minute

// listBootNonce makes every ETag from a previous process run mismatch.
var listBootNonce = strconv.FormatInt(time.Now().UnixNano(), 36)

type cachedList struct {
	version uint64
	objects []s3x.ObjectInfo
	at      time.Time
}

// listCache keeps each user's last whole-bucket listing, tagged with the
// events hub version it was taken at. Zero value is ready to use.
type listCache struct {
	mu sync.Mutex
	m  map[string]cachedList
}

func (lc *listCache) get(uid string, version uint64, now time.Time) (cachedList, bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	e, ok := lc.m[uid]
	if !ok || e.version != version || now.Sub(e.at) >= listCacheTTL {
		return cachedList{}, false
	}
	return e, true
}

func (lc *listCache) put(uid string, e cachedList) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.m == nil {
		lc.m = map[string]cachedList{}
	}
	lc.m[uid] = e
}

// listETag identifies one user's listing: boot, version and listing time
// (so a TTL relist changes it), plus a hash of the uid (a shared browser
// cache never matches another user's entry) and of the processing markers.
func listETag(uid string, e cachedList, processing []string) string {
	slices.Sort(processing)
	h := sha256.New()
	h.Write([]byte(uid))
	for _, p := range processing {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return fmt.Sprintf(`"%s-%d-%d-%x"`, listBootNonce, e.version, e.at.UnixNano(), h.Sum(nil)[:8])
}

// listRoot serves the whole-bucket listing every client polls. MinIO is
// only hit when the user's bucket changed (events hub version) or the
// cached copy expired; browsers revalidate with If-None-Match and get a
// body-less 304.
func (s *Server) listRoot(c *fiber.Ctx, uid, bucket string) error {
	now := time.Now()
	// Read before listing: a change during the listing bumps the version
	// and the entry stored below is already stale for the next request.
	version := s.Events.Version(uid)
	entry, ok := s.lists.get(uid, version, now)
	if !ok {
		objs, err := s.S3.ListObjects(c.Context(), bucket, "")
		if err != nil {
			return s.serverError("files: list objects", err)
		}
		entry = cachedList{version: version, objects: objs, at: now}
		s.lists.put(uid, entry)
	}
	processing := s.listProcessing(uid)
	etag := listETag(uid, entry, slices.Clone(processing))
	c.Set(fiber.HeaderETag, etag)
	c.Set(fiber.HeaderCacheControl, "private, no-cache")
	c.Set(fiber.HeaderVary, fiber.HeaderAuthorization)
	// A proxy that recompresses the body may weaken the tag to W/"...".
	if strings.TrimPrefix(c.Get(fiber.HeaderIfNoneMatch), "W/") == etag {
		return c.SendStatus(fiber.StatusNotModified)
	}
	return c.JSON(fiber.Map{
		"objects":    entry.objects,
		"processing": processing,
	})
}
