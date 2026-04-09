.PHONY: help install build dev run clean test test-unit test-integration

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

install: ## install deps for api + web + desktop
	cd api && go mod download
	cd web && bun install
	cd desktop && go mod download && cd frontend && bun install

build: ## build api + web + desktop
	cd api && CGO_ENABLED=0 go build -o bin/api ./cmd/server
	cd web && bun run build
	cd desktop && wails build

desktop-dev: ## run the wails desktop app in dev mode
	# webkit2_41 tag required on Ubuntu 24.04+ (webkit2gtk-4.1).
	cd desktop && wails dev -tags webkit2_41

desktop-build: ## build the wails desktop binary
	cd desktop && wails build -tags webkit2_41

dev: ## run api (air) + web (vite) locally, side by side
	@echo "starting api (air) and web (vite)..."
	@(cd api && air 2>/dev/null || go run ./cmd/server) & \
	 (cd web && bun run dev); \
	 wait

data-dirs: ## pre-create bind-mount dirs owned by the host user (avoids root-owned dirs from dockerd)
	mkdir -p deploy/data/api deploy/data/minio deploy/data/logs

run: data-dirs ## docker compose up (with traefik tls profile)
	cd deploy && docker compose --profile tls up --build

run-notls: data-dirs ## docker compose up without traefik (bring your own gateway)
	cd deploy && docker compose up --build

test: test-unit test-integration ## run all api tests (unit + integration)

test-unit: ## run api unit tests
	cd api && go test ./...

test-integration: ## run api integration tests (requires docker)
	cd api && go test -tags=integration -timeout=300s ./...

clean:
	rm -rf api/bin api/tmp web/dist web/node_modules desktop/build/bin desktop/frontend/dist desktop/frontend/node_modules
