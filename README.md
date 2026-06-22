# Mist Drive

[![CI](https://github.com/creativeyann17/mist-drive/actions/workflows/ci.yml/badge.svg)](https://github.com/creativeyann17/mist-drive/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/creativeyann17/mist-drive)](https://github.com/creativeyann17/mist-drive/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/creativeyann17/mist-drive?filename=api%2Fgo.mod)](api/go.mod)
[![License](https://img.shields.io/github/license/creativeyann17/mist-drive)](LICENSE)
[![Buy Me A Coffee](https://img.shields.io/badge/Buy%20Me%20A%20Coffee-support-orange?logo=buy-me-a-coffee&logoColor=white)](https://buymeacoffee.com/creativeyann17)

Self-hosted drive. Go Fiber API + Vite/React SPA + MinIO object storage. No database — per-user JSON files on disk. Optional Wails desktop client with system tray and sync engine.

## Stack

- `api/` — Go Fiber. Auth, multipart uploads via presigned S3 URLs, admin, WebSocket push. Embeds the built SPA as a single binary.
- `web/` — Vite + Bun + React. No component library.
- `shared/` — shared components and libs (`@shared` alias) used by both web and desktop frontend.
- `desktop/` — Wails (Go + React). System tray, sync engine, file browser.

## Dev

```sh
make install       # wails, air, go mod, bun install
make dev-api       # air hot-reload API on :3000 + starts MinIO via docker compose
make dev-ui        # Vite dev server on :5173 (proxies /api/* to :3000)
make dev-desktop   # wails dev (webkit2_41 tag required on Ubuntu 24.04+)
```

`make dev-api` auto-creates `api/.env` with a generated `JWT_SECRET` and `ADMIN_PASSWORD=admin` if missing.

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

Data is persisted in `data/` at the project root (shared between `make dev-api` and `make run`).

## CE / Pro

Feature flags are compile-time. CE build (`go build ./...`) ships with all flags false. Pro build (`go build -tags pro ./...`) requires the private `pro.go` file. `/health` returns the active feature set.
