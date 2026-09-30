# Build and test Jevkit

Install the Go version in [`go.mod`](../go.mod), then run these commands from the repository root:

```bash
make build       # write bin/jevkit
./bin/jevkit version
make test        # go test ./...
make install     # copy to ~/.local/bin, or $INSTALL_DIR
```

Add `~/.local/bin` to your `PATH` if needed. For linting, install [golangci-lint](https://golangci-lint.run/usage/install/) and run `make lint`. Run `make plugins` after changing generated agent plugin templates, then confirm the generated-plugin check your CI uses still passes. Before a pull request, run `make lint test` and `git diff --check`.

Local SDLC runs from a built binary write under `$JEVKIT_STATE_DIR` (or the XDG/Windows default described in [usage](USAGE.md)). Tests use temp directories and do not need a personal state tree. Fixture-driven SDLC journeys live under `cmd/jevkit/sdlc` and `internal/sdlc`; they exercise planner ready/blocked setup, plan and check approval, verification outcomes, fan-out scheduling, custom/child runs, and delete/prune without calling live providers.

`make snapshot` needs GoReleaser, Syft, and Cosign to produce the configured snapshot artifacts locally. The tagged release workflow installs its own tools; see [Releases](../CONTRIBUTING.md#releases). Install the published binary from [GitHub Releases](https://github.com/JoshJancula/jevkit/releases); use `make install` when building from source for development.

## Container toolchain

Docker and the dev container are contributor tools; users run the Jevkit binary directly. To reproduce Go tests in the project container:

```bash
docker compose run --rm dev make test
docker compose run --rm dev make lint test
```

To build a native binary with that toolchain, run `scripts/build-with-docker.sh`. Add `--install` to install the result. `.devcontainer/devcontainer.json` uses the same Dockerfile.

See [CONTRIBUTING.md](../CONTRIBUTING.md) for branches, pull requests, and releases.
