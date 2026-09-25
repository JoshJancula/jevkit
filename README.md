# jevkit

**jevkit** is a local toolkit that connects coding agents to [TypeSafe AI](https://typesafe.ai)'s **Jev** classifier. It installs agent hooks and an MCP server, redacts content before it leaves your machine, and can optionally compact noisy shell and tool output. Jevkit assembles compacted output locally from the original text; it is not a chat model or an autonomous coding agent.

## What is Jev?

| Jev **is** | Jev **is not** |
| --- | --- |
| A closed-set classifier and ranked-line helper | A general chat LLM or coding agent |
| A way to support local compaction and decisions | A substitute for your agent’s own model |
| Used only after Jevkit’s local redaction | A place to send secrets, keys, or raw dumps |

## Quickstart

```bash
# Build from source
make build
./bin/jevkit version

# Store and test your Typesafe API key (hidden prompt)
jevkit key set
jevkit key test

# Optional: choose a Jev model for all Jevkit integrations
jevkit model set jev-1.13.0
jevkit model status

# Install hooks and MCP for an agent in this repo
jevkit install claude                 # or codex, opencode, cursor, antigravity, all

# Optional: enable conservative compaction
export JEVKIT_COMPACT=1
```

Once a release is published, macOS/Linux users can run the checksum-verified
installer (it uses `~/.local/bin` without sudo):

```bash
curl -fsSL https://raw.githubusercontent.com/JoshJancula/jevkit/main/install.sh | sh
```

Set `JEVKIT_VERSION` to pin a release; Windows users can run `install.ps1` from
the published release assets. Use `--scope user` for a user-level agent install. See
[the agent guide](docs/AGENT-INTEGRATIONS.md) and [key guide](docs/KEYS.md) for options and
precedence.

## Where next

Jevkit’s detailed instructions live in the documentation:

| Topic | Guide |
| --- | --- |
| Agent installs, hooks, plugins, MCP, and compaction | [docs/AGENT-INTEGRATIONS.md](docs/AGENT-INTEGRATIONS.md) |
| Ask Jev from the terminal | [docs/ASK.md](docs/ASK.md) |
| API key storage and precedence | [docs/KEYS.md](docs/KEYS.md) |
| Redaction and privacy boundaries | [docs/REDACTION.md](docs/REDACTION.md) |
| Shell security checks | [docs/SECURITY-CHECK.md](docs/SECURITY-CHECK.md) |
| Adaptive SDLC runs | [docs/SDLC.md](docs/SDLC.md) |
| Custom SDLC workflows | [docs/SDLC-WORKFLOWS.md](docs/SDLC-WORKFLOWS.md) |
| Build and development | [docs/BUILD.md](docs/BUILD.md) |
| Contribution workflow | [CONTRIBUTING.md](CONTRIBUTING.md) |

Adaptive SDLC uses a personal agent roster to route tasks through classifiers and
workers; start with [the SDLC guide](docs/SDLC.md) and its [workflow guide](docs/SDLC-WORKFLOWS.md).
For one-off typed questions, use `jevkit ask`; its forms and file-based request
format are documented in [docs/ASK.md](docs/ASK.md). `jevkit usage` shows
recorded Jev calls separately from agent runtime usage; use `--source jev` or
`--source runtime` to select one, or `--format json` for a version 2 report.

Everything sent to `api.typesafe.ai` is redacted locally first. Invalid config or
a failed verification pass blocks the send, while integrations pass original
output through to the agent; see [docs/REDACTION.md](docs/REDACTION.md) and the
[security checklist](docs/SECURITY-CHECK.md).

## Build

See [docs/BUILD.md](docs/BUILD.md) for prerequisites, all Make targets, Docker,
and the dev container. The essential local checks are:

```bash
make build
make test
make lint
make plugins
```

## License

jevkit is released under the [MIT License](LICENSE).
