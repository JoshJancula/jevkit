# jevkit

**jevkit** is a local toolkit that connects coding agents to [TypeSafe AI](https://typesafe.ai)'s **Jev** classifier (`api.typesafe.ai`). It installs agent hooks and an MCP server, redacts anything that would leave your machine, and optionally **compacts** large shell and tool output so agents see less noise.

## What Jev is (and is not)

| Jev **is** | Jev **is not** |
| --- | --- |
| A closed-set classifier (SystemOne) | A general chat LLM |
| A ranked-line helper for compaction | A substitute for your agent’s own model |
| Something that only ever sees **already-redacted** text | A place to send secrets, keys, or raw dumps |

“Compaction” here means: tag lines, ask Jev which lines matter, then assemble a shorter head / ranked middle / tail **locally** from the original output. If redaction or Jev fails, hooks fail open and pass the original through.

## Quickstart

```bash
# Build from source (or install a release binary onto PATH)
make build
./bin/jevkit version

# Store your Typesafe API key (hidden prompt; never pass the key as an argv)
jevkit key set
jevkit key test
jevkit doctor

# Wire hooks + MCP into an agent in this repo (or use --scope user)
jevkit install claude
# or: cursor | opencode | antigravity | codex | all

# Enable compaction for the wrapper / hooks (off by default)
export JEVKIT_COMPACT=1
```

Optional plugins under `plugins/jevkit/<host>/` (regenerate with `make plugins`) register the same hooks/MCP and check that `jevkit` is on `PATH`. See [docs/AGENTS.md](docs/AGENTS.md).

Coming from ralph’s jev integration? Run `jevkit migrate-from-ralph` once.

## Per-agent compaction mechanism

Every supported agent can rewrite shell commands to `jevkit exec -- …` (or replace output after the fact on Claude). That is what lets jevkit see the **real exit code** and compact safely.

| Agent | Install name | Primary mechanism | Model-visible output replace |
| --- | --- | --- | --- |
| Claude Code | `claude` | PostToolUse `updatedToolOutput` (success / Bash only) | Yes |
| Cursor | `cursor` | PreToolUse rewrite → `jevkit exec`; post-tool compact for native/MCP results | Shell post: no; native/MCP: yes |
| Antigravity | `antigravity` | PreToolUse `overwrite.CommandLine` → `jevkit exec` | No |
| OpenCode | `opencode` | Plugin `tool.execute.before` rewrite → `jevkit exec` | Unproven (telemetry only) |
| Codex | `codex` | PreToolUse rewrite → `jevkit exec` | Unproven (telemetry only) |

Details, payloads, and fixture notes: [docs/AGENTS.md](docs/AGENTS.md) and [docs/AGENT-CAPABILITIES.md](docs/AGENT-CAPABILITIES.md).

## Key storage (summary)

First match wins:

1. `JEVKIT_API_KEY` or `TYPESAFE_API_KEY` (environment)
2. Workspace `.env` (parsed for the key line only; never sourced)
3. Stored credential **command** (`jevkit key set --command '…'`)
4. OS keychain (service `jevkit`, account `TYPESAFE_API_KEY`)
5. `0600` file under the config dir (last resort)

Full precedence, paths, and commands: [docs/KEYS.md](docs/KEYS.md).

## Usage tracking

Every successful Jev call appends one line to local state (`usage.jsonl`). Nothing is uploaded for analytics.

```bash
jevkit usage
jevkit usage --format json
```

## Shadow mode and enabling compaction

Compaction is **off** until you opt in:

```bash
export JEVKIT_COMPACT=1
```

**Shadow mode** measures what ranked-line compaction would have saved without changing what the agent sees (and for Claude/Cursor post-tool paths, skips replacement entirely):

```bash
export JEVKIT_COMPACT=1
export JEVKIT_COMPACT_SHADOW=1
```

Shadow savings are recorded under the state directory as `jev-compact.jsonl`. Turn shadow off (unset `JEVKIT_COMPACT_SHADOW`) when you want live ranked-line compaction.

There is a separate MCP/registry gate, `JEVKIT_SHADOW=1`, that forces decision tools to report fallback while logging what Jev answered—useful while question sets are still uncalibrated.

## Security and redaction

Everything destined for `api.typesafe.ai` is redacted on your machine first. Invalid config or a failed verification pass **blocks the send** (exit 3); hooks still pass original output to the agent.

```bash
jevkit redact init
jevkit redact check
jevkit redact test --diff -
```

Threat model, layered config, HARD vs SOFT rules, and a regulated-data checklist: [docs/REDACTION.md](docs/REDACTION.md).

## Docs map

| Doc | Contents |
| --- | --- |
| [docs/AGENTS.md](docs/AGENTS.md) | Install, plugins, hooks, MCP tools, env gates |
| [docs/KEYS.md](docs/KEYS.md) | Key sources, precedence, migrate-from-ralph |
| [docs/REDACTION.md](docs/REDACTION.md) | Redaction threat model and tuning |
| [docs/AGENT-CAPABILITIES.md](docs/AGENT-CAPABILITIES.md) | Phase-0 hook capability spike notes |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Build, test, lint, Docker for contributors |

## Build

```bash
make build    # bin/jevkit
make test
make lint
make plugins  # regenerate plugins/jevkit/<host>/
```

Module path currently uses a placeholder owner (`github.com/OWNER/jevkit`) until the publish owner is confirmed.
