# Agents

How coding agents connect to jevkit: install, plugins, compaction mechanisms, MCP tools, and environment gates.

## Install (hooks + MCP)

From a project directory (default `--scope project`):

```bash
jevkit install claude
jevkit install opencode
jevkit install codex
jevkit install all
```

Useful flags:

```bash
jevkit install claude --dry-run
jevkit install codex --scope user
jevkit install claude --binary /usr/local/bin/jevkit
jevkit uninstall claude
jevkit doctor
```

Install is idempotent and marker-based. The first write keeps a `.jevkit-original` backup so uninstall can restore byte-exact config. `jevkit doctor` reports whether each agent binary is on `PATH` and whether hooks/MCP look installed.

## Plugin packages

Distributable packages live under `plugins/jevkit/<host>/` (Claude Code and OpenCode). Regenerate from templates with:

```bash
make plugins
```

Each package registers hooks/MCP that call the `jevkit` binary and ships `shared/jevkit-plugin-bootstrap.sh`, which probes `PATH` and prints (or can apply) a pinned `go install …` remediation when `jevkit` is missing.

## Capability matrix

| Agent | Install surface | Post-tool evidence | Model-visible result policy |
| --- | --- | --- | --- |
| Claude Code | Plugin hooks or `.claude/settings.json` | Success and failure are distinct events; successful Bash payloads carry the output shape. | May replace only a validated tool-result shape with `updatedToolOutput`; otherwise preserve it. |
| Codex | `.codex/hooks.json` | PostToolUse observes Bash output after both successful and non-zero commands, but has no documented exit-status field. | May replace an eligible, high-confidence result through `continue:false` feedback; otherwise preserve it. |
| OpenCode | OpenCode plugin | `tool.execute.after` observes completed/error status and result. | May replace an eligible, high-confidence shell result with the locally assembled compacted output; otherwise preserve it. |

This matrix was audited against the vendor hook references on 2026-09-23. It is deliberately conservative: a pre-tool input rewrite does **not** prove that a runtime can compact or replace a result after execution. Sanitized fixtures and adapter tests enforce the stated boundary.

## Runtime integration

`jevkit install <agent>` installs the default **plugin bundle** (hooks plus MCP). Use `--components hooks` or `--components mcp` when you need only one integration surface. Installed assets call a private, versioned dispatcher and always fail open; `hook` and `exec` are intentionally not public CLI commands.

## MCP server

```bash
jevkit mcp status
jevkit mcp config
jevkit mcp start
```

`jevkit mcp start` serves over stdio:

| Tool | Role |
| --- | --- |
| `jev_classify_request` | Classify a request |
| `jev_classify_failure` | Classify a failure |
| `jev_rank_relevance` | Rank line relevance |
| `jev_developer_assess` | Dispatch one of five built-in `developer.*` decision-support assessments |
| `jev_ask` | Unregistered escape hatch: arbitrary state and named questions, not a registry set |

The server resolves the API key itself; client configs never embed it. Without a key (or with an open circuit breaker), tools return a successful envelope with `available: false` so agents fall back natively.

`jev_ask` accepts a `state` value and a named `questions` map (each `{type, instructions, criteria}`), both of which may be plain strings or literal JSON (an object or array) for structured content; it applies the same JSON-aware redaction and size limits as the curated tools, marks every call `unregistered: true`, and records a privacy-safe local audit line (timestamp, caller, question ids/types, byte counts, redaction rule counts — never payload text) to `<state>/jevkit/jev-ask-audit.jsonl`. Its `options` field (a bare array of Choice labels) is a **deprecated** compatibility alias for null-valued `criteria` entries; do not combine it with `criteria`, and prefer `criteria` in new integrations — `options` is removed in the next breaking release. For large context, load and redact content from a file rather than pasting it inline, the same way `jevkit ask request --file <path|->` does for a human operator.

`jev_developer_assess` dispatches to a small, versioned, opt-in set of registry-backed `developer.*` question sets built for coding-agent decision support, not autonomous execution: `developer.change-risk` (Score, low-to-critical), `developer.failure-triage` (Choice: regression, dependency-toolchain, configuration-environment, test-defect-flake, unknown), `developer.test-priority` (Choice: block, targeted-tests, full-suite, no-additional-tests), `developer.review-disposition` (Choice: block, needs-review, informational, no-finding), and `developer.release-readiness` (Noul with explicit true/false criteria). Its `state` argument is a structured object drawn from a shared field set (`diffSummary`, `affectedAreas`, `testOutput`, `environment`, `constraints`); each assessment requires its own subset and rejects unknown keys. Every call applies the same JSON-aware redaction and size limits as the curated tools, then returns the typed answer plus the registry act/gather/fallback decision, confidence, and registry version, logged to `decisions.jsonl` exactly like `jev_classify_request`. No `developer.*` answer is ever turned into an automatic code change, command, merge, deploy, or secret exposure — it is data for the caller to act on. Thresholds are uncalibrated placeholders: use `JEVKIT_SHADOW=1` to log would-have decisions before trusting `act`. For custom, one-off, or project-local questions outside this curated set, use `jev_ask` instead — a project-local addition should never bypass `jev_ask`'s redaction to reach the wire, since that is the only way a locally-defined question keeps the built-in safety guarantees.

`jevkit install` registers the MCP entry for each agent; `jevkit mcp config --merge <file>` can merge the same entry into an existing client config.

## Environment gates

| Variable | Effect |
| --- | --- |
| `JEVKIT_COMPACT=1` | Enable conservative post-tool compaction where the runtime can replace the result |
| `JEVKIT_COMPACT_SHADOW=1` (or `true`) | Measure would-have savings; do not replace agent-visible output |
| `JEVKIT_SHADOW=1` | MCP/registry decisions: log Jev answers, return fallback to callers |
| `JEVKIT_HOOK_TIMEOUT_MS` | Override hook dispatch timeout (milliseconds) |
| `JEVKIT_ENDPOINT` | Override API endpoint (see `jevkit doctor`) |
| `JEVKIT_TRANSPORT=fixture` | Offline fixture transport (no live calls) |

Recommended rollout:

```bash
export JEVKIT_COMPACT=1
export JEVKIT_COMPACT_SHADOW=1   # measure first
# …exercise the agent…
unset JEVKIT_COMPACT_SHADOW      # then enable live compaction
```

## Redaction reminder

Hooks and MCP never send unredacted text. Tune with `jevkit redact …` and read [REDACTION.md](REDACTION.md). Keys: [KEYS.md](KEYS.md).
