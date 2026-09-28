# Build and test Jevkit

Install the Go version in [`go.mod`](../go.mod), then run these commands from the repository root:

```bash
make build       # write bin/jevkit
./bin/jevkit version
make test        # go test ./...
make install     # copy to ~/.local/bin, or $INSTALL_DIR
```

Add `~/.local/bin` to your `PATH` if needed. For linting, install [golangci-lint](https://golangci-lint.run/usage/install/) and run `make lint`. Run `make plugins` after changing generated agent plugin templates. Before a pull request, run `make lint test`.

`make snapshot` needs GoReleaser, Syft, and Cosign to produce the configured snapshot artifacts locally. The tagged release workflow installs its own tools; see [Releases](../CONTRIBUTING.md#releases).

## Container toolchain

Docker and the dev container are contributor tools; users run the Jevkit binary directly. To reproduce Go tests in the project container:

```bash
docker compose run --rm dev make test
docker compose run --rm dev make lint test
```

To build a native binary with that toolchain, run `scripts/build-with-docker.sh`. Add `--install` to install the result. `.devcontainer/devcontainer.json` uses the same Dockerfile.

See [CONTRIBUTING.md](../CONTRIBUTING.md) for branches, pull requests, and releases.
