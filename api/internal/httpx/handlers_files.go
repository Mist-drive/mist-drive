package httpx

import (
	"archive/zip"
	"bufio"
	"context"
	"io"
	"path"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/events"
)

func (s *Server) listFiles(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	prefix := c.Query("prefix", "")
	objs, err := s.S3.ListObjects(c.Context(), u.Bucket(), prefix)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
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
	key := strings.Trim(body.Path, "/") + "/.keep"
	prefix := strings.Trim(body.Path, "/")
	if s.isProcessingBlocked(u.ID, prefix) {
		return fiber.NewError(fiber.StatusConflict, "destination is currently being processed")
	}
	if err := s.S3.PutEmptyObject(c.Context(), u.Bucket(), key); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
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
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		keys := make([]string, 0, len(objs))
		for _, o := range objs {
			keys = append(keys, o.Key)
			freed += o.Size
			count++
		}
		if err := s.S3.RemoveObjects(c.Context(), u.Bucket(), keys); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
	} else {
		size, err := s.S3.StatObject(c.Context(), u.Bucket(), key)
		if err != nil {
			return fiber.NewError(fiber.StatusNotFound, "not found")
		}
		if err := s.S3.RemoveObject(c.Context(), u.Bucket(), key); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		freed = size
		count = 1
	}

	// Recompute usedBytes authoritatively from S3. Cheap and keeps the
	// counter honest across crashes / stray parts / partial deletes.
	if remaining, lerr := s.S3.ListObjects(c.Context(), u.Bucket(), ""); lerr == nil {
		var total int64
		for _, o := range remaining {
			total += o.Size
		}
		_ = s.Users.SetUsedBytes(u.ID, total)
	} else {
		_ = s.Users.AddUsedBytes(u.ID, -freed)
	}
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
	objs, err := s.S3.ListObjects(c.Context(), u.Bucket(), "")
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	var total int64
	for _, o := range objs {
		total += o.Size
	}
	if err := s.Users.SetUsedBytes(u.ID, total); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true, "usedBytes": total, "count": len(objs)})
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
	url, err := s.S3.PresignGet(c.Context(), u.Bucket(), key, s.Cfg.PresignDownload)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
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
	prefix := c.Query("prefix", "")

	objs, err := s.S3.ListObjects(c.Context(), u.Bucket(), prefix)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if len(objs) == 0 {
		return fiber.NewError(fiber.StatusNotFound, "no objects under prefix")
	}
	var total int64
	for _, o := range objs {
		total += o.Size
	}
	if s.Cfg.MaxZipBytes > 0 && total > s.Cfg.MaxZipBytes {
		return fiber.NewError(fiber.StatusRequestEntityTooLarge, "folder exceeds MAX_ZIP_BYTES")
	}

	filename := zipFilename(prefix)
	c.Set(fiber.HeaderContentType, "application/zip")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+filename+`"`)

	bucket := u.Bucket()
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
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

	bucket := u.Bucket()
	// Determine if source is a file or folder
	_, statErr := s.S3.StatObject(c.Context(), bucket, oldPath)
	isFile := statErr == nil
	if !isFile {
		children, lerr := s.S3.ListObjects(c.Context(), bucket, oldPath+"/")
		if lerr != nil || len(children) == 0 {
			return fiber.NewError(fiber.StatusNotFound, "not found")
		}
	}

	// Guard: reject if source is already being processed
	if s.isProcessingBlocked(u.ID, oldPath) {
		return fiber.NewError(fiber.StatusConflict, "resource is currently being processed")
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
			// Snapshot before modifying
			objs, lerr := s.S3.ListObjects(ctx, bucket, oldPath+"/")
			if lerr != nil {
				renameErr = lerr
			} else {
				oldKeys := make([]string, 0, len(objs))
				for _, o := range objs {
					dstKey := newPath + "/" + strings.TrimPrefix(o.Key, oldPath+"/")
					if err := s.S3.CopyObject(ctx, bucket, o.Key, dstKey); err != nil {
						renameErr = err
						break
					}
					oldKeys = append(oldKeys, o.Key)
				}
				if renameErr == nil {
					renameErr = s.S3.RemoveObjects(ctx, bucket, oldKeys)
				}
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
