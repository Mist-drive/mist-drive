# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Mist Drive

Self-hosted drive. Go Fiber API + Vite/Bun/React SPA + MinIO object storage. No database — per-user JSON files on disk with file-lock concurrency. Wails desktop client with system tray, sync engine, and file browser. The API embeds the built SPA via `//go:embed` so it ships as a single binary/container.

## Commands

```sh
make install          # install tooling (wails, air) + deps for api + web + desktop
make dev              # run api (air hot-reload) + web (vite) side by side
make build            # build api binary + web dist + desktop binary
make run              # docker compose up (mist-drive + minio)
make test             # all api tests (unit + integration)
make test-unit        # go test ./... (in api/)
make test-integration # go test -tags=integration (requires docker)
make desktop-dev      # wails dev with webkit2_41 tag
make desktop-build    # wails build with webkit2_41 tag
```

Single test: `cd api && go test ./internal/httpx/ -run TestName`
Integration tests: `cd api && go test -tags=integration -timeout=300s ./internal/httpx/ -run TestName`
Wails bindings regen: `cd desktop && wails generate module`

## Architecture

### API (`api/`)

- **Entrypoint**: `cmd/server/main.go` — boots config, stores, S3 client, bootstraps admin user, starts upload GC goroutine, mounts routes + embedded SPA.
- **`internal/httpx/`** — HTTP layer. `Server` struct holds all deps. Route registration in `handlers.go`, handlers split by concern: `handlers_auth.go`, `handlers_files.go`, `handlers_upload.go`, `handlers_admin.go`, `handlers_ws.go`. `middleware.go` has JWT auth + admin guard.
- **`internal/users/`** — JSON-file-backed user store with in-memory index + `flock` for disk writes. No database.
- **`internal/uploads/`** — Multipart upload state persistence (also JSON files). `gc.go` reclaims stale uploads.
- **`internal/s3x/`** — MinIO/S3 client wrapper (presigned URLs, bucket ops).
- **`internal/config/`** — All config from env vars. Required: `JWT_SECRET`, `ADMIN_PASSWORD`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`.
- **`internal/quota/`** — In-memory upload quota reservations.
- **`internal/events/`** — WebSocket event hub for push notifications (`files-changed`).
- **`internal/webui/`** — `//go:embed` of `web/dist/` into Go binary. Must be called AFTER route registration (fall-through handler).

### Web (`web/`)

- Vite + Bun + React + TypeScript. No component library.
- `src/lib/api.ts` — typed API client, session management, reconnecting WebSocket client.
- `src/lib/uploader.ts` — multipart upload with presigned URLs, 8 MiB parts, 4 concurrent PUT workers, abort support.
- Pages: `Login.tsx`, `Files.tsx`, `Admin.tsx`.

### Desktop (`desktop/`)

- Wails app (Go + web frontend). Packages: `apiclient`, `sync`, `tray`, `settings`, `wsclient`, `desktopentry`.
- Build tag `webkit2_41` required on Ubuntu 24.04+.
- `app.go` — Wails-bound backend. All exported methods callable from frontend. Handles auth, file ops, sync folder management, settings, window lifecycle.
- **Sync engine** (`internal/sync/engine.go`) — fsnotify-driven reconcile loop. Per-folder upload/download toggles. Keeps a 200-entry log ring buffer for the history modal. `SetAPI()` / `ClearStatus()` for clean re-auth after token expiry.
- **Settings** (`internal/settings/`) — JSON on disk, multi-environment (per API URL). Global flags: `startOnLaunch`, `closeToTray` (default true — window hides to tray on close, quit via tray menu).
- **SSO handoff** — "Open Web" passes JWT via URL fragment (`#token=`), not query param. Web app consumes + scrubs immediately.

### Deploy (`deploy/`)

- `docker-compose.yml` brings up mist-drive + minio. Expects `.env` file (copy from `.env.example`).
- Designed to sit behind an external reverse proxy that terminates TLS.

## Key patterns

- **Uploads**: browser → API (init, get presigned URLs) → browser PUTs parts direct to MinIO → API (complete). Quota reserved on init, released on complete/abort/GC.
- **WebSocket push**: server publishes `files-changed` after mutations; SPA re-fetches file list (no deltas).
- **Embedded SPA**: `webui.Mount()` must come after `srv.Register()` — Fiber matches in registration order, API routes take precedence.
- **Integration tests**: use `testcontainers-go` to spin up real MinIO. Build tag `integration` required.
- **Desktop login flow**: `Login()` must bounce the sync engine (`Stop` → `SetAPI` → `ClearStatus` → `Start`) so it picks up the fresh token and clears stale errors.

## Roadmap

### Quick wins
- **Rename files/folders** — no endpoint yet, users must delete + re-upload
- **Create folder** — web UI only creates folders implicitly via upload path
- **"Remember me" on web** — session uses `sessionStorage`, lost on tab close

### Medium effort
- **File preview** — inline images/PDFs/text via presigned URL modal instead of always downloading
- **Drag & drop upload** — multipart uploader exists, just needs a drop zone on web
- **Search/filter** — search bar over file keys for large buckets

### Larger features
- **Share links** — time-limited presigned URLs for files without requiring login
- **Desktop notifications** — surface sync engine events (upload complete, errors) as OS notifications
- **2FA / TOTP** — auth hardening beyond password-only JWT flow
