# Mist Drive

Self-hosted drive. Go Fiber API + Vite/Bun/React web UI + MinIO storage. No database — per-user JSON files. Optional Traefik HTTPS gateway.

## Quick start (dev, local)

```sh
make install
cp deploy/.env.example deploy/.env   # then edit secrets
# optional for the tls profile: generate self-signed certs
(cd deploy/traefik && cat README.md)
make run            # with traefik https
# or
make run-notls      # bring your own gateway
```

## Layout

- `api/` — Go Fiber API (auth, users, multipart uploads, admin)
- `web/` — Vite + Bun + React SPA
- `deploy/` — docker-compose, traefik, env template
- `desktop/` — Wails client (TBD)

## Endpoints

- `POST /auth/login`
- `GET  /api/me`
- `GET  /api/files?prefix=`
- `POST /api/files/upload/init|complete|abort`
- `GET  /api/files/download?key=`
- `DELETE /api/files?key=`
- `GET/POST/PATCH/DELETE /api/admin/users[...]`

Uploads use direct-to-S3 multipart via presigned URLs. Reboot-safe (state persisted per upload).
