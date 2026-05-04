// Package logger provides a thin, level-based facade over log/slog.
//
// - stdout gets a human-friendly text handler
// - the rotating file (lumberjack) gets structured JSON
// - both destinations see every record via a fan-out handler
//
// The public surface keeps printf-style sugar (Debug/Info/Warn/Error/Fatal)
// for ergonomic call sites, plus With(k,v) for context propagation.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Config holds logger configuration
type Config struct {
	ServiceName string
	LogLevel    string   // debug | info | warn | error
	LogPath     string   // defaults to /app/logs/app.log
	SkipPaths   []string // paths to skip in request logging
	MaxSizeMB   int      // max size before rotation (default 10)
	MaxBackups  int      // max rotated files to keep (default 10)
	MaxAgeDays  int      // max days to keep files (default 10)
	Compress    bool     // compress rotated files (default true)
}

// Logger wraps *slog.Logger with printf-style sugar and a skip set.
type Logger struct {
	sl        *slog.Logger
	skipPaths map[string]struct{}
}

// New creates a new Logger with the given config.
// stdout -> text handler, file -> json handler, both level-filtered.
func New(cfg Config) *Logger {
	if cfg.LogPath == "" {
		cfg.LogPath = "/app/logs/app.log"
	}
	if cfg.MaxSizeMB == 0 {
		cfg.MaxSizeMB = 10
	}
	if cfg.MaxBackups == 0 {
		cfg.MaxBackups = 10
	}
	if cfg.MaxAgeDays == 0 {
		cfg.MaxAgeDays = 10
	}
	if cfg.SkipPaths == nil {
		cfg.SkipPaths = []string{"/health"}
	}

	lvl := parseLevel(cfg.LogLevel)
	hOpts := &slog.HandlerOptions{Level: lvl, AddSource: true}

	fileWriter := &lumberjack.Logger{
		Filename:   cfg.LogPath,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
	}

	stdoutH := slog.NewTextHandler(os.Stdout, hOpts)
	fileH := slog.NewJSONHandler(io.Writer(fileWriter), hOpts)
	fan := &fanoutHandler{handlers: []slog.Handler{stdoutH, fileH}}

	sl := slog.New(fan).With(slog.String("service", cfg.ServiceName))

	skip := make(map[string]struct{}, len(cfg.SkipPaths))
	for _, p := range cfg.SkipPaths {
		skip[p] = struct{}{}
	}
	return &Logger{sl: sl, skipPaths: skip}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// --- fanout handler: dispatches each record to all sub-handlers ---

type fanoutHandler struct {
	handlers []slog.Handler
}

func (f *fanoutHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}
func (f *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.handlers {
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
func (f *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: next}
}
func (f *fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &fanoutHandler{handlers: next}
}

// --- public API ---

// With returns a child logger with extra attributes attached to every record.
// Use it in handlers to pin request_id / user_id, etc.
func (l *Logger) With(args ...any) *Logger {
	return &Logger{sl: l.sl.With(args...), skipPaths: l.skipPaths}
}

// Printf-style sugar. slog's own Log call sites evaluate attrs lazily,
// but we accept printf format+args for backward-compat with existing call sites.
// The format is only rendered if the level is enabled.

func (l *Logger) Debug(format string, v ...any) { l.log(slog.LevelDebug, format, v...) }
func (l *Logger) Info(format string, v ...any)  { l.log(slog.LevelInfo, format, v...) }
func (l *Logger) Warn(format string, v ...any)  { l.log(slog.LevelWarn, format, v...) }
func (l *Logger) Error(format string, v ...any) { l.log(slog.LevelError, format, v...) }

// LogAttrs emits a structured record with slog key-value pairs.
// Use for request logging where fields (uid, status, latency) matter.
func (l *Logger) LogAttrs(level slog.Level, msg string, args ...any) {
	if !l.sl.Enabled(context.Background(), level) {
		return
	}
	l.sl.Log(context.Background(), level, msg, args...)
}

// Fatal logs at error level then exits 1. Use at startup for unrecoverable errors.
func (l *Logger) Fatal(format string, v ...any) {
	l.log(slog.LevelError, format, v...)
	os.Exit(1)
}

func (l *Logger) log(lvl slog.Level, format string, v ...any) {
	if !l.sl.Enabled(context.Background(), lvl) {
		return
	}
	msg := format
	if len(v) > 0 {
		msg = fmt.Sprintf(format, v...)
	}
	l.sl.Log(context.Background(), lvl, msg)
}

// ShouldSkip returns true if the path should be skipped in request logging.
// O(1) lookup.
func (l *Logger) ShouldSkip(path string) bool {
	_, ok := l.skipPaths[path]
	return ok
}
