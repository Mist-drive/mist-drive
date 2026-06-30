package httpx

import (
	"bytes"
	"image"
	"image/jpeg"
	_ "image/gif"
	_ "image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	previewTextMax  = 4 * 1024
	previewImageMax = 20 * 1024 * 1024
	previewMaxDim   = 800
)

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

var textExts = map[string]bool{
	".txt": true, ".md": true, ".log": true, ".json": true,
	".yaml": true, ".yml": true, ".toml": true, ".csv": true,
	".xml": true, ".html": true, ".htm": true, ".svg": true,
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".rs": true, ".java": true, ".c": true, ".cpp": true,
	".h": true, ".sh": true, ".bash": true, ".zsh": true,
	".env": true, ".conf": true, ".ini": true, ".cfg": true,
	".sql": true, ".css": true, ".scss": true, ".less": true,
}

func filePreviewType(key string) string {
	ext := strings.ToLower(filepath.Ext(key))
	if imageExts[ext] {
		return "image"
	}
	if textExts[ext] {
		return "text"
	}
	return ""
}

func (s *Server) previewFile(c *fiber.Ctx) error {
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

	ptype := filePreviewType(key)

	if ptype == "" {
		rc, err := s.S3.GetObjectRange(c.Context(), u.Bucket(), key, 0, 511)
		if err != nil {
			return fiber.NewError(fiber.StatusNotFound, "not found")
		}
		sniff, _ := io.ReadAll(io.LimitReader(rc, 512))
		_ = rc.Close()
		ct := http.DetectContentType(sniff)
		switch {
		case strings.HasPrefix(ct, "image/"):
			ptype = "image"
		case strings.HasPrefix(ct, "text/"):
			ptype = "text"
		default:
			ptype = "binary"
		}
	}

	c.Set("X-Preview-Type", ptype)

	switch ptype {
	case "image":
		size, err := s.S3.StatObject(c.Context(), u.Bucket(), key)
		if err != nil {
			return fiber.NewError(fiber.StatusNotFound, "not found")
		}
		if size > previewImageMax {
			c.Set("X-Preview-Type", "binary")
			return c.Status(fiber.StatusOK).SendString("")
		}
		rc, err := s.S3.GetObject(c.Context(), u.Bucket(), key)
		if err != nil {
			return s.serverError("preview: get object", err)
		}
		defer rc.Close()
		img, _, err := image.Decode(rc)
		if err != nil {
			c.Set("X-Preview-Type", "binary")
			return c.Status(fiber.StatusOK).SendString("")
		}
		thumb := thumbnailImage(img, previewMaxDim)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 72}); err != nil {
			return s.serverError("preview: encode thumbnail", err)
		}
		c.Set(fiber.HeaderContentType, "image/jpeg")
		return c.Send(buf.Bytes())

	case "text":
		rc, err := s.S3.GetObjectRange(c.Context(), u.Bucket(), key, 0, int64(previewTextMax)-1)
		if err != nil {
			return s.serverError("preview: get object range", err)
		}
		defer rc.Close()
		raw, _ := io.ReadAll(rc)
		c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
		return c.SendString(safeUTF8(raw))

	default:
		return c.Status(fiber.StatusOK).SendString("")
	}
}

func thumbnailImage(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return src
	}
	scale := float64(maxDim) / float64(w)
	if sh := float64(maxDim) / float64(h); sh < scale {
		scale = sh
	}
	nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

func safeUTF8(b []byte) string {
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}
