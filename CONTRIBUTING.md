# Contributing to jevkit

## Local toolchain

Requires Go (see `go.mod`), `golangci-lint`, and optionally `goreleaser` and `git-cliff`.

    make build      # bin/jevkit
    make test
    make lint
    make snapshot   # goreleaser snapshot build

## Docker (contributors only)

Docker is contributor tooling that reproduces CI. jevkit itself is a single
binary and is **never** run in Docker at runtime.

The `Dockerfile` is multi-stage and bundles the Go toolchain, golangci-lint,
goreleaser and git-cliff:

    docker build -t jevkit-dev .
    docker run --rm jevkit-dev make test
    docker run --rm jevkit-dev make lint

Docker Compose provides the same contributor environment with the repository
mounted at `/src`:

    docker compose run --rm dev make build test
    docker compose run --rm dev make lint test

For a host-runnable Apple Silicon binary without changing local Go:

    docker compose run --rm -e CGO_ENABLED=0 -e GOOS=darwin -e GOARCH=arm64 dev make build

## Dev container

`.devcontainer/devcontainer.json` builds from the same `Dockerfile`; open the
repo in VS Code (or any devcontainer-compatible tool) and reopen in container.
