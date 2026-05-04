VERSION ?= dev

.PHONY: help install install-data build api-dev ui-dev desktop-dev desktop-build run stop test test-unit test-integration clean

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

install: ## install deps for api + web + desktop
	go install github.com/wailsapp/wails/v2/cmd/wails@latest
	go install github.com/air-verse/air@latest
	cd api && go mod download
	cd web && bun install
	cd desktop && go mod download && cd frontend && bun install

install-data:
	mkdir -p data/api data/minio data/api/logs

build: ## build api + web + desktop
	cd api && CGO_ENABLED=0 go build -ldflags "-X main.Version=$(VERSION)" -o bin/api ./cmd/server
	cd web && bun run build
	cd desktop && wails build -tags webkit2_41 -ldflags "-X main.version=$(VERSION)"

api-dev: install-data ## run api with air hot-reload (starts minio via docker compose)
	@[ -f api/.env ] || (touch api/.env && echo "created api/.env")
	@grep -q "^JWT_SECRET=" api/.env || (echo "JWT_SECRET=$$(openssl rand -base64 48)" >> api/.env && echo "appended JWT_SECRET to api/.env")
	@grep -q "^ADMIN_PASSWORD=" api/.env || (echo "ADMIN_PASSWORD=admin" >> api/.env && echo "appended ADMIN_PASSWORD=admin to api/.env")
	@grep -q "^DATA_DIR=" api/.env || echo "DATA_DIR=../data/api" >> api/.env
	@grep -q "^LOG_PATH=" api/.env || echo "LOG_PATH=../data/api/logs/app.log" >> api/.env
	docker compose up -d --wait minio
	cd api && set -a && . ./.env && set +a && air || go run ./cmd/server

ui-dev: ## run web vite dev server
	cd web && bun run dev

desktop-dev: ## run the wails desktop app in dev mode (webkit2_41 tag is required on Ubuntu 24.04+)
	cd desktop && wails dev -tags webkit2_41

desktop-build: ## build the wails desktop binary
	cd desktop && wails build -tags webkit2_41 -ldflags "-X main.version=$(VERSION)"

run: install-data ## docker compose up — mist-drive + minio, no TLS, no reverse proxy
	docker compose up --build

test: test-unit test-integration ## run all api tests (unit + integration)

test-unit: ## run api unit tests
	cd api && go test ./...

test-integration: ## run api integration tests (requires docker)
	cd api && go test -tags=integration -timeout=300s ./...

stop: ## stop all docker compose services
	docker compose down

clean: ## remove build artifacts and dependencies
	rm -rf api/bin api/tmp web/dist web/node_modules shared/node_modules desktop/build/bin desktop/frontend/dist desktop/frontend/node_modules
