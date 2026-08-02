VERSION ?= dev

# Extra ldflags for the AppImage build only (e.g. CI:
# make appimage LDFLAGS="-s -w -X main.version=1.2.3 -X main.commit=abc1234").
LDFLAGS ?= -X main.version=$(VERSION)

.PHONY: help install install-data build dev-api dev-ui dev-desktop desktop-build appimage db-web run stop test test-unit test-integration fmt check install-hooks clean

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

install: ## install deps for api + web + desktop
	go install github.com/wailsapp/wails/v2/cmd/wails@latest
	go install github.com/air-verse/air@latest
	go install github.com/creativeyann17/go-docstore/cmd/godocstore@latest
	cd api && go mod download
	cd web && bun install
	cd desktop && go mod download && cd frontend && bun install

install-data:
	mkdir -p data/api data/minio data/api/logs

build: ## build api + web + desktop
	cd api && CGO_ENABLED=0 go build -ldflags "-X main.Version=$(VERSION)" -o bin/api ./cmd/server
	cd web && bun run build
	cd desktop && wails build -tags webkit2_41 -ldflags "-X main.version=$(VERSION)"

dev-api: install-data ## run api with air hot-reload (starts minio via docker compose)
	@[ -f api/.env ] || (touch api/.env && echo "created api/.env")
	@grep -q "^JWT_SECRET=" api/.env || (echo "JWT_SECRET=$$(openssl rand -base64 48)" >> api/.env && echo "appended JWT_SECRET to api/.env")
	@grep -q "^ADMIN_PASSWORD=" api/.env || (echo "ADMIN_PASSWORD=admin" >> api/.env && echo "appended ADMIN_PASSWORD=admin to api/.env")
	@grep -q "^DATA_DIR=" api/.env || echo "DATA_DIR=../data/api" >> api/.env
	@grep -q "^LOG_PATH=" api/.env || echo "LOG_PATH=../data/api/logs/app.log" >> api/.env
	@grep -q "^SMTP_HOST=" api/.env || echo "SMTP_HOST=localhost" >> api/.env
	@grep -q "^SMTP_PORT=" api/.env || echo "SMTP_PORT=1025" >> api/.env
	@grep -q "^SMTP_TLS=" api/.env || echo "SMTP_TLS=none" >> api/.env
	@grep -q "^SMTP_FROM=" api/.env || echo "SMTP_FROM=noreply@mist-drive.local" >> api/.env
	@grep -q "^PUBLIC_URL=" api/.env || echo "PUBLIC_URL=http://localhost:3000" >> api/.env
	docker compose up -d --wait minio mailpit
	@# Reap any orphaned hot-reload child from a previous session: when
	@# air dies hard, its compiled api/tmp/api keeps port 3000 forever
	@# (found one 5 days old). pkill matches the binary path, not "air".
	@pkill -f "[a]pi/tmp/api" 2>/dev/null && sleep 1 || true
	cd api && set -a && . ./.env && set +a && air || go run ./cmd/server

dev-ui: ## run web vite dev server
	cd web && bun run dev

dev-desktop: ## run the wails desktop app in dev mode (webkit2_41 tag is required on Ubuntu 24.04+)
	cd desktop && wails dev -tags webkit2_41

# Browse/edit the dev database (data/api/mist.db) with the godocstore
# CLI's own web UI at http://localhost:8391 — JSON-first, doesn't
# truncate the doc columns like a generic SQL browser would. Safe
# alongside a running dev-api (WAL + busy_timeout). Loopback-only, no
# auth, requires `make install`.
db-web: ## open godocstore serve on the dev mist.db (http://localhost:8391)
	godocstore serve --db data/api/mist.db

desktop-build: ## build the wails desktop binary
	cd desktop && wails build -tags webkit2_41 -ldflags "-X main.version=$(VERSION)"

# Packages desktop/build/bin/mist-drive as an AppImage. linuxdeploy +
# its plugin are cached in desktop/build/bin/ (gitignored) so repeat
# runs skip the download. Same tool/flags as CI's release.yml.
appimage: ## build the desktop AppImage (Linux)
	cd desktop && wails build -tags webkit2_41 -ldflags "$(LDFLAGS)"
	@test -x desktop/build/bin/linuxdeploy.AppImage || { \
		curl -Lo desktop/build/bin/linuxdeploy.AppImage https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage; \
		chmod +x desktop/build/bin/linuxdeploy.AppImage; \
	}
	@test -x desktop/build/bin/linuxdeploy-plugin-appimage.AppImage || { \
		curl -Lo desktop/build/bin/linuxdeploy-plugin-appimage.AppImage https://github.com/linuxdeploy/linuxdeploy-plugin-appimage/releases/download/continuous/linuxdeploy-plugin-appimage-x86_64.AppImage; \
		chmod +x desktop/build/bin/linuxdeploy-plugin-appimage.AppImage; \
	}
	rm -rf desktop/build/bin/appdir
	APPIMAGE_EXTRACT_AND_RUN=1 PATH="$(CURDIR)/desktop/build/bin:$$PATH" desktop/build/bin/linuxdeploy.AppImage \
		--appdir desktop/build/bin/appdir \
		--executable desktop/build/bin/mist-drive \
		--desktop-file desktop/build/linux/mist-drive.desktop \
		--icon-file desktop/build/linux/mist-drive.png \
		--output appimage
	mv Mist_Drive-x86_64.AppImage desktop/build/bin/mist-drive-x86_64.AppImage
	@echo "AppImage: desktop/build/bin/mist-drive-x86_64.AppImage"

run: install-data ## docker compose up — mist-drive + minio, no TLS, no reverse proxy
	docker compose up --build

test: test-unit test-integration ## run all api tests (unit + integration)

test-unit: ## run api unit tests
	cd api && go test ./...

test-integration: ## run api integration tests (requires docker)
	cd api && go test -tags=integration -timeout=300s ./...

stop: ## stop all docker compose services
	docker compose down

fmt: ## gofmt the api module
	cd api && gofmt -w .

check: ## gofmt check + go vet + race tests on the api module (pre-commit gate)
	@fmtout=$$(cd api && gofmt -l .); if [ -n "$$fmtout" ]; then echo "✗ needs gofmt:"; echo "$$fmtout"; exit 1; fi
	cd api && go vet ./...
	cd api && go test -race ./... -count=1

install-hooks: ## install the pre-commit hook (fmt + check on api before every commit)
	cp hooks/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit

clean: ## remove build artifacts and dependencies
	rm -rf api/bin api/tmp web/dist web/node_modules shared/node_modules desktop/build/bin desktop/frontend/dist desktop/frontend/node_modules
