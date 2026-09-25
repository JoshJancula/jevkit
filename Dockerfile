# Contributor tooling only. jevkit is a single static binary and never runs in Docker.
# Build: docker build -t jevkit-dev .
# Test:  docker run --rm jevkit-dev make test

FROM golangci/golangci-lint:v2.13.2 AS golangci-lint
FROM goreleaser/goreleaser:v2.12.0 AS goreleaser
FROM ghcr.io/orhun/git-cliff/git-cliff:2.10.1 AS git-cliff

FROM golang:1.26.2 AS dev
COPY --from=golangci-lint /usr/bin/golangci-lint /usr/local/bin/golangci-lint
COPY --from=goreleaser /usr/bin/goreleaser /usr/local/bin/goreleaser
COPY --from=git-cliff /usr/local/bin/git-cliff /usr/local/bin/git-cliff
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
