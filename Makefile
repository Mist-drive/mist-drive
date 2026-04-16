.PHONY: help install build dev run clean test test-unit test-integration desktop-dev desktop-build

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

install: ## install deps for api + web + desktop
	go install github.com/wailsapp/wails/v2/cmd/wails@latest
	go install github.com/air-verse/air@latest
	cd api && go mod download
	cd web && bun install
	cd desktop && go mod download && cd frontend && bun install

build: ## build api + web + desktop
	cd api && CGO_ENABLED=0 go build -o bin/api ./cmd/server
	cd web && bun run build
	cd desktop && wails build -tags webkit2_41

dev: ## run api (air) + web (vite) locally, side by side
	@echo "starting api (air) and web (vite)..."
	@(cd api && air 2>/dev/null || go run ./cmd/server) & \
	 (cd web && bun run dev); \
	 wait

desktop-dev: ## run the wails desktop app in dev mode (webkit2_41 tag is required on Ubuntu 24.04+)
	cd desktop && wails dev -tags webkit2_41

desktop-build: ## build the wails desktop binary
	cd desktop && wails build -tags webkit2_41

run: ## docker compose up — mist-drive + minio, no TLS, no reverse proxy
	mkdir -p deploy/data/api deploy/data/minio deploy/data/logs
	cd deploy && docker compose up --build

test: test-unit test-integration ## run all api tests (unit + integration)

test-unit: ## run api unit tests
	cd api && go test ./...

test-integration: ## run api integration tests (requires docker)
	cd api && go test -tags=integration -timeout=300s ./...

clean:
	rm -rf api/bin api/tmp web/dist web/node_modules desktop/build/bin desktop/frontend/dist desktop/frontend/node_modules
