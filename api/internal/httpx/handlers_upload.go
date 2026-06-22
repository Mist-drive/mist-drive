package httpx

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/yann/mist-drive/api/internal/compress"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/s3x"
	"github.com/yann/mist-drive/api/internal/uploads"
)

type initReq struct {
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	PartSize int64  `json:"partSize"` // optional, defaults to 8 MiB
}
type initResp struct {
	UploadID string        `json:"uploadId"`
	PartSize int64         `json:"partSize"`
	URLs     []s3x.PartURL `json:"urls"`
}

const defaultPartSize = 8 * 1024 * 1024

func (s *Server) uploadInit(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	var r initReq
	if err := c.BodyParser(&r); err != nil || r.Key == "" || r.Size <= 0 {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	r.Key = strings.TrimPrefix(strings.TrimSpace(r.Key), "/")
	if r.Key == "" || strings.Contains(r.Key, "..") {
		return fiber.NewError(fiber.StatusBadRequest, "invalid key")
	}
	if s.isProcessingBlocked(u.ID, r.Key) {
		return fiber.NewError(fiber.StatusConflict, "destination is currently being processed")
	}
	// When replacing an existing file the quota check must consider only the
	// net size change (newSize - existingSize), not the full newSize, because
	// the old object is removed when the multipart upload completes.
	existingSize, _ := s.S3.StatObject(c.Context(), u.Bucket(), r.Key)
	effectiveUsed := max(int64(0), u.UsedBytes-existingSize)
	if !s.Reservations.TryReserve(u.ID, r.Size, effectiveUsed, u.QuotaBytes) {
		return fiber.NewError(fiber.StatusRequestEntityTooLarge, "quota exceeded")
	}
	if free := quota.DiskFree(s.Cfg.DataDir); free > 0 && r.Size > free {
		s.Reservations.Release(u.ID, r.Size)
		return fiber.NewError(fiber.StatusInsufficientStorage, "not enough disk space")
	}
	if r.PartSize <= 0 {
		r.PartSize = defaultPartSize
	}
	ttl := s.Cfg.UploadTTL
	uploadID, urls, err := s.S3.InitMultipart(c.Context(), u.Bucket(), r.Key, r.Size, r.PartSize, ttl)
	if err != nil {
		s.Reservations.Release(u.ID, r.Size)
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	_ = s.Uploads.Save(&uploads.State{
		UserID: u.ID, UploadID: uploadID, Bucket: u.Bucket(), Key: r.Key,
		Size: r.Size, PartSize: r.PartSize, CreatedAt: time.Now(),
	})
	return c.JSON(initResp{UploadID: uploadID, PartSize: r.PartSize, URLs: urls})
}

type completeReq struct {
	UploadID string             `json:"uploadId"`
	Parts    []s3x.CompletePart `json:"parts"`
}

func (s *Server) uploadComplete(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	var r completeReq
	if err := c.BodyParser(&r); err != nil || r.UploadID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	st, err := s.Uploads.Get(u.ID, r.UploadID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "upload not found")
	}
	// Stat the existing object before completing — CompleteMultipart
	// atomically replaces it, so this is the only window to read the old size.
	oldSize, _ := s.S3.StatObject(c.Context(), st.Bucket, st.Key)
	if err := s.S3.CompleteMultipart(c.Context(), st.Bucket, st.Key, st.UploadID, r.Parts); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	size, etag, err := s.S3.StatObjectFull(c.Context(), st.Bucket, st.Key)
	if err == nil {
		_ = s.Users.AddUsedBytes(u.ID, size-oldSize)
		ext := strings.ToLower(filepath.Ext(st.Key))
		if s.CompressQueue != nil && (ext == ".zip" || ext == ".jpg" || ext == ".jpeg") {
			_ = s.CompressQueue.Enqueue(compress.Item{
				Bucket:  st.Bucket,
				Key:     st.Key,
				Size:    size,
				ETag:    etag,
				AddedAt: time.Now(),
			})
		}
	}
	s.Reservations.Release(u.ID, st.Size)
	_ = s.Uploads.Delete(u.ID, r.UploadID)
	s.publishChange(u.ID)
	return c.JSON(fiber.Map{"ok": true, "size": size})
}

type abortReq struct {
	UploadID string `json:"uploadId"`
}

func (s *Server) uploadAbort(c *fiber.Ctx) error {
	u, err := s.currentUser(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "no user")
	}
	var r abortReq
	if err := c.BodyParser(&r); err != nil || r.UploadID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	st, err := s.Uploads.Get(u.ID, r.UploadID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "upload not found")
	}
	_ = s.S3.AbortMultipart(c.Context(), st.Bucket, st.Key, st.UploadID)
	s.Reservations.Release(u.ID, st.Size)
	_ = s.Uploads.Delete(u.ID, r.UploadID)
	return c.JSON(fiber.Map{"ok": true})
}
