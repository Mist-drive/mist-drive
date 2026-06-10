// Package httpx wires the HTTP surface of the API. Handlers are split
// across sibling files by concern:
//
//	handlers_auth.go   — login, me
//	handlers_files.go  — list, delete, download(-zip)
//	handlers_upload.go — multipart init/complete/abort
//	handlers_admin.go  — user CRUD
//	handlers_ws.go     — websocket push channel
//
// Everything below is the only glue that needs to touch more than one
// of them: the Server struct and the route registration.
package httpx

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/events"
	"github.com/yann/mist-drive/api/internal/features"
	"github.com/yann/mist-drive/api/internal/logger"
	"github.com/yann/mist-drive/api/internal/notify"
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
	Events       *events.Hub
	Log          *logger.Logger
	Version      string
	Features     features.Features
	Mailer       *notify.Mailer
	throttle     *loginThrottle
	throttleOnce sync.Once
	dlTickets    *downloadTickets
	dlOnce       sync.Once
	bootTime     time.Time // tokens issued before this are rejected (set in Register)
	procMu       sync.RWMutex
	processing   map[string]map[string]bool // userID → processing path prefixes
}

// loginGuard lazily builds the login throttle. Lazy so a bare
// Server{} (constructed directly in some unit tests) still works
// without an explicit init step.
func (s *Server) loginGuard() *loginThrottle {
	s.throttleOnce.Do(func() {
		if s.throttle == nil {
			s.throttle = newLoginThrottle()
		}
	})
	return s.throttle
}

// dlGuard lazily builds the download-ticket store. Same lazy-init
// rationale as loginGuard.
func (s *Server) dlGuard() *downloadTickets {
	s.dlOnce.Do(func() {
		if s.dlTickets == nil {
			s.dlTickets = newDownloadTickets()
		}
	})
	return s.dlTickets
}

// secWarn emits a structured WARN security event. No-ops when Log is nil
// (e.g. in unit tests that create a bare Server{}).
func (s *Server) secWarn(msg string, args ...any) {
	if s.Log != nil {
		s.Log.LogAttrs(slog.LevelWarn, msg, args...)
	}
}

func (s *Server) TokenVersionMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		u, err := s.Users.GetByID(UID(c))
		if err != nil || tokenVer(c) < u.TokenVersion {
			return fiber.NewError(fiber.StatusUnauthorized, "session revoked, please log in again")
		}
		return c.Next()
	}
}

func (s *Server) Register(app *fiber.App) {
	// Truncate to whole seconds: JWT iat is stored as Unix seconds, so
	// sub-second precision on bootTime would cause false 401s for tokens
	// issued in the same second the server started.
	// Dev builds skip the check entirely (zero time is before any real iat).
	var bootTime time.Time
	if s.Version != "dev" {
		bootTime = time.Now().Truncate(time.Second)
	}
	s.bootTime = bootTime // wsHandler re-runs the same check during first-message auth

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true, "version": s.Version, "features": s.Features})
	})
	app.Post("/auth/login", s.login)
	// Zip stream authorized by a single-use ticket (not the JWT). Lives
	// OUTSIDE /api on purpose: the /api group's AuthMiddleware runs for
	// every /api/* path, so a no-JWT route must sit at top level. The
	// browser mints a ticket at POST /api/files/download-zip-ticket
	// (authenticated) then navigates here — keeping the JWT out of the URL.
	app.Get("/download-zip", s.downloadZipByTicket)
	// WebSocket push channel. Also top-level (no /api auth): the handshake
	// can't carry an Authorization header and we no longer accept the JWT
	// as a query param, so the client authenticates by sending
	// {"type":"auth","token":"<jwt>"} as its first frame — validated in
	// wsHandler within a short deadline before joining the hub.
	app.Get("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	}, websocket.New(s.wsHandler))

	api := app.Group("/api", AuthMiddleware(s.Cfg.JWTSecret, bootTime, s.Log), s.TokenVersionMiddleware())
	api.Get("/me", s.me)
	api.Get("/login-history", s.loginHistory)
	api.Put("/me/email", s.updateEmail)
	api.Put("/me/password", s.changePassword)
	api.Post("/me/logout-all", s.logoutAll)

	files := api.Group("/files")
	files.Get("/", s.listFiles)
	files.Delete("/", s.deleteFile)
	files.Get("/download", s.download)
	files.Get("/download-zip", s.downloadZip) // header-authed (desktop)
	files.Post("/download-zip-ticket", s.downloadZipTicket)
	files.Post("/upload/init", s.uploadInit)
	files.Post("/upload/complete", s.uploadComplete)
	files.Post("/upload/abort", s.uploadAbort)
	files.Post("/mkdir", s.mkdir)
	files.Post("/rename", s.renameFile)
	files.Post("/recompute-usage", s.recomputeUsage)
	files.Get("/preview", s.previewFile)

	totp := api.Group("/totp")
	totp.Get("/setup", s.totpSetup)
	totp.Post("/enable", s.totpEnable)
	totp.Delete("/disable", s.totpDisable)
	totp.Post("/regen-backup", s.totpRegenBackup)

	devices := api.Group("/devices")
	devices.Get("/", s.listDevices)
	devices.Delete("/", s.revokeAllDevices)
	devices.Delete("/:id", s.revokeDevice)

	admin := api.Group("/admin", AdminOnly)
	admin.Get("/users", s.adminListUsers)
	admin.Post("/users", s.adminCreateUser)
	admin.Patch("/users/:id/quota", s.adminPatchQuota)
	admin.Delete("/users/:id", s.adminDeleteUser)
}

// currentUser is the tiny helper every authenticated handler uses to
// resolve the caller. Kept in the glue file so every split can reach
// it without circular imports.
func (s *Server) currentUser(c *fiber.Ctx) (*users.User, error) {
	return s.Users.GetByID(UID(c))
}

// publishChange is the one-liner every mutating handler calls after
// a successful write. Centralised so we don't forget on a code path.
func (s *Server) publishChange(uid string) {
	if s.Events != nil {
		s.Events.Publish(uid, events.Event{Type: events.FilesChanged})
	}
}

func (s *Server) addProcessing(userID, prefix string) {
	s.procMu.Lock()
	defer s.procMu.Unlock()
	if s.processing == nil {
		s.processing = make(map[string]map[string]bool)
	}
	if s.processing[userID] == nil {
		s.processing[userID] = make(map[string]bool)
	}
	s.processing[userID][prefix] = true
}

func (s *Server) removeProcessing(userID, prefix string) {
	s.procMu.Lock()
	defer s.procMu.Unlock()
	if s.processing == nil {
		return
	}
	delete(s.processing[userID], prefix)
	if len(s.processing[userID]) == 0 {
		delete(s.processing, userID)
	}
}

func (s *Server) isProcessingBlocked(userID, key string) bool {
	s.procMu.RLock()
	defer s.procMu.RUnlock()
	for prefix := range s.processing[userID] {
		if key == prefix || strings.HasPrefix(key, prefix+"/") {
			return true
		}
	}
	return false
}

// AddProcessing is the exported wrapper for addProcessing (used by tests).
func (s *Server) AddProcessing(userID, prefix string) { s.addProcessing(userID, prefix) }

// RemoveProcessing is the exported wrapper for removeProcessing (used by tests).
func (s *Server) RemoveProcessing(userID, prefix string) { s.removeProcessing(userID, prefix) }

// IsProcessingBlocked is the exported wrapper for isProcessingBlocked (used by tests).
func (s *Server) IsProcessingBlocked(userID, key string) bool {
	return s.isProcessingBlocked(userID, key)
}

func (s *Server) listProcessing(userID string) []string {
	s.procMu.RLock()
	defer s.procMu.RUnlock()
	out := make([]string, 0, len(s.processing[userID]))
	for p := range s.processing[userID] {
		out = append(out, p)
	}
	return out
}
