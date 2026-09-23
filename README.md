# jevkit

**jevkit** is a local toolkit that connects coding agents to [TypeSafe AI](https://typesafe.ai)'s **Jev** classifier (`api.typesafe.ai`). It installs agent hooks and an MCP server, redacts anything that would leave your machine, and optionally **compacts** large shell and tool output so agents see less noise.

## What is Jev?

| Jev **is** | Jev **is NOT** |
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
# or: codex | opencode | all

# Enable compaction for the wrapper / hooks (off by default)
export JEVKIT_COMPACT=1
```

Optional plugins under `plugins/jevkit/<host>/` (regenerate with `make plugins`) register the same hooks/MCP and check that `jevkit` is on `PATH`. See [docs/AGENTS.md](docs/AGENTS.md).

## Ask Jev from the terminal

`ask` is the human-facing interface to Jev's three typed questions. It uses the
same key resolution and redaction safeguards as the agent integrations, and
every value it sends — state, instructions, criteria — is redacted locally
before it ever leaves the machine.

```bash
# Noul: a yes/no assessment returned as a value from 0 to 1.
jevkit ask noul --state "42 tests passed; 0 failed" --question "Did the build succeed?"

# Choice: `--options` is one comma-separated, closed set of answer labels.
jevkit ask choice --state "HTTP status: 503" --question "What should happen next?" --options "retry,fail"

# Score: levels are ordered from low to high and must contain 2–10 values.
jevkit ask score --state "This change affects authentication" --question "How risky is this?" --levels "low,medium,high"

# JSON is appropriate for a script or another tool.
jevkit ask choice --state "lint: 0 issues" --question "What is the outcome?" --options "pass,fail" --format json
```

The CLI translates a choice list to the API's `criteria` map with null descriptions, which the API explicitly permits when labels need no additional rubric. See the [TypeSafe API reference](https://docs.typesafe.ai/api) for the underlying request and answer types.

### Which input form to use

Each `ask noul|choice|score` flag has three forms; use exactly one per field:

| Field        | Plain (shorthand)                    | `--*-json`             | `--*-file`             |
| ------------ | ------------------------------------- | ----------------------- | ----------------------- |
| state        | `--state`                             | `--state-json`          | `--state-file`          |
| instructions | `--question`                          | `--instructions-json`   | `--instructions-file`   |
| criteria     | `--options`/`--levels`/`--true-criteria`/`--false-criteria` | `--criteria-json` | `--criteria-file` |

- The plain flags are the ergonomic default: a comma list for `--options`/`--levels`,
  or free text for `--state`/`--question`/noul's `--true-criteria`/`--false-criteria`.
- `--*-json` takes literal JSON text on the command line: an object for Choice
  or Noul criteria, an array for Score criteria, or any JSON string/object/array
  for state or instructions. Quote it so your shell passes it through as one
  argument — single quotes are usually easiest, since JSON itself uses double
  quotes: `--criteria-json '{"billing":"...","technical":"...","sales":"..."}'`.
- `--*-file` reads the same JSON (or plain text) from a file, or from stdin
  with `-`, for content too large or awkward to quote on a command line.

Reach for `--criteria-json`/`--criteria-file` instead of the comma-list
shorthand when an option needs more than a bare label — a rubric, examples, or
structured fields the classifier should weigh:

```bash
jevkit ask choice --state "Customer says the invoice total looks wrong" \
  --question "Route to which team?" \
  --criteria-json '{"billing":"Payment, invoice or refund issues","technical":"Product defects or errors","sales":"Pricing or new purchase questions"}'
```

Run `jevkit ask noul|choice|score --help` for the full set of shorthand and
detailed examples.

### Multi-question requests

`jevkit ask request --file <path|->` sends a full API-shaped request — several
named questions in one call, with an optional model override:

```bash
jevkit ask request --file request.json
cat request.json | jevkit ask request --file - --model jev-1.13.0
```

`request.json` mirrors the wire shape directly: a `model` (optional), a
`state`, and a `questions` object of `{type, instructions, criteria}` entries
keyed by question id. Every value is validated against the local typed schema
and redacted before it is sent, exactly as with the single-question forms, and
the file's contents are never echoed back unredacted. Output names every
answer; `--format json` includes the model, token usage, each answer's type,
confidence, probabilities and (for Score) legend.

## Agent integrations

`jevkit install <agent>` installs the default plugin bundle (runtime hooks plus MCP). Use `--components hooks` or `--components mcp` to install only one part. Claude Code, Codex, and OpenCode can replace eligible, high-confidence post-tool output with Jevkit’s locally assembled compacted result.

Details for installation, hooks, and MCP configuration: [docs/AGENTS.md](docs/AGENTS.md).

## Developer assessments (MCP)

`jev_developer_assess` is a strongly typed MCP dispatcher over five small, opt-in, versioned `developer.*` question sets. They are **decision support, not autonomous execution**: every answer is returned as data, and no jevkit tool call ever modifies code, runs a command, merges, deploys, or exposes a secret — the caller decides what to do with the answer. Low-confidence answers always resolve to `gather` or `fallback`, never `act`, the same registry policy that backs `jev_classify_request` and friends.

Each assessment expects a structured `state` object drawn from a shared field set (`diffSummary`, `affectedAreas`, `testOutput`, `environment`, `constraints`); every assessment requires its own subset (unknown keys are rejected, and everything is JSON-aware redacted before it is sent):

```json
{"assessment": "developer.change-risk", "state": {"diffSummary": "widen auth token TTL from 15m to 1h", "affectedAreas": ["auth"], "testOutput": "go test ./internal/auth/... ok"}}
```

```json
{"assessment": "developer.failure-triage", "state": {"testOutput": "FAIL TestLogin: dial tcp: i/o timeout after 30s", "diffSummary": "no related changes in the last 3 commits"}}
```

```json
{"assessment": "developer.test-priority", "state": {"diffSummary": "renamed an internal helper, no behavior change", "affectedAreas": ["internal/util"]}}
```

```json
{"assessment": "developer.review-disposition", "state": {"diffSummary": "removed an unreachable dead code path", "affectedAreas": ["internal/registry"]}}
```

```json
{"assessment": "developer.release-readiness", "state": {"testOutput": "all suites passed", "constraints": "must not require a database migration"}}
```

For one-off or project-local questions outside this curated set, use `jev_ask` instead: it applies the identical redaction and size limits without weakening either, so a project never needs its own less-safe path for custom assessments.

**Calibration, shadow rollout, limitations:** every `developer.*` set's thresholds are placeholders (`"calibration": "placeholder - uncalibrated"` in the registry) pending real shadow-mode data; run with `JEVKIT_SHADOW=1` first (see below) to log what the assessment would have decided without acting on it, and only tighten thresholds once logged decisions have been reviewed against outcomes. These assessments reason only over what `state` contains — they do not read your repository, run tests, or see anything you did not include — so an incomplete `diffSummary` or missing `testOutput` degrades the answer's quality without any signal that it did.

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

**Shadow mode** records the proposed closed-set disposition and byte savings without changing what the agent sees:

```bash
export JEVKIT_COMPACT=1
export JEVKIT_COMPACT_SHADOW=1
```

Shadow savings are recorded under the state directory as `jev-compact.jsonl`. Turn shadow off (unset `JEVKIT_COMPACT_SHADOW`) when you want live ranked-line compaction.

## Compaction policy

Optionally create `<config-dir>/compaction.yaml` to protect or tune known commands. Policies are declarative; they cannot run code or override binary/source-data, redaction, confidence, or runtime-capability safeguards.

```yaml
version: 1
rules:
  - id: preserve-generated-files
    command: '^npm run generate'
    action: never-compact
  - id: focused-tests
    command: '^go test'
    action: eligible
    threshold_bytes: 4096
```

Validate and inspect a rule locally:

```bash
jevkit compact validate
jevkit compact explain --command "go test ./..."
```

Project policy lives at `.jevkit/compaction.yaml`; because it is repository content, it may add only `never-compact` rules.

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
| [docs/KEYS.md](docs/KEYS.md) | Key sources and precedence |
| [docs/REDACTION.md](docs/REDACTION.md) | Redaction threat model and tuning |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Build, test, lint, Docker for contributors |

## Build

```bash
make build    # bin/jevkit
make test
make lint
make plugins  # regenerate plugins/jevkit/<host>/
```

### Docker Compose contributor workflow

Docker Compose uses the repository's pinned contributor toolchain, so it is
useful when the locally installed Go version differs from the project target.
It is not required to run `jevkit` itself.

```bash
# Build a native binary for this operating system and CPU.
scripts/build-with-docker.sh

# Build and install it to ~/.local/bin/jevkit.
scripts/build-with-docker.sh --install
jevkit version

# Run the full lint and test checks used by CI.
docker compose run --rm dev make lint test

# Open a shell with the source tree mounted at /src.
docker compose run --rm --entrypoint bash dev
```

Module path currently uses a placeholder owner (`github.com/OWNER/jevkit`) until the publish owner is confirmed.

## License

jevkit is released under the [MIT License](LICENSE).
