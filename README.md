# Mist Drive

[![CI](https://github.com/Mist-drive/mist-drive/actions/workflows/ci.yml/badge.svg)](https://github.com/Mist-drive/mist-drive/actions/workflows/ci.yml)
[![GitHub release](https://img.shields.io/github/v/release/Mist-drive/mist-drive)](https://github.com/Mist-drive/mist-drive/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Mist-drive/mist-drive?filename=api%2Fgo.mod)](api/go.mod)
[![License](https://img.shields.io/github/license/Mist-drive/mist-drive)](LICENSE)
[![Buy Me A Coffee](https://img.shields.io/badge/Buy%20Me%20A%20Coffee-support-orange?logo=buy-me-a-coffee&logoColor=white)](https://buymeacoffee.com/creativeyann17)

A self-hosted personal drive. Upload, organise, preview, and sync your files — on your own server, under your own rules.

## Features

- **Web app** — file browser with folder tree, drag & drop upload, preview (images, text), folder download as ZIP, search
- **Desktop client** — native app (Linux / Windows) with system tray, automatic sync engine, and upload from local folders
- **Multipart uploads** — large files split into 8 MiB parts uploaded in parallel directly to MinIO; resumable and cancellable
- **2FA / TOTP** — optional two-factor authentication with backup codes and trusted-device cookies (30 days)
- **Per-user quotas** — configurable storage limit per account, shown in real time
- **Background compression** — optional server-side re-compression of uploaded ZIPs and JPEGs (opt-in, off by default)
- **Email notifications** — new IP login alert and failed login alert via any SMTP relay (optional)
- **Admin panel** — create/delete users, set quotas, view storage usage
- **Single binary** — the API embeds the built SPA; one container to run, no separate web server

## Why mist-drive?

**Good fit if you:**
- Want a simple self-hosted alternative to Google Drive / Dropbox on a single VPS
- Prefer owning your data with no third-party cloud dependency
- Need a lightweight setup — no database, no Redis, no message broker
- Want a desktop sync client alongside the web app

**Not a good fit if you:**
- Need multi-user collaboration, shared folders, or link sharing (not implemented yet)
- Expect a polished consumer product — this is a developer-maintained open source project
- Run at scale — the single-process JSON store is designed for personal or small-team use

## Setup

See **[deploy/traefik/](deploy/traefik/README.md)** for the recommended production setup with Traefik and Let's Encrypt on a VPS.

For a quick local run:

```sh
cp .env.example .env   # fill in the required values
make run               # docker compose up: mist-drive on :8080 + minio on :9000
```

## Development

```sh
make install      # install tooling (wails, air) + deps for api, web, desktop
make dev-api      # hot-reload API on :3000 + starts MinIO and Mailpit via docker compose
make dev-ui       # Vite dev server on :5173 (proxies /api/* to :3000)
make dev-desktop  # Wails dev app (webkit2_41 tag required on Ubuntu 24.04+)
make build        # production build: api binary + web dist + desktop binary
make test         # full test suite (unit + integration)
```

`make dev-api` auto-creates `api/.env` with a generated `JWT_SECRET` on first run.

## Stack

- **API** — Go + [Fiber](https://gofiber.io). Single binary, embeds the built SPA.
- **Storage** — [MinIO](https://min.io) (S3-compatible). No database — user data stored as JSON files with file-lock concurrency.
- **Web** — Vite + Bun + React + TypeScript. No component library.
- **Desktop** — [Wails](https://wails.io) (Go + React). Shared frontend components with the web app.
