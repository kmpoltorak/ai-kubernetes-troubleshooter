BINARY  := bin/api
TEST_DB := akt-test-pg
TEST_DATABASE_URL ?= postgres://troubleshooter:troubleshooter@localhost:55433/troubleshooter_test?sslmode=disable
BUILD_VERSION ?= dev
BUILD_REVISION ?= unknown
BUILD_TIME ?=

# Load .env for run/migrate targets when present.
ifeq (,$(filter test-e2e,$(MAKECMDGOALS)))
ifneq (,$(wildcard .env))
include .env
export
endif
endif

.PHONY: web build test test-auth test-e2e test-integration test-upgrade test-browser test-db test-db-stop lint fmt vet run migrate-up migrate-down docker-up docker-down

# Builds the React UI into internal/api/ui/dist, which the binary embeds.
web:
	cd web && npm ci && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-X 'github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/buildinfo.Version=$(BUILD_VERSION)' -X 'github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/buildinfo.Revision=$(BUILD_REVISION)' -X 'github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/buildinfo.BuiltAt=$(BUILD_TIME)'" -o $(BINARY) ./cmd/api

test:
	go test -race ./...

# Runs a real OAuth2 Proxy with a disposable protocol-level OIDC provider.
test-auth:
	go test -race -tags authintegration -count=1 -run '^TestOIDCAuthentication$$' ./internal/api

test-e2e:
	bash scripts/test-e2e.sh

# Baseline-commit upgrade, two-replica migration and pg_dump/pg_restore; requires Docker.
test-upgrade:
	bash scripts/test-upgrade.sh

# Playwright (Chromium) against the real binary; requires Docker and web deps:
# cd web && npm ci && npx playwright install chromium
test-browser:
	bash scripts/test-browser.sh

# Starts a disposable PostgreSQL container (port 55433) if it is not running.
test-db:
	@docker inspect $(TEST_DB) >/dev/null 2>&1 || docker run -d --rm --name $(TEST_DB) \
		-e POSTGRES_USER=troubleshooter -e POSTGRES_PASSWORD=troubleshooter -e POSTGRES_DB=troubleshooter_test \
		-p 55433:5432 postgres:17-alpine >/dev/null
	@until docker exec $(TEST_DB) pg_isready -U troubleshooter -d troubleshooter_test >/dev/null 2>&1; do sleep 1; done

test-db-stop:
	-docker rm -f $(TEST_DB)

# Integration tests share one database, so packages run sequentially (-p 1).
test-integration: test-db
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -tags integration -count=1 -p 1 ./...

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	golangci-lint run --build-tags integration,authintegration,modeleval,e2e

fmt:
	gofmt -w .

vet:
	go vet ./...

run:
	go run ./cmd/api

migrate-up:
	go run ./cmd/api migrate up

migrate-down:
	go run ./cmd/api migrate down

docker-up:
	docker compose up --build -d   # needs DATABASE_URL in .env

docker-down:
	docker compose down
