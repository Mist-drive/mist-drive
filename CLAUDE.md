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
- **`internal/httpx/`** — HTTP layer. `Server` struct holds all deps. Route registration in `handlers.go`, handlers split by concern: `handlers_auth.go`, `handlers_files.go`, `handlers_upload.go`, `handlers_admin.go`, `handlers_ws.go`, `handlers_totp.go`, `handlers_devices.go`. `middleware.go` has JWT auth + admin guard.
- **`internal/users/`** — JSON-file-backed user store with in-memory index + `flock` for disk writes. No database.
- **`internal/uploads/`** — Multipart upload state persistence (also JSON files). `gc.go` reclaims stale uploads.
- **`internal/s3x/`** — MinIO/S3 client wrapper (presigned URLs, bucket ops).
- **`internal/config/`** — All config from env vars. Required: `JWT_SECRET`, `ADMIN_PASSWORD`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`. Defaults: `DATA_DIR=./data`, `LOG_PATH=./logs/app.log`.
- **`internal/quota/`** — In-memory upload quota reservations. `disk.go` has `DiskFree(path)` via `syscall.Statfs`.
- **`internal/events/`** — WebSocket event hub for push notifications (`files-changed`).
- **`internal/webui/`** — `//go:embed` of `web/dist/` into Go binary. Must be called AFTER route registration (fall-through handler).
- **`internal/features/`** — CE/Pro feature flags. `features.go` defines `Features` struct + `Current()`. `ce.go` (`//go:build !pro`) sets all flags false. `pro.go` (`//go:build pro`, private repo only) sets all flags true.
- **`/health`** — returns `{"ok": true, "version": "...", "features": {...}}`. Version injected at build time via `-ldflags`; defaults to `"dev"`. Features computed at compile time via build tag.

### Web (`web/`)

- Vite + Bun + React + TypeScript. No component library.
- `src/lib/api.ts` — typed API client, session management, reconnecting WebSocket client. `fetchHealth()` hits `/health` unauthenticated and returns `{version, features}`. 401 responses auto-redirect to `/login` after clearing the session.
- `src/lib/uploader.ts` — multipart upload with presigned URLs, 8 MiB parts, 4 concurrent PUT workers, abort support.
- `src/i18n.ts` — i18next init with HTTP backend; loads `/locales/{{lng}}.json` at runtime (not bundled). `web/public/locales` is a symlink → `../../shared/locales`; Vite copies it into `dist/locales/` at build time so the Go embed picks it up.
- Pages: `Login.tsx`, `Files.tsx`, `Admin.tsx`, `Settings.tsx`.

### Desktop (`desktop/`)

- Wails app (Go + web frontend). Packages: `apiclient`, `sync`, `tray`, `settings`, `wsclient`, `desktopentry`.
- Build tag `webkit2_41` required on Ubuntu 24.04+.
- `app.go` — Wails-bound backend. All exported methods callable from frontend. Handles auth, file ops, sync folder management, settings, window lifecycle.
  - `PickFile` returns `LocalFile{Key, Size}` — size used by frontend for diff conflict detection.
  - `PickFolderForUpload` returns `[]LocalFile` with sizes.
  - `UploadFolderPicked(skipKeys []string)` — skip list for diff uploads.
  - `CancelUpload(key)` / `CancelUploads()` — per-file and batch cancel via `context.Context`.
  - `GetFeatures()` — returns `apiclient.Features` fetched from server `/health` after login/Me. Desktop 401 responses trigger session expiry via `session.ts` pub/sub → `setUser(null)` in `App.tsx`.
- **Sync engine** (`internal/sync/engine.go`) — fsnotify-driven reconcile loop. Per-folder upload/download toggles. Keeps a 200-entry log ring buffer for the history modal. `SetAPI()` / `ClearStatus()` for clean re-auth after token expiry.
- **Settings** (`internal/settings/`) — JSON on disk, multi-environment (per API URL). Global flags: `startOnLaunch`, `closeToTray` (default true — window hides to tray on close, quit via tray menu).
- **SSO handoff** — "Open Web" passes JWT via URL fragment (`#token=`), not query param. Web app consumes + scrubs immediately.

### Deploy (`deploy/`)

- `docker-compose.yml` brings up mist-drive + minio. Expects `.env` file (copy from `.env.example`).
- Designed to sit behind an external reverse proxy that terminates TLS.

## Key patterns

- **Uploads**: browser → API (init, get presigned URLs) → browser PUTs parts direct to MinIO → API (complete). Quota reserved on init, released on complete/abort/GC.
- **Upload quota on replace**: `uploadInit` deducts the existing file size from `usedBytes` before the quota check so replacing a large file with a slightly larger one is allowed. `uploadComplete` adds only the net delta `(newSize - oldSize)` to `usedBytes`.
- **WebSocket push**: server publishes `files-changed` after mutations; SPA re-fetches file list (no deltas).
- **Embedded SPA**: `webui.Mount()` must come after `srv.Register()` — Fiber matches in registration order, API routes take precedence.
- **Integration tests**: use `testcontainers-go` to spin up real MinIO. Build tag `integration` required.
- **Desktop login flow**: `Login()` must bounce the sync engine (`Stop` → `SetAPI` → `ClearStatus` → `Start`) so it picks up the fresh token and clears stale errors.
- **Rename disk check**: `renameFile` calls `quota.DiskFree(cfg.DataDir)` before spawning the copy goroutine; rejects with `507` if `copySize + 1 GiB > free`. Rename is copy+delete (no S3 atomic rename), so it needs ~2× file size free temporarily.
- **Shared code** (`shared/`): components and libs used by both web and desktop via `@shared` Vite/TS alias. Key exports: `LoadingBar`, `LoginCard`, `Logo`, `UploadCard`, `UploadProgressPanel`, `ReplaceDialog`, `PreviewContent`, `StorageStats`; libs: `format`, `tree`, `upload`, `loading`, `i18n`.
- **i18n** (`shared/locales/en.json`): single source of truth for all UI strings. Web lazy-loads via HTTP (`/locales/en.json`); desktop bundles statically via `import en from '@shared/locales/en.json'`. Both Vite configs set `resolve.dedupe: ['i18next', 'react-i18next']` to prevent dual-instance issues when resolving through the `@shared` alias. `shared/lib/i18n.ts` re-exports `useTranslation`/`Trans` — shared components always import from there, never directly from `react-i18next`. Run `python3 check_i18n.py` from repo root to audit missing/unused keys.
- **Data dirs**: `DATA_DIR` defaults to `./data` (relative to where the binary runs). In dev (`make api-dev`), `api/.env` sets `DATA_DIR=../data/api` so data lands in `data/api/` at the repo root, not inside `api/`. Docker uses the default `./data` which resolves to `/app/data` inside the container (WORKDIR `/app`). `LOG_PATH` follows the same pattern.
- **`api-dev` env**: `DATA_DIR` and `LOG_PATH` are written into `api/.env` on first run by the Makefile and sourced via `set -a && . ./.env`. The `.air.toml` `[env]` section is unreliable across air versions — env vars go in `.env` instead.
- **Trusted device tokens**: cookie value is `{uuid}:{32-byte-hex}`; server stores `SHA-256({32-byte-hex})` — never the plain token. Lookup: split on `:`, find device by UUID, hash the token half, compare. `pruneExpiredDevices` called on every registration to keep the slice clean. Cookie is httpOnly + SameSite=Strict, 30-day MaxAge.
- **Login history**: `User.LoginHistory []LoginRecord` — up to 10 most-recent successful logins (newest first), each storing IP + User-Agent + timestamp. Appended in `login()` after token is issued; `AppendLoginRecord` prepends and trims to 10. `GET /api/login-history` returns it read-only. Backup-code removal is persisted with an immediate `Users.Update` before the final write so a crash can't allow replay.
- **Real client IP**: `fiber.Config{ProxyHeader: fiber.HeaderXForwardedFor}` in `main.go` so `c.IP()` reads from Traefik's `X-Forwarded-For` header. **Known limitation**: Traefik appends but doesn't strip client-supplied XFF by default, so a crafty client can spoof the first value. For the current use (security logs + login history display) this is cosmetic. To harden: configure `forwardedHeaders.trustedIPs` in Traefik so it replaces rather than appends.
- **Desktop trusted device cookie**: `apiclient.Client` stores `deviceCookie` and sends it on every request via `do()`. The `Login` method builds its own raw HTTP request (not via `do()`) so it must set the `Cookie` header explicitly. On login, `app.go` seeds the fresh `cli` with the stored cookie from settings before calling `Login`.

## Roadmap

### Quick wins
- **WebSocket first-message auth** — WS currently connects as `/api/ws?token=<JWT>`; token appears in server access logs. Fix: connect without token, client sends `{"type":"auth","token":"..."}` as first message, server validates within a timeout (e.g. 10s) before accepting further messages. Requires bypassing JWT middleware on the WS route and handling auth inside `wsHandler`.

### Medium effort
- **Drag & drop upload (desktop)** — web is done; desktop needs `runtime.OnFileDrop` + Go recursive walker + `UploadLocalPaths(paths, prefix)` binding. See conversation notes for design.

### Larger features
- **Share links** — time-limited presigned URLs for files without requiring login
- **Desktop notifications** — surface sync engine events (upload complete, errors) as OS notifications

### Done
- ~~**Login history**~~ — `User.LoginHistory []LoginRecord` (IP + User-Agent + timestamp, last 10, newest first); recorded on every successful login; `GET /api/login-history` endpoint; web Settings page shows it read-only; `AppendLoginRecord` truncates UA at 120 runes
- ~~**2FA / TOTP**~~ — `handlers_totp.go`: setup/enable/disable/regen-backup endpoints; `verifyTOTP` checks live code + bcrypt-hashed backup codes (one-time use, consumed on verify); login two-step flow: password-only first call returns `{totp_required: true}`, second call sends `totpCode`; disabling clears secret + backup codes + trusted devices; web: Settings page with QR scan, confirm, backup codes display, disable flow
- ~~**Trusted devices (remember this device 30 days)**~~ — cookie `mist_device={id}:{plainToken}`; server stores SHA-256 hash in `user.TrustedDevices[]`; valid cookie skips TOTP on login entirely; "Don't ask again on this device for 30 days" checkbox shown on TOTP step; Settings page lists active devices (label from User-Agent, expiry date) with per-device and revoke-all buttons; expired devices pruned on registration; devices wiped when TOTP is disabled; `GET /api/devices`, `DELETE /api/devices`, `DELETE /api/devices/:id`
- ~~**File preview**~~ — `GET /api/files/preview?key=`; images resized to 800px JPEG (72% quality), text first 4KB, binary placeholder; web: right-side sliding panel; desktop: modal popup; `X-Preview-Type` response header drives rendering in shared `PreviewContent` component
- ~~**Create folder**~~ — `.keep` marker file in S3; filtered in `buildTree`, API returns all objects (web + desktop)
- ~~**"Remember me" on web**~~ — always localStorage; checkbox persists across logout; desktop mirrors via settings JSON
- ~~**Search/filter**~~ — client-side search bar over file keys (web + desktop)
- ~~**Replace file warning**~~ — `ReplaceDialog` with end-truncated paths, collapsible list, keyboard support; **Diff button** skips conflicts where local and remote size match (web + desktop)
- ~~**Drag & drop upload (web)**~~ — files + folders onto tree; folder highlight + auto-expand on hover; root drop zone; shared upload pipeline with conflict detection
- ~~**Rename files/folders**~~ — async copy+delete in S3; processing guard prevents concurrent ops; disk space pre-check (`507` if `copySize + 1 GiB > free`); rename error pushed via WebSocket
- ~~**Upload card (web + desktop)**~~ — shared `UploadCard` + `UploadProgressPanel`; active/queued/done/ETA stats; per-file and cancel-all buttons; desktop cancel via `context.Context` threading through Go → MinIO PUT
- ~~**Quota fix for replace**~~ — init deducts existing file size; complete applies net delta only
- ~~**Shared UI**~~ — `shared/` directory with components + libs aliased as `@shared`; covers `LoadingBar`, `LoginCard`, `Logo` (with version), `ReplaceDialog`, `UploadCard`, `PreviewContent`, `StorageStats`, and libs `format`, `tree`, `upload`, `loading`
- ~~**Harmonized login**~~ — shared `LoginCard` with logo + version; web fetches version from `/health`; desktop from `GetVersion()`; server field desktop-only; sign-in button disabled until login + password filled
- ~~**Version in UI**~~ — `/health` exposes `version`; shown in navbar (web) and header bar (desktop); both login pages display it below the logo
- ~~**CE/Pro feature flags**~~ — `api/internal/features/` package; `ce.go` (`!pro` tag) ships in public repo with all flags false; `pro.go` (`pro` tag) lives in private repo only; `/health` returns `features` object; web reads via `fetchHealth()`, desktop via `GetFeatures()` Wails binding
- ~~**401 auto-redirect**~~ — web: `req()` and `previewFile()` detect 401 → `clearSession()` + `window.location.replace('/login')`; desktop: `session.ts` pub/sub, `is401()` helper, `notifySessionExpired()` called in `Files.tsx`/`Home.tsx` catch blocks
- ~~**i18n**~~ — i18next + react-i18next; `shared/locales/en.json` single source of truth; web lazy-loads via HTTP backend (not bundled); desktop bundles via static JSON import; `resolve.dedupe` in both Vite configs prevents dual-instance `NO_I18NEXT_INSTANCE` error in production builds
