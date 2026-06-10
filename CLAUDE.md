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
- **`internal/httpx/`** — HTTP layer. `Server` struct holds all deps. Route registration in `handlers.go`, handlers split by concern: `handlers_auth.go`, `handlers_files.go`, `handlers_upload.go`, `handlers_admin.go`, `handlers_ws.go`, `handlers_totp.go`, `handlers_devices.go`. `middleware.go` has JWT auth + admin guard + token-version check.
- **`internal/users/`** — JSON-file-backed user store with in-memory index + `flock` for disk writes. No database.
- **`internal/uploads/`** — Multipart upload state persistence (also JSON files). `gc.go` reclaims stale uploads.
- **`internal/s3x/`** — MinIO/S3 client wrapper (presigned URLs, bucket ops).
- **`internal/config/`** — All config from env vars. Required: `JWT_SECRET`, `ADMIN_PASSWORD`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`. Defaults: `DATA_DIR=./data`, `LOG_PATH=./logs/app.log`, `MAX_ZIP_BYTES=20GiB`, `ZIP_STREAM_TIMEOUT=30m` (Go duration; max wall-time for one folder-zip stream — the download ticket TTL only gates the start, this bounds the whole transfer). Optional SMTP block: `SMTP_HOST/PORT/USER/PASSWORD/FROM/TLS` — all empty by default (email disabled).
- **`internal/notify/`** — `Mailer` wrapping `go-mail`. `Enabled()` guards all sends. `SendNewIP` / `SendFailedLogin`. No-op when `SMTP_HOST` is empty.
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
- **Sync engine** (`internal/sync/engine.go`) — fsnotify-driven reconcile loop. Per-folder upload/download toggles. Keeps a 200-entry log ring buffer for the history modal. `SetAPI()` / `ClearStatus()` for clean re-auth after token expiry. `download()` guards against path traversal via `safeLocalPath(base, rel)` (uses `filepath.Rel`) — server-supplied object keys containing `../` can't write outside the sync folder.
- **Settings** (`internal/settings/`) — JSON on disk, multi-environment (per API URL). Global flags: `startOnLaunch`, `closeToTray` (default true — window hides to tray on close, quit via tray menu).
- **SSO handoff** — "Open Web" passes JWT via URL fragment (`#token=`), not query param. Web app consumes + scrubs immediately.

### Deploy (`deploy/`)

- `docker-compose.yml` brings up mist-drive + minio. Expects `.env` file (copy from `.env.example`).
- Designed to sit behind an external reverse proxy that terminates TLS.

## Key patterns

- **Uploads**: browser → API (init, get presigned URLs) → browser PUTs parts direct to MinIO → API (complete). Quota reserved on init, released on complete/abort/GC.
- **Upload quota on replace**: `uploadInit` deducts the existing file size from `usedBytes` before the quota check so replacing a large file with a slightly larger one is allowed. `uploadComplete` adds only the net delta `(newSize - oldSize)` to `usedBytes`.
- **WebSocket push**: server publishes `files-changed` after mutations; SPA re-fetches file list (no deltas). Route is top-level `GET /ws` (outside `/api`, so the JWT `AuthMiddleware` doesn't run). The handshake carries no credentials — the client sends `{"type":"auth","token":"<jwt>"}` as its first frame; `authenticateWS` (in `handlers_ws.go`) validates it within a 10s read deadline (`auth.Parse` + boot-time + token-version checks, mirroring `AuthMiddleware`), then clears the deadline and subscribes to the hub. Web (`api.ts` `ensureWS`) sends the auth frame in `onopen`; desktop (`wsclient`) sends it right after `Dial`. Dev needs a `/ws` Vite proxy entry with `ws:true`.
- **No JWT in URLs**: `AuthMiddleware` is header-only (`Authorization: Bearer`). The old `?token=` query fallback was retired once its two consumers moved off it — WS → first-message auth (above), folder-zip download → single-use ticket ([[project-login-throttle]] sibling pattern in `dlticket.go`). A JWT in `?token=` now 401s (regression-guarded by `TestAuthMiddleware_QueryParamRejected` + `TestIntegration_DownloadZipQueryTokenRejected`). The real WS handshake + push is covered by `TestIntegration_WSFirstMessageAuth` (serves the app on a loopback listener, dials a real ws client, asserts valid-token→push and bogus-token→close) — `app.Test` can't do ws upgrades, so this one needs the listener. All under `make test-integration` (testcontainers MinIO + a programmatic no-TOTP fixture user — no running stack or seeded account needed). Both `/ws` and `/download-zip` are top-level routes precisely because `/api/*` always runs the auth middleware.
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
- **Real client IP**: `fiber.Config{ProxyHeader: fiber.HeaderXForwardedFor}` in `main.go` so `c.IP()` reads from Traefik's `X-Forwarded-For` header. Now load-bearing (not cosmetic): the per-IP login throttle keys off it. **Hardened in the stack** (`cy17-single-node-services-stack/docker-compose.yml`): both Traefik entrypoints set `forwardedHeaders.trustedIPs` to loopback + RFC1918, so Traefik (the edge) overwrites client-supplied XFF for public clients instead of appending → first value can't be spoofed. **⚠️ VERIFY ON NEXT DEPLOY**: Traefik must be *recreated* (static config), then `curl -H "X-Forwarded-For: 1.2.3.4" https://drive.<domain>/auth/login -d '{"login":"nope","password":"nope"}' -H 'Content-Type: application/json'` and confirm the `auth: unknown user` log shows the real IP, not `1.2.3.4`. If a CDN/LB is ever added in front, append its egress CIDRs to both `trustedIPs` lists. In local dev XFF is absent → `clientIP()` helper falls back to `c.Context().RemoteAddr()` TCP address, then `"unknown"`.
- **Login throttle / lockout** (`internal/httpx/throttle.go`): in-memory `loginThrottle`, two independent dimensions, login locked if *either* trips. `login:<name>` threshold 5 (targeted brute-force; includes unknown logins). `ip:<addr>` threshold 20 (password-spray; higher for shared NATs; only when `clientIP` is concrete — `usableIP` skips `""`/`"unknown"`). Lock 15min, entries lazily pruned after 1h idle TTL (bounds the map — replaced the old unbounded `sync.Map` failed-login counter). Both wrong-password and wrong-TOTP count as failures. Success resets only the per-login counter (per-IP ages out via TTL so one valid cred in a spray can't reset the IP budget). Returns `429 + Retry-After`. Wired via `Server.loginLocked/loginFail/loginSucceeded`; `loginGuard()` lazily inits so a bare `Server{}` in tests works. **Caveats**: in-memory + per-process (multi-replica multiplies effective threshold — needs shared store like Redis to fix); resets on restart; no admin-unlock endpoint.
- **Login timing equalizer**: unknown-user login path calls `auth.DummyVerify(pw)` (bcrypt vs a throwaway hash computed once) so its latency matches the wrong-password path — no timing oracle for username enumeration.
- **TOTP-enable is password-gated**: `POST /api/totp/enable` requires `password` (re-auth) so a leaked session token alone can't enrol an attacker-controlled 2FA secret and lock the user out. Web Settings sends it via a password field on the QR-confirm step.
- **Device cookie hygiene**: `validateDeviceCookie` compares the hashed token with `subtle.ConstantTimeCompare`. Revoking devices clears the browser's `mist_device` cookie via `expireDeviceCookie` — `revokeAllDevices` always; `revokeDevice` only when the revoked id matches `currentDeviceID(c)` (the device you're sitting on).
- **Email uniqueness**: `users.Store.EmailTaken(email, exceptID)` — case-insensitive (`strings.EqualFold`), empty never taken, `exceptID` lets a user re-save their own email. Enforced (`409 "email already in use"`) in `updateEmail` (passes `u.ID`) and `adminCreateUser` (passes `""`). Check-then-write, not atomic (TOCTOU window negligible; email is a notify target, not a credential).
- **Login validation** (`adminCreateUser`): `validLogin` allows email-style logins — letters/digits/`. - _ @ +`, length 3-64. Login is only an in-memory index key (user files are named by UUID), so this is input sanity, not path safety. Password min length 8 (`minPasswordLen`).
- **Usage accounting helpers** (`handlers_files.go`): `sumObjectSizes(objs)` and `recountUsedBytes(ctx, uid, bucket)` (authoritative full-listing recount → `SetUsedBytes`) centralise the size loops previously duplicated across delete/recompute/zip/rename.
- **`isNewIP` behavior**: returns `false` when login history is empty (first login is not an alert condition). Returns `true` only when the IP doesn't match any existing `LoginRecord`. Always evaluated before `AppendLoginRecord` so the current IP isn't in history yet.
- **Desktop trusted device cookie**: `apiclient.Client` stores `deviceCookie` and sends it on every request via `do()`. The `Login` method builds its own raw HTTP request (not via `do()`) so it must set the `Cookie` header explicitly. On login, `app.go` seeds the fresh `cli` with the stored cookie from settings before calling `Login`.
- **JWT token versioning**: `User.TokenVersion int64` incremented by `POST /api/me/logout-all`. JWT claims carry `ver int64`; `TokenVersionMiddleware` (wired after `AuthMiddleware` on all authenticated routes) rejects tokens where `claims.ver < user.TokenVersion`. Users store is in-memory so the per-request lookup is cheap. Existing tokens have `ver=0`, `TokenVersion` defaults to `0` — no forced re-login on deploy.
- **Email notifications**: new-IP login → `SendNewIP` to the affected user's `Email` field; wrong-password × 3 → `SendFailedLogin` to admin's `Email` field (paced by the per-login failure count returned from `loginFail`, `count%3`). Both fire in goroutines (non-blocking). `make api-dev` starts Mailpit on port 1025 (UI at http://localhost:8025) and seeds `api/.env` with `SMTP_HOST=localhost SMTP_PORT=1025 SMTP_TLS=none`.
- **Change password**: `PUT /api/me/password` — verifies current password (bcrypt) + TOTP if enabled (uses existing `verifyTOTP`), then hashes and saves the new password.
- **Per-user email**: `User.Email string` + `PublicUser.Email`. Set via `PUT /api/me/email`. Used as destination for new-IP notifications. Admin email used for failed-login alerts.
- **Desktop OS notifications** (`desktop/`): the sync engine stays pure — it calls an injected `notify func(title, body string)` callback (set via `Engine.SetNotifier`, nil = off). `app.go` provides the impl using `github.com/gen2brain/beeep` (cross-platform; dbus/notify-send on Linux), gated by the per-user `Notifications` setting (read fresh each fire, so toggling is instant). Coalescing: at most one activity notification per reconcile pass (`syncSummary` at the end of `reconcileAll`, only when files moved — idle passes are silent; a single file is named by basename e.g. "Uploaded photo.jpg", multiples collapse to "Uploaded photo.jpg +N more" / "Synced N files (↑X ↓Y)" via the per-pass `passUp`/`passDown` basename slices) plus one error notification, deduped by `lastErrKey` so a steady-state failure doesn't fire every 30s (`setErr` for fatal pass errors, per-file `recordErr` rolls into the end-of-pass count). `Notifications *bool` in `settings.diskFormat` (nil = default true), surfaced as a toggle in `Sync.tsx`.

## Roadmap

### Medium effort
- **Drag & drop upload (desktop)** — web is done; desktop needs `runtime.OnFileDrop` + Go recursive walker + `UploadLocalPaths(paths, prefix)` binding. See conversation notes for design.

### Larger features
- **Share links** — time-limited presigned URLs for files without requiring login

### Done
- ~~**Desktop OS notifications**~~ — sync engine fires native notifications via `beeep` (gated by a per-user `Notifications` toggle): one coalesced "↑N ↓M" per pass + deduped error notifications; idle passes silent. See the "Desktop OS notifications" pattern above.
- ~~**JWT out of all URLs**~~ — retired the `?token=` query-param auth fallback (it landed in access logs). Folder-zip download now uses a single-use 60s ticket (`dlticket.go`; mint at `POST /api/files/download-zip-ticket`, stream at top-level `GET /download-zip?ticket=`); WebSocket now uses first-message auth (top-level `GET /ws`, client sends `{"type":"auth","token":…}`, validated in `authenticateWS` within a 10s deadline). `AuthMiddleware` is header-only; query token → 401 (regression-tested). Configurable `ZIP_STREAM_TIMEOUT` (default 30m) bounds the zip stream wall-time. Web + desktop + Vite proxy (`/download-zip`, `/ws` with `ws:true`) updated.
- ~~**Auth hardening batch**~~ — login lockout throttle (per-login + per-IP, `429 + Retry-After`); unknown-user timing equalizer (`auth.DummyVerify`); TOTP-enable password-gated; trusted-device cookie cleared on revoke + constant-time token compare; per-user notification email uniqueness (`409`); `validLogin` allows email-style logins (3-64, `. - _ @ +`) + password min 8; desktop sync download path-traversal guard (`safeLocalPath`); `sumObjectSizes`/`recountUsedBytes` dedupe. Traefik `forwardedHeaders.trustedIPs` set in the stack so the per-IP throttle reads a non-spoofable client IP — **⚠️ verify on next deploy** (see "Real client IP" above).
- ~~**Email notifications + JWT revocation + password change**~~ — new-IP login → email to user; 3 failed attempts → email to admin; `POST /api/me/logout-all` increments `TokenVersion` invalidating all sessions (TOTP-gated); `PUT /api/me/password` (current pwd + TOTP if enabled); `PUT /api/me/email`; Mailpit auto-started in `make api-dev` (UI :8025, SMTP :1025); production uses external SMTP via env vars
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

## MinIO KMS / Encryption — Options Under Consideration

Three approaches discussed for protecting MinIO data at rest. No decision made yet.

- **Option A — S3 Server-Side Encryption (SSE-S3 / SSE-KMS)**: MinIO native encryption via `MINIO_KMS_SECRET_KEY` (or external KMS like Vault/KES). Key passed as env var or secret. Transparent to mist-drive — no application changes. Downside: key and MinIO process on the same host limits protection against full server compromise.
- **Option B — 1Password-style account key**: A second secret derived client-side is mixed into encryption so that the server secret alone cannot decrypt. Would require an application-level encryption layer in mist-drive before uploading to MinIO. Strong separation (server breach is not enough) but significant complexity and hard key-recovery UX.
- **Option C — Docker Swarm Secrets**: Deploy `cy17-single-node-services-stack` via `docker stack deploy`. `MINIO_KMS_SECRET_KEY_FILE` points to `/run/secrets/minio_kms_key`; secret created once with `docker secret create`, never written to disk in plaintext. Secret not visible in `docker inspect` or `.env`. Requires Swarm mode even on a single node.
