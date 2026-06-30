package httpx

import (
	"archive/zip"
	"bufio"
	"context"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/events"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/s3x"
	"github.com/yann/mist-drive/api/internal/users"
)

// renameCopyWorkers bounds how many CopyObject calls a folder rename runs
// concurrently against MinIO — fan-out without unbounded goroutines on
// folders with thousands of objects.
const renameCopyWorkers = 8

// copyFolderObjects copies every object in folderObjs from under oldPath
// to the equivalent path under newPath, fanning out across a bounded
// worker pool instead of one MinIO round-trip at a time. Returns the
// source keys that were copied successfully (safe to remove) and the
// first error encountered, if any; on error, copying still runs to
// completion for the remaining objects rather than aborting the batch
// (these are independent S3 calls, so there's nothing to roll back, and
// stopping early would leave the worker pool's already-in-flight calls
// to finish anyway).
func copyFolderObjects(ctx context.Context, s3c *s3x.Client, bucket, oldPath, newPath string, folderObjs []s3x.ObjectInfo) ([]string, error) {
	workers := min(renameCopyWorkers, len(folderObjs))

	work := make(chan s3x.ObjectInfo)
	go func() {
		defer close(work)
		for _, o := range folderObjs {
			work <- o
		}
	}()

	var mu sync.Mutex
	var oldKeys []string
	var firstErr error

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for o := range work {
				dstKey := newPath + "/" + strings.TrimPrefix(o.Key, oldPath+"/")
				err := s3c.CopyObject(ctx, bucket, o.Key, dstKey)
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
				} else {
					oldKeys = append(oldKeys, o.Key)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	return oldKeys, firstErr
}

// sumObjectSizes totals the byte size of an object listing.
func sumObjectSizes(objs []s3x.ObjectInfo) int64 {
	var total int64
	for _, o := range objs {
		total += o.Size
	}
	return total
}

// recountUsedBytes re-derives a user's usedBytes from an authoritative
// full bucket listing and persists it. Keeps the counter honest across
// crashes / stray parts / partial deletes — but it's a full listing, so
// callers that already know the size delta of what changed (uploads
// completing, single-file/prefix deletes) should apply that delta via
// AddUsedBytes instead of paying for this. Reserved for recomputeUsage,
// which exists precisely to fix drift.
func (s *Server) recountUsedBytes(ctx context.Context, uid, bucket string) (total int64, count int, err error) {
	objs, err := s.S3.ListObjects(ctx, bucket, "")
	if err != nil {
		return 0, 0, err
	}
	total = sumObjectSizes(objs)
	if err := s.Users.SetUsedBytes(uid, total); err != nil {
		return 0, 0, err
	}
	return total, len(objs), nil
}

func (s *Server) listFiles(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	prefix := c.Query("prefix", "")
	objs, err := s.S3.ListObjects(c.Context(), u.Bucket(), prefix)
	if err != nil {
		return s.serverError("files: list objects", err)
	}
	return c.JSON(fiber.Map{
		"objects":    objs,
		"processing": s.listProcessing(u.ID),
	})
}

func (s *Server) mkdir(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := c.BodyParser(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		return fiber.NewError(fiber.StatusBadRequest, "missing path")
	}
	if hasDotDotSegment(body.Path) {
		return fiber.NewError(fiber.StatusBadRequest, "invalid path")
	}
	key := strings.Trim(body.Path, "/") + "/.keep"
	prefix := strings.Trim(body.Path, "/")
	if s.isProcessingBlocked(u.ID, prefix) {
		return fiber.NewError(fiber.StatusConflict, "destination is currently being processed")
	}
	if err := s.S3.PutEmptyObject(c.Context(), u.Bucket(), key); err != nil {
		return s.serverError("files: mkdir", err)
	}
	s.publishChange(u.ID)
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Server) deleteFile(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	key := c.Query("key")
	prefix := c.Query("prefix")
	if key == "" && prefix == "" {
		return fiber.NewError(fiber.StatusBadRequest, "missing key or prefix")
	}
	if hasDotDotSegment(key) || hasDotDotSegment(prefix) {
		return fiber.NewError(fiber.StatusBadRequest, "invalid key or prefix")
	}

	if key != "" && s.isProcessingBlocked(u.ID, key) {
		return fiber.NewError(fiber.StatusConflict, "resource is currently being processed")
	}
	if prefix != "" && s.isProcessingBlocked(u.ID, strings.TrimSuffix(prefix, "/")) {
		return fiber.NewError(fiber.StatusConflict, "resource is currently being processed")
	}

	var freed int64
	var count int

	if prefix != "" {
		// Recursive delete under prefix. Batch via RemoveObjects — one
		// request instead of N — and reconcile used bytes afterwards.
		objs, err := s.S3.ListObjects(c.Context(), u.Bucket(), prefix)
		if err != nil {
			return s.serverError("files: delete list objects", err)
		}
		keys := make([]string, 0, len(objs))
		for _, o := range objs {
			keys = append(keys, o.Key)
			freed += o.Size
			count++
		}
		if err := s.S3.RemoveObjects(c.Context(), u.Bucket(), keys); err != nil {
			return s.serverError("files: delete remove objects", err)
		}
	} else {
		size, err := s.S3.StatObject(c.Context(), u.Bucket(), key)
		if err != nil {
			return fiber.NewError(fiber.StatusNotFound, "not found")
		}
		if err := s.S3.RemoveObject(c.Context(), u.Bucket(), key); err != nil {
			return s.serverError("files: delete remove object", err)
		}
		freed = size
		count = 1
	}

	// freed is already exact — it came from the same StatObject/ListObjects
	// call that drove the actual deletion above — so apply it as a delta
	// instead of paying for a second full-bucket listing (recountUsedBytes)
	// just to recompute the same number. recountUsedBytes stays reserved
	// for recomputeUsage, which exists precisely to catch drift.
	_ = s.Users.AddUsedBytes(u.ID, -freed)
	s.publishChange(u.ID)
	return c.JSON(fiber.Map{"ok": true, "count": count, "freed": freed})
}

// recomputeUsage re-derives the caller's usedBytes from an authoritative
// S3 listing. Useful after enabling compression, recovering from a
// crash, or any time the counter drifts from reality.
func (s *Server) recomputeUsage(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	total, count, err := s.recountUsedBytes(c.Context(), u.ID, u.Bucket())
	if err != nil {
		return s.serverError("files: recompute usage", err)
	}
	return c.JSON(fiber.Map{"ok": true, "usedBytes": total, "count": count})
}

func (s *Server) download(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	key := c.Query("key")
	if key == "" {
		return fiber.NewError(fiber.StatusBadRequest, "missing key")
	}
	if hasDotDotSegment(key) {
		return fiber.NewError(fiber.StatusBadRequest, "invalid key")
	}
	url, err := s.S3.PresignGet(c.Context(), u.Bucket(), key, s.Cfg.PresignDownload)
	if err != nil {
		return s.serverError("files: presign download", err)
	}
	return c.JSON(fiber.Map{"url": url})
}

// downloadZip streams a zip archive of every object under `prefix`.
// Store (no compression) — most user files are already compressed.
// Streams via fasthttp's SetBodyStreamWriter so memory stays flat.
func (s *Server) downloadZip(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	return s.streamZip(c, u, c.Query("prefix", ""))
}

// downloadZipTicket mints a short-lived single-use ticket so the browser
// can stream a zip via plain navigation without putting the session JWT
// in the URL. Authenticated (Authorization header) — runs under /api.
func (s *Server) downloadZipTicket(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	var body struct {
		Prefix string `json:"prefix"`
	}
	_ = c.BodyParser(&body)
	ticket, err := s.dlGuard().issue(u.ID, body.Prefix)
	if err != nil {
		return s.serverError("files: issue download ticket", err)
	}
	return c.JSON(fiber.Map{"ticket": ticket})
}

// downloadZipByTicket streams a zip authorized solely by a ticket. It
// lives OUTSIDE the /api group (top-level route) so no JWT auth runs —
// the ticket is the credential. Prefix comes from the ticket, never the
// query, so a ticket authorizes exactly one download.
func (s *Server) downloadZipByTicket(c *fiber.Ctx) error {
	uid, prefix, ok := s.dlGuard().consume(c.Query("ticket"))
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid or expired download ticket")
	}
	u, err := s.Users.GetByID(uid)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	return s.streamZip(c, u, prefix)
}

// streamZip is the shared archive-streaming core used by both the
// header-authenticated route (desktop) and the ticket route (browser).
func (s *Server) streamZip(c *fiber.Ctx, u *users.User, prefix string) error {
	objs, err := s.S3.ListObjects(c.Context(), u.Bucket(), prefix)
	if err != nil {
		return s.serverError("files: zip list objects", err)
	}
	if len(objs) == 0 {
		return fiber.NewError(fiber.StatusNotFound, "no objects under prefix")
	}
	total := sumObjectSizes(objs)
	if s.Cfg.MaxZipBytes > 0 && total > s.Cfg.MaxZipBytes {
		return fiber.NewError(fiber.StatusRequestEntityTooLarge, "folder exceeds MAX_ZIP_BYTES")
	}

	filename := zipFilename(prefix)
	c.Set(fiber.HeaderContentType, "application/zip")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+filename+`"`)

	// Wall-time ceiling for the whole stream. This — not the download
	// ticket's TTL — is what bounds a big/slow transfer; the ticket is
	// validated once up front and plays no part during streaming.
	timeout := s.Cfg.ZipStreamTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	bucket := u.Bucket()
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		zw := zip.NewWriter(w)
		defer zw.Close()
		for _, o := range objs {
			if strings.HasSuffix(o.Key, "/.keep") {
				continue
			}
			rel := strings.TrimPrefix(o.Key, prefix)
			if rel == "" {
				rel = path.Base(o.Key)
			}
			hdr := &zip.FileHeader{Name: rel, Method: zip.Store, Modified: o.LastModified}
			entry, err := zw.CreateHeader(hdr)
			if err != nil {
				return
			}
			rc, err := s.S3.GetObject(ctx, bucket, o.Key)
			if err != nil {
				return
			}
			if _, err := io.Copy(entry, rc); err != nil {
				_ = rc.Close()
				return
			}
			_ = rc.Close()
		}
	})
	return nil
}

// zipFilename derives a friendly archive name from the prefix.
// `foo/bar/` -> "bar.zip"; empty prefix -> "download.zip".
func zipFilename(prefix string) string {
	p := strings.Trim(prefix, "/")
	if p == "" {
		return "download.zip"
	}
	parts := strings.Split(p, "/")
	return parts[len(parts)-1] + ".zip"
}

func (s *Server) renameFile(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	var body struct {
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if err := c.BodyParser(&body); err != nil || strings.TrimSpace(body.Path) == "" || strings.TrimSpace(body.NewName) == "" {
		return fiber.NewError(fiber.StatusBadRequest, "missing path or newName")
	}
	oldPath := strings.Trim(body.Path, "/")
	newName := strings.TrimSpace(body.NewName)
	if strings.Contains(newName, "/") {
		return fiber.NewError(fiber.StatusBadRequest, "newName must not contain slashes")
	}
	if hasDotDotSegment(oldPath) || hasDotDotSegment(newName) {
		return fiber.NewError(fiber.StatusBadRequest, "invalid path or newName")
	}

	// New path = same parent directory + new name
	parent := ""
	if i := strings.LastIndex(oldPath, "/"); i >= 0 {
		parent = oldPath[:i]
	}
	newPath := newName
	if parent != "" {
		newPath = parent + "/" + newName
	}
	if oldPath == newPath {
		return c.JSON(fiber.Map{"ok": true})
	}

	const renameMargin = 1 << 30 // 1 GiB safety buffer for the temporary copy

	bucket := u.Bucket()
	// Determine if source is a file or folder; collect children for folders.
	fileSize, statErr := s.S3.StatObject(c.Context(), bucket, oldPath)
	isFile := statErr == nil
	var folderObjs []s3x.ObjectInfo
	if !isFile {
		var lerr error
		folderObjs, lerr = s.S3.ListObjects(c.Context(), bucket, oldPath+"/")
		if lerr != nil || len(folderObjs) == 0 {
			return fiber.NewError(fiber.StatusNotFound, "not found")
		}
	}

	// Guard: reject if source is already being processed
	if s.isProcessingBlocked(u.ID, oldPath) {
		return fiber.NewError(fiber.StatusConflict, "resource is currently being processed")
	}

	// Disk space check: rename requires a temporary second copy of the data.
	// Reject early if the server filesystem doesn't have enough free space.
	if free := quota.DiskFree(s.Cfg.DataDir); free > 0 {
		var copySize int64
		if isFile {
			copySize = fileSize
		} else {
			copySize = sumObjectSizes(folderObjs)
		}
		if copySize+renameMargin > free {
			return fiber.NewError(fiber.StatusInsufficientStorage,
				"not enough disk space to rename (would require a temporary copy)")
		}
	}

	// Collision check
	if isFile {
		if _, err := s.S3.StatObject(c.Context(), bucket, newPath); err == nil {
			return fiber.NewError(fiber.StatusConflict, "a file with that name already exists")
		}
	} else {
		if existing, _ := s.S3.ListObjects(c.Context(), bucket, newPath+"/"); len(existing) > 0 {
			return fiber.NewError(fiber.StatusConflict, "a folder with that name already exists")
		}
		if _, err := s.S3.StatObject(c.Context(), bucket, newPath); err == nil {
			return fiber.NewError(fiber.StatusConflict, "a file with that name already exists")
		}
	}

	s.addProcessing(u.ID, oldPath)
	// Publish immediately so all clients see the processing state.
	s.publishChange(u.ID)

	uid := u.ID
	go func() {
		defer func() {
			s.removeProcessing(uid, oldPath)
		}()
		ctx := context.Background()
		var renameErr error

		if isFile {
			if renameErr = s.S3.CopyObject(ctx, bucket, oldPath, newPath); renameErr == nil {
				renameErr = s.S3.RemoveObject(ctx, bucket, oldPath)
			}
		} else {
			oldKeys, copyErr := copyFolderObjects(ctx, s.S3, bucket, oldPath, newPath, folderObjs)
			renameErr = copyErr
			// Match the previous sequential behavior: only remove the old
			// objects once every copy in the folder succeeded. A partial
			// failure leaves the source folder untouched (some objects now
			// duplicated at the destination) rather than silently deleting
			// originals whose copy never happened or errored.
			if renameErr == nil {
				renameErr = s.S3.RemoveObjects(ctx, bucket, oldKeys)
			}
		}

		if renameErr != nil {
			if s.Events != nil {
				s.Events.Publish(uid, events.Event{
					Type:    events.RenameError,
					Message: renameErr.Error(),
					Path:    oldPath,
				})
			}
		} else {
			s.publishChange(uid)
		}
	}()

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"ok": true})
}
