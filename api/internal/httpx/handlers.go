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
	"github.com/google/uuid"
	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/s3x"
	"github.com/yann/mist-drive/api/internal/uploads"
	"github.com/yann/mist-drive/api/internal/users"
)

type Server struct {
	Cfg          *config.Config
	Users        *users.Store
	S3           *s3x.Client
	Uploads      *uploads.Store
	Reservations *quota.Reservations
}

func (s *Server) Register(app *fiber.App) {
	app.Get("/health", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"ok": true}) })
	app.Post("/auth/login", s.login)

	api := app.Group("/api", AuthMiddleware(s.Cfg.JWTSecret))
	api.Get("/me", s.me)

	files := api.Group("/files")
	files.Get("/", s.listFiles)
	files.Delete("/", s.deleteFile)
	files.Get("/download", s.download)
	files.Get("/download-zip", s.downloadZip)
	files.Post("/upload/init", s.uploadInit)
	files.Post("/upload/complete", s.uploadComplete)
	files.Post("/upload/abort", s.uploadAbort)

	admin := api.Group("/admin", AdminOnly)
	admin.Get("/users", s.adminListUsers)
	admin.Post("/users", s.adminCreateUser)
	admin.Patch("/users/:id/quota", s.adminPatchQuota)
	admin.Delete("/users/:id", s.adminDeleteUser)
}

// --- auth ---

type loginReq struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}
type loginResp struct {
	Token string             `json:"token"`
	User  users.PublicUser   `json:"user"`
}

func (s *Server) login(c *fiber.Ctx) error {
	var r loginReq
	if err := c.BodyParser(&r); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	u, err := s.Users.GetByLogin(r.Login)
	if err != nil || !auth.VerifyPassword(u.BcryptPwd, r.Password) {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}
	tok, err := auth.Issue(s.Cfg.JWTSecret, u.ID, string(u.Role), s.Cfg.JWTTTL)
	if err != nil {
		return err
	}
	return c.JSON(loginResp{Token: tok, User: u.Public()})
}

func (s *Server) me(c *fiber.Ctx) error {
	u, err := s.Users.GetByID(UID(c))
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user gone")
	}
	p := u.Public()
	p.ReservedBytes = s.Reservations.Get(u.ID)
	p.DiskFreeBytes = quota.DiskFree(s.Cfg.DataDir)
	return c.JSON(p)
}

// --- files ---

func (s *Server) currentUser(c *fiber.Ctx) (*users.User, error) {
	return s.Users.GetByID(UID(c))
}

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
	return c.JSON(objs)
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

	var freed int64
	var count int

	if prefix != "" {
		// Recursive delete of everything under the prefix (folder delete).
		objs, err := s.S3.ListObjects(c.Context(), u.Bucket(), prefix)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, err.Error())
		}
		for _, o := range objs {
			if err := s.S3.RemoveObject(c.Context(), u.Bucket(), o.Key); err != nil {
				continue
			}
			freed += o.Size
			count++
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

	// Atomic subtract — same reasoning as uploadComplete: avoid racing
	// concurrent deletes against each other (and against completes).
	_ = s.Users.AddUsedBytes(u.ID, -freed)
	return c.JSON(fiber.Map{"ok": true, "count": count, "freed": freed})
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

// downloadZip streams a zip archive of every object under `prefix` in
// the caller's bucket. The method is Store (no compression) because
// most user files are already compressed — spending CPU on Deflate for
// a few percent savings is not worth the latency hit on large folders.
//
// Streaming is done via fasthttp's SetBodyStreamWriter so memory stays
// flat: only one object is in flight at a time. The body writer runs
// *after* the handler returns, so all validation (auth, size cap) must
// happen before we reach it — once bytes start flowing we can no longer
// change the HTTP status.
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
	// We don't know the final compressed length (we're streaming), so
	// leave Content-Length unset; fasthttp will send chunked encoding.

	bucket := u.Bucket()
	// Detach from the request context: fasthttp recycles c after the
	// handler returns, and the stream writer runs afterwards.
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		zw := zip.NewWriter(w)
		defer zw.Close()
		for _, o := range objs {
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

// --- multipart uploads ---

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
	// sanitize key
	r.Key = strings.TrimPrefix(strings.TrimSpace(r.Key), "/")
	if r.Key == "" || strings.Contains(r.Key, "..") {
		return fiber.NewError(fiber.StatusBadRequest, "invalid key")
	}
	// Atomic reservation: prevents parallel uploads from oversubscribing the quota.
	if !s.Reservations.TryReserve(u.ID, r.Size, u.UsedBytes, u.QuotaBytes) {
		return fiber.NewError(fiber.StatusRequestEntityTooLarge, "quota exceeded")
	}
	// Also refuse if host disk wouldn't fit it (best-effort: assumes MinIO
	// shares the same volume as DATA_DIR, which is true for our compose setup).
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
	UploadID string              `json:"uploadId"`
	Parts    []s3x.CompletePart  `json:"parts"`
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
	if err := s.S3.CompleteMultipart(c.Context(), st.Bucket, st.Key, st.UploadID, r.Parts); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	// Authoritative size from S3. Use AddUsedBytes rather than
	// GetByID+Update so concurrent completes can't race each other
	// (they'd each read a stale copy and clobber the total).
	size, err := s.S3.StatObject(c.Context(), st.Bucket, st.Key)
	if err == nil {
		_ = s.Users.AddUsedBytes(u.ID, size)
	}
	s.Reservations.Release(u.ID, st.Size)
	_ = s.Uploads.Delete(u.ID, r.UploadID)
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

// --- admin ---

type createUserReq struct {
	Login      string `json:"login"`
	Password   string `json:"password"`
	QuotaBytes int64  `json:"quotaBytes"`
}

func (s *Server) adminListUsers(c *fiber.Ctx) error {
	all := s.Users.List()
	out := make([]users.PublicUser, 0, len(all))
	for _, u := range all {
		out = append(out, u.Public())
	}
	return c.JSON(out)
}

func (s *Server) adminCreateUser(c *fiber.Ctx) error {
	var r createUserReq
	if err := c.BodyParser(&r); err != nil || r.Login == "" || r.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	if r.QuotaBytes <= 0 {
		r.QuotaBytes = s.Cfg.DefaultQuota
	}
	hash, err := auth.HashPassword(r.Password)
	if err != nil {
		return err
	}
	id := uuid.NewString()
	u := &users.User{
		ID: id, Login: r.Login, BcryptPwd: hash,
		QuotaBytes: r.QuotaBytes,
		Role:       users.RoleUser, CreatedAt: time.Now(),
	}
	if err := s.S3.EnsureBucket(c.Context(), u.Bucket()); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	if err := s.Users.Create(u); err != nil {
		return fiber.NewError(fiber.StatusConflict, err.Error())
	}
	return c.JSON(u.Public())
}

type patchQuotaReq struct {
	QuotaBytes int64 `json:"quotaBytes"`
}

func (s *Server) adminPatchQuota(c *fiber.Ctx) error {
	id := c.Params("id")
	u, err := s.Users.GetByID(id)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "not found")
	}
	var r patchQuotaReq
	if err := c.BodyParser(&r); err != nil || r.QuotaBytes <= 0 {
		return fiber.NewError(fiber.StatusBadRequest, "bad body")
	}
	u.QuotaBytes = r.QuotaBytes
	if err := s.Users.Update(u); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(u.Public())
}

func (s *Server) adminDeleteUser(c *fiber.Ctx) error {
	id := c.Params("id")
	u, err := s.Users.GetByID(id)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "not found")
	}
	if u.Role == users.RoleAdmin {
		return fiber.NewError(fiber.StatusForbidden, "cannot delete admin")
	}
	_ = s.S3.RemoveBucket(c.Context(), u.Bucket())
	if err := s.Users.Delete(id); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}
