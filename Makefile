VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
INSTALL_DIR ?= $(HOME)/.local/bin

.PHONY: build clean test lint install snapshot plugins

build:
	go build -ldflags "$(LDFLAGS)" -o bin/jevkit ./cmd/jevkit

clean:
	rm -f jevkit bin/jevkit

install: build
	mkdir -p "$(INSTALL_DIR)"
	install -m 0755 bin/jevkit "$(INSTALL_DIR)/jevkit"
	@echo "installed $(INSTALL_DIR)/jevkit"

test:
	go test ./...

lint:
	golangci-lint run ./...

snapshot:
	goreleaser release --snapshot --clean

plugins:
	go run ./internal/plugins/cmd/gen
