# Build and development

This guide covers the local toolchain and containerized contributor environment.
For the branch, commit, rebase, and pull request workflow, see
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Prerequisites

For a host build, install:

- Go, using the version declared in [`go.mod`](../go.mod).
- [`golangci-lint`](https://golangci-lint.run/usage/install/).

[`goreleaser`](https://goreleaser.com/install/) and
[`git-cliff`](https://git-cliff.org/docs/installation/) are optional unless you
need to make a snapshot release or generate release notes. The Docker toolchain
includes all four tools at the pinned versions used by the project.

## Make targets

Run these commands from the repository root:

```bash
make build      # build bin/jevkit
make clean      # remove local build artifacts
make test       # run go test ./...
make lint       # run golangci-lint
make install    # install to ~/.local/bin, or $INSTALL_DIR
make snapshot   # create a GoReleaser snapshot
make plugins    # regenerate plugins/jevkit/<host>/
```

Prefer `make build` or `go build -o bin/jevkit`; bare `go build ./cmd/jevkit`
drops an ignored `./jevkit` at the repository root, which you can remove with
`make clean` or `rm -f jevkit`.

Before opening or updating a pull request, run at least `make lint test`.

## Docker and Compose

Docker is contributor tooling for reproducing CI; jevkit itself is a single
binary and is not run in Docker at runtime. The multi-stage `Dockerfile`
provides Go, `golangci-lint`, GoReleaser, and git-cliff. To use it directly:

```bash
docker build -t jevkit-dev .
docker run --rm jevkit-dev make test
docker run --rm jevkit-dev make lint
```

Docker Compose builds the same environment and mounts the repository at `/src`:

```bash
docker compose run --rm dev make build test
docker compose run --rm dev make lint test
docker compose run --rm --entrypoint bash dev
```

To build a native binary with the Compose toolchain, use
[`scripts/build-with-docker.sh`](../scripts/build-with-docker.sh):

```bash
scripts/build-with-docker.sh
scripts/build-with-docker.sh --install
```

For an Apple Silicon binary from another host, the equivalent explicit command
is:

```bash
docker compose run --rm -e CGO_ENABLED=0 -e GOOS=darwin -e GOARCH=arm64 dev make build
```

## Dev container

`.devcontainer/devcontainer.json` builds from the same `Dockerfile`, mounts the
repository at `/src`, and enables the Go extension. Open the repository in VS
Code or another devcontainer-compatible tool and reopen it in the container.
