# Contributor tooling only. jevkit is a single static binary and never runs in Docker.
# Build: docker build -t jevkit-dev .
# Test:  docker run --rm jevkit-dev make test

FROM golangci/golangci-lint:v2.13.2 AS golangci-lint
FROM goreleaser/goreleaser:v2.18.2 AS goreleaser

FROM golang:1.26.2 AS dev
COPY --from=golangci-lint /usr/bin/golangci-lint /usr/local/bin/golangci-lint
COPY --from=goreleaser /usr/bin/goreleaser /usr/local/bin/goreleaser
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
