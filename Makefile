BINARY  := bin/api
TEST_DB := akt-test-pg
TEST_DATABASE_URL ?= postgres://troubleshooter:troubleshooter@localhost:55433/troubleshooter_test?sslmode=disable

# Load .env for run/migrate targets when present.
ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: build test test-integration test-db test-db-stop lint fmt vet run migrate-up migrate-down docker-up docker-down

build:
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/api

test:
	go test -race ./...

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
	go vet ./...
	golangci-lint run --build-tags integration

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
	docker compose up --build -d

docker-down:
	docker compose down
