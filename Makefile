GO      ?= go
PKGS    ?= ./...
COVER   ?= coverage.out

# Integration tests run when these are set, e.g.
#   make test SQLKIT_TEST_POSTGRES_DSN="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
export SQLKIT_TEST_POSTGRES_DSN
export SQLKIT_TEST_CLICKHOUSE_DSN

.PHONY: all test lint cover vet fmt tidy examples postgres-up postgres-down

all: vet lint test

## test: run all tests with the race detector
test:
	$(GO) test -race -count=1 $(PKGS)

## vet: run go vet
vet:
	$(GO) vet $(PKGS)

## lint: run golangci-lint
lint:
	golangci-lint run $(PKGS)

## cover: run tests with coverage and print the summary
cover:
	$(GO) test -race -count=1 -covermode=atomic -coverprofile=$(COVER) $(PKGS)
	$(GO) tool cover -func=$(COVER) | tail -n 1

## fmt: format the code
fmt:
	gofmt -w .

## tidy: tidy go.mod
tidy:
	$(GO) mod tidy

## examples: run the SQLite example
examples:
	$(GO) run ./examples/sqlite

## postgres-up: start a throwaway PostgreSQL in Docker for the integration tests
postgres-up:
	docker run -d --rm --name sqlkit-postgres -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:17-alpine

## postgres-down: stop the throwaway PostgreSQL
postgres-down:
	docker stop sqlkit-postgres
