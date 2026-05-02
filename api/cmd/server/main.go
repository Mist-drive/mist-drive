package main

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/google/uuid"

	"github.com/yann/mist-drive/api/internal/auth"
	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/events"
	"github.com/yann/mist-drive/api/internal/httpx"
	"github.com/yann/mist-drive/api/internal/logger"
	"github.com/yann/mist-drive/api/internal/quota"
	"github.com/yann/mist-drive/api/internal/s3x"
	"github.com/yann/mist-drive/api/internal/uploads"
	"github.com/yann/mist-drive/api/internal/users"
	"github.com/yann/mist-drive/api/internal/webui"
)

// Version is overridden at build time via -ldflags "-X main.Version=1.2.3".
// Defaults to "dev" so ad-hoc builds skip the version check.
var Version = "dev"

func main() {
	cfg := config.Load()

	appLog := logger.New(logger.Config{
		ServiceName: cfg.ServiceName,
		LogLevel:    cfg.LogLevel,
		LogPath:     cfg.LogPath,
		SkipPaths:   []string{"/health"},
		Compress:    true,
	})

	userStore, err := users.NewStore(cfg.DataDir)
	if err != nil {
		appLog.Fatal("users store init: %v", err)
	}
	uploadStore, err := uploads.NewStore(cfg.DataDir)
	if err != nil {
		appLog.Fatal("uploads store init: %v", err)
	}
	s3c, err := s3x.New(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3UseSSL, cfg.PublicS3Host)
	if err != nil {
		appLog.Fatal("s3 client init: %v", err)
	}

	if err := bootstrapAdmin(cfg, userStore, s3c); err != nil {
		appLog.Fatal("admin bootstrap: %v", err)
	}
	appLog.Info("admin user ready (login=%s)", cfg.AdminLogin)

	go gcStaleUploads(cfg, uploadStore, s3c, appLog.With("component", "upload-gc"))

	app := fiber.New(fiber.Config{AppName: "mist-drive", DisableStartupMessage: true})
	app.Use(recover.New())
	// CORS is intentionally not configured: the SPA is served from the
	// same origin as the API (see webui.Mount below), so cross-origin
	// requests shouldn't reach us in the first place. If you ever need
	// to talk to this API from a different origin, add cors.New() back
	// here with an explicit allowlist — never "*" once auth is real.

	// Request logger (skips configured paths)
	app.Use(func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		if !appLog.ShouldSkip(c.Path()) {
			appLog.Debug("%s %s %d %v",
				c.Method(),
				c.Path(),
				c.Response().StatusCode(),
				time.Since(start))
		}
		return err
	})

	srv := &httpx.Server{
		Cfg: cfg, Users: userStore, S3: s3c,
		Uploads:      uploadStore,
		Reservations: quota.New(),
		Events:       events.NewHub(),
		Version:      Version,
	}
	srv.Register(app)

	// Embedded SPA — must come AFTER srv.Register so API/auth/ws
	// routes take precedence. Anything that falls through lands on
	// the Vite build baked into the binary via //go:embed.
	if err := webui.Mount(app); err != nil {
		appLog.Fatal("webui mount: %v", err)
	}

	appLog.Info("starting %s on port %s (level=%s)", cfg.ServiceName, cfg.Port, cfg.LogLevel)
	if err := app.Listen(":" + cfg.Port); err != nil {
		appLog.Fatal("listen: %v", err)
	}
}

func bootstrapAdmin(cfg *config.Config, s *users.Store, s3c *s3x.Client) error {
	if _, err := s.GetByLogin(cfg.AdminLogin); err == nil {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	id := uuid.NewString()
	u := &users.User{
		ID: id, Login: cfg.AdminLogin, BcryptPwd: hash,
		QuotaBytes: cfg.DefaultQuota,
		Role:       users.RoleAdmin, CreatedAt: time.Now(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s3c.EnsureBucket(ctx, u.Bucket()); err != nil {
		return err
	}
	return s.Create(u)
}

func gcStaleUploads(cfg *config.Config, us *uploads.Store, s3c *s3x.Client, appLog *logger.Logger) {
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if n := uploads.GC(ctx, us, s3c, cfg.UploadTTL); n > 0 {
			appLog.Warn("reclaimed %d stale upload(s)", n)
		}
	}
	run()
	t := time.NewTicker(1 * time.Hour)
	defer t.Stop()
	for range t.C {
		run()
	}
}
