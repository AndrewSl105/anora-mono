# Fleetpocket monorepo tasks. GNU Make 3.81 compatible (macOS default).
#
#   make dev-setup   build dev images, print a password hash and TOTP secret
#   make dev         dev stack (server, postgres, docker-proxy, fake agent) + Vite
#   make test        go test + agent tests + web unit tests
#   make lint        go vet + golangci-lint, agent typecheck, web typecheck/lint/schema check
#   make build       production images (fleetpocket-server, fleetpocket-agent, fleetpocket-web), tag :dev
#   make gen-api     regenerate web/src/api/schema.ts from docs/openapi.yaml
#   make e2e         Playwright smoke tests (mock mode)
#   make e2e-real    Playwright against the running dev stack (E2E_PASSWORD, E2E_TOTP_SECRET)
#   make screenshots README screenshots and demo.gif in docs/screenshots (mock mode; ffmpeg for the GIF)

COMPOSE_DEV := docker compose -f deploy/docker-compose.dev.yml

# Server port on localhost and the Vite dev server port; change them when 8080
# or 5173 are taken. The server allows the Vite origin for the WebSocket.
FLEETPOCKET_DEV_PORT ?= 8080
WEB_PORT ?= 5173
FLEETPOCKET_DEV_CLIENT_ORIGIN ?= http://localhost:$(WEB_PORT)
export FLEETPOCKET_DEV_PORT FLEETPOCKET_DEV_CLIENT_ORIGIN

# Postgres for the server's tests (`make test-db` starts one on port 55432).
# Without it, database and Docker integration tests are skipped.
TEST_DB_PORT ?= 55432
FLEETPOCKET_TEST_DATABASE_URL ?= postgres://fleetpocket:fleetpocket@localhost:$(TEST_DB_PORT)/fleetpocket?sslmode=disable

.PHONY: dev dev-setup dev-down dev-logs test test-db test-server test-agent test-web \
	lint build gen-api e2e e2e-real screenshots deps

deps:
	cd agent && npm ci
	cd web && npm ci

# ---- local development ----

dev-setup:
	$(COMPOSE_DEV) build
	@echo "Password (at least 12 characters), then put the hash in deploy/.env.dev as ADMIN_PASSWORD_HASH='<hash>':"
	@$(COMPOSE_DEV) run --rm --no-deps server hash-password
	@$(COMPOSE_DEV) run --rm --no-deps server totp-setup
	@echo "Put ADMIN_TOTP_SECRET in deploy/.env.dev and the otpauth:// URI in your authenticator app."

dev:
	$(COMPOSE_DEV) up -d --build
	cd web && FLEETPOCKET_BACKEND_URL=http://localhost:$(FLEETPOCKET_DEV_PORT) npx vite --port $(WEB_PORT) --strictPort

dev-down:
	$(COMPOSE_DEV) down

dev-logs:
	$(COMPOSE_DEV) logs -f server

# ---- tests ----

test: test-server test-agent test-web

test-db:
	docker run -d --name fleetpocket-test-pg -e POSTGRES_USER=fleetpocket -e POSTGRES_PASSWORD=fleetpocket \
		-p $(TEST_DB_PORT):5432 postgres:17-alpine

# -p 1: Docker integration tests must not run in parallel (server/README.md).
# They start the fake agent from fleetpocket-agent:dev.
test-server:
	docker build -q -t fleetpocket-agent:dev agent >/dev/null
	cd server && FLEETPOCKET_TEST_DATABASE_URL='$(FLEETPOCKET_TEST_DATABASE_URL)' go test -p 1 -count=1 ./...

test-agent:
	cd agent && npm test

test-web:
	cd web && npm test

e2e:
	cd web && npm run e2e

e2e-real:
	cd web && E2E_BACKEND=http://localhost:$(FLEETPOCKET_DEV_PORT) E2E_PORT=$(WEB_PORT) npx playwright test

screenshots:
	cd web && npm run screenshots

# ---- lint, build, codegen ----

lint:
	cd server && go vet ./... && golangci-lint run
	cd agent && npm run typecheck
	cd web && npm run check:api && npm run typecheck && npm run lint

build:
	docker build -t fleetpocket-server:dev server
	docker build -t fleetpocket-agent:dev agent
	docker build -t fleetpocket-web:dev --build-arg VITE_APP_VERSION=$$(git rev-parse --short HEAD) web

gen-api:
	cd web && npm run gen:api
