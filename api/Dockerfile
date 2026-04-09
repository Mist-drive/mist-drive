# syntax=docker/dockerfile:1
#
# Unified mist-drive image: builds the Vite SPA, embeds it into the Go
# binary via //go:embed, and ships the whole stack as a single process.
# There is no separate web container — Fiber serves / from the embedded
# dist/ and /api, /auth, /health from Go handlers.
#
# Build context MUST be the repo root (the directory that contains
# both ./api and ./web), because stage 1 needs to pull web/ too:
#   docker build -f api/Dockerfile -t mist-drive .

# ── stage 1: build the Vite frontend with Bun ─────────────────────────
FROM oven/bun:1.2-alpine AS web
WORKDIR /src/web
# Lockfile first so `bun install` caches across source-only changes.
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build
# Result lives at /src/web/dist

# ── stage 2: build the Go API with the SPA embedded ───────────────────
FROM golang:1.26-alpine AS builder
WORKDIR /src
# Deps first (cached as long as go.mod/go.sum are unchanged). We do
# NOT run `go mod tidy` here — it would drag in test-only modules on
# every production build.
COPY api/go.mod api/go.sum ./
RUN go mod download
COPY api/ ./
# Overwrite the committed placeholder dist/ with the real build output
# from stage 1. //go:embed picks it up at compile time.
COPY --from=web /src/web/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /out/mist-drive ./cmd/server

# ── stage 3: minimal runtime image ────────────────────────────────────
FROM alpine:3.21
RUN adduser -D -u 1000 appuser \
  && apk add --no-cache curl ca-certificates tzdata

WORKDIR /app
RUN mkdir -p /data /app/logs && chown -R appuser:appuser /app /data

COPY --from=builder /out/mist-drive /app/mist-drive

USER appuser
EXPOSE 3000
ENTRYPOINT ["/app/mist-drive"]
