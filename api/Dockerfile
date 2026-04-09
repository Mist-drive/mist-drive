# Build stage
FROM golang:1.26-alpine AS builder
WORKDIR /src
# Download deps first (cached as long as go.mod/go.sum don't change).
# We deliberately do NOT run `go mod tidy` here: it would pull test-only
# modules (testcontainers-go, etc.) on every production build.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /out/api ./cmd/server

# Runtime stage
FROM alpine:3.21
RUN adduser -D -u 1000 appuser \
  && apk add --no-cache curl ca-certificates tzdata

WORKDIR /app
RUN mkdir -p /data /app/logs && chown -R appuser:appuser /app /data

COPY --from=builder /out/api /app/api

USER appuser
EXPOSE 3000
ENTRYPOINT ["/app/api"]
