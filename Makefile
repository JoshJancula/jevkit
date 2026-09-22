VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint snapshot plugins

build:
	go build -ldflags "$(LDFLAGS)" -o bin/jevkit ./cmd/jevkit

test:
	go test ./...

lint:
	golangci-lint run ./...

snapshot:
	goreleaser release --snapshot --clean

plugins:
	go run ./internal/plugins/cmd/gen
