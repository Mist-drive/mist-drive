# Deploy with Traefik

Self-contained production deployment: Traefik v3 (HTTPS + Let's Encrypt) + mist-drive + MinIO.

## Prerequisites

- VPS with Docker installed
- Ports **80** and **443** open
- Two DNS **A records** pointing to the server IP:
  - `drive.yourdomain.com` — the app
  - `s3.yourdomain.com` — MinIO S3 (presigned uploads go here)

## Quick start

```sh
cp .env.example .env
# Edit .env — fill in DOMAIN, ACME_EMAIL, JWT_SECRET, ADMIN_PASSWORD, MINIO_ROOT_PASSWORD
docker compose up -d
```

The first startup requests a Let's Encrypt certificate automatically. Logs:

```sh
docker compose logs -f
```

## Subdomains

| Subdomain | Service |
|---|---|
| `drive.${DOMAIN}` | mist-drive web app + API |
| `s3.${DOMAIN}` | MinIO S3 endpoint (browser uploads) |

## Compression tuning

Background re-compression of ZIPs and JPEGs runs with conservative defaults (1 worker, level 9, JPEG quality 90). Uncomment and adjust the `COMPRESS_*` variables in `docker-compose.yml` if you have a multi-core VPS and want faster processing.
