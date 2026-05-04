# Mist Drive

Self-hosted drive. Go Fiber API + Vite/React SPA + MinIO object storage. No database — per-user JSON files on disk. Optional Wails desktop client with system tray and sync engine.

## Stack

- `api/` — Go Fiber. Auth, multipart uploads via presigned S3 URLs, admin, WebSocket push. Embeds the built SPA as a single binary.
- `web/` — Vite + Bun + React. No component library.
- `shared/` — shared components and libs (`@shared` alias) used by both web and desktop frontend.
- `desktop/` — Wails (Go + React). System tray, sync engine, file browser.

## Dev

```sh
make install       # wails, air, go mod, bun install
make api-dev       # air hot-reload API on :3000 + starts MinIO via docker compose
make ui-dev        # Vite dev server on :5173 (proxies /api/* to :3000)
make desktop-dev   # wails dev (webkit2_41 tag required on Ubuntu 24.04+)
```

`make api-dev` auto-creates `api/.env` with a generated `JWT_SECRET` and `ADMIN_PASSWORD=admin` if missing.

## Build & run

```sh
make build         # api binary + web dist + desktop binary
make run           # docker compose up (mist-drive on :8080 + minio on :9000)
make stop          # docker compose down
make clean         # remove all build artifacts and node_modules
```

`make run` expects a `.env` at the project root. Copy from `.env.example` and fill in secrets.

## API

```
GET    /health
POST   /auth/login

GET    /api/me
GET    /api/ws                          (WebSocket, JWT via ?token=)

GET    /api/files
DELETE /api/files
GET    /api/files/download
GET    /api/files/download-zip
GET    /api/files/preview
POST   /api/files/mkdir
POST   /api/files/rename
POST   /api/files/recompute-usage
POST   /api/files/upload/init
POST   /api/files/upload/complete
POST   /api/files/upload/abort

GET    /api/admin/users
POST   /api/admin/users
PATCH  /api/admin/users/:id/quota
DELETE /api/admin/users/:id
```

Uploads: browser → API (init, presigned URLs) → browser PUTs parts direct to MinIO → API (complete). Quota reserved on init, net delta applied on complete.

## Deploy

Designed to sit behind an existing reverse proxy that terminates TLS. Point `drive.<domain>` at `mist-drive:3000` and `s3.<domain>` at `minio:9000`.

```sh
cp .env.example .env   # fill in JWT_SECRET, ADMIN_PASSWORD, S3_DOMAIN
make run
```

Data is persisted in `data/` at the project root (shared between `make api-dev` and `make run`).

## CE / Pro

Feature flags are compile-time. CE build (`go build ./...`) ships with all flags false. Pro build (`go build -tags pro ./...`) requires the private `pro.go` file. `/health` returns the active feature set.
