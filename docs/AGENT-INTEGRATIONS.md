# Agent integrations

Install hooks and MCP tools for Claude, Codex, Cursor, OpenCode, or Antigravity. For API key setup, see [API keys](KEYS.md).

- [Install and remove](#install-and-remove)
- [What the integration does](#what-the-integration-does)
- [Compaction behavior](#compaction-behavior)
- [MCP server](#mcp-server)
- [Environment settings](#environment-settings)
- [Plugin packages](#plugin-packages)

## Install and remove

Run `jevkit install` from the project where you use your coding agent:

```bash
jevkit install codex       # or claude, opencode, cursor, antigravity, all
jevkit doctor              # check the binary, hooks, and MCP setup
```

The default install adds hooks and an MCP server. It is project scoped; add `--scope user` to install for your user account. Jevkit keeps a backup of configuration it changes, and `jevkit uninstall codex` restores it. Preview changes with `jevkit install codex --dry-run`.

For SDLC runs, `jevkit sdlc integrations` shows the personal default for this
project. Each new interactive run asks whether to auto-install hooks for its
CLI agents and enable tool-output compaction, unless you select “keep this as
my default.” Scripts can set the default with `jevkit sdlc integrations
--hooks on --compaction on`; `--ask-every-run` clears it. Turning auto-install
off does not remove hooks already
installed in the project; use `jevkit uninstall AGENT --components hooks` for
those files.

When Codex project hooks are installed, `jevkit sdlc integrations` also checks
for project hook trust records. SDLC Codex invocations with hooks enabled pass
`--dangerously-bypass-hook-trust`, so those runs can use enabled hooks without
persisted trust. This flag applies to every enabled hook in the project.
Standalone Codex sessions still require review of new or changed hooks; run
`codex`, then `/hooks` to review the exact definitions. Installing the file alone
does not confirm that Codex executed it.

Use `--components hooks` or `--components mcp` when you need only one integration. The agent's existing permissions still apply.

`jevkit install claude --injection-guard` adds broad pre-tool and post-tool hooks for [prompt-injection review](SECURITY-CHECK.md). The `*` pre-tool matcher runs Jevkit before every Claude tool call, so it adds hook startup latency to each call. Codex, Cursor, and Antigravity can also use `--injection-guard` to enable their installed hooks and shell wrappers. Review mode is off by default; try a shadow policy before enforcing it.

Installed assets call a private, versioned dispatcher and fail open when Jevkit is unavailable. `hook` and `exec` are intentionally not public CLI commands.

## What the integration does

- **MCP tools** let an agent classify requests or failures, rank relevant lines, and ask typed questions. `jevkit mcp status` checks the server. Without a usable API key, tools report that Jev is unavailable so the agent can continue.
- **Output compaction** can shorten supported tool results. It is off until you set `JEVKIT_COMPACT=1`. Jevkit saves the full original output locally and includes a retrieval path in a compacted result. `JEVKIT_COMPACT_SHADOW=1` measures what it would change without altering agent-visible output.
- **Command checks** inspect supported shell calls before execution. They are a [path and command guard](SECURITY-CHECK.md), not an operating-system sandbox. OpenCode shell calls are not policy-checked in this version.
- **SDLC capability matrix**: `jevkit sdlc agents capabilities` reports, per installed CLI runtime, what jevkit's SDLC worker actually does — whether read-only execution is enforced (and why not, when it isn't), the writable-invocation approval argument, any permission-bypass flag such as Antigravity's `--dangerously-skip-permissions`, whether the pre-tool hook covers shell calls, session resume, and a CLI version obtained by actually running the binary, not just a PATH lookup.

Jevkit redacts known secrets and configured patterns locally before an API request. Pattern matching has limits; [test redaction](REDACTION.md) with the output your agents see.

## Compaction behavior

Claude Code and OpenCode compact only result shapes their native post-tool contracts allow them to replace. Codex, Cursor, and Antigravity use a different, shared mechanism: their pre-tool hook replaces an eligible shell command with Jevkit's private wrapper command. The wrapper runs the original command, captures its streams and exit status, then prints the compacted result as the command output itself. This avoids relying on undocumented post-tool output mutation.

The wrapper is fail-open for compaction and does not recursively wrap an already-wrapped command. It stores the complete original before attempting compaction, then includes the retrieval path in any compacted result. Stored output is private to Jevkit state. The classifier uses at most two requests: one for output triage and, when needed, one for salient line selection. `JEVKIT_COMPACT_GENERIC=1` enables deterministic compaction without a Jev client.

`jevkit usage --source jev` reports global Jev transport attempts by origin,
including hook calls. Its separate hook-dispatch table counts hook executions,
which may make no Jev request. For example, the standard Claude compaction hook
only considers Bash results above the 8 KiB threshold; `Read` results do not
match that compaction hook.

For Claude SDLC invocations, Jevkit asks Jev whether invocation-wide provider
prompt caching is likely to pay for a cache write. It sends only prompt size,
fingerprint, role, model, and prior cache counters. An unavailable or uncertain
answer disables caching for that invocation. `jevkit usage --source runtime`
shows enabled, disabled, and unmanaged decision counts. Other CLI runtimes have
no Jevkit provider-cache control.

Claude Code also installs a PreToolUse:Bash hook for the [security check](SECURITY-CHECK.md). Codex, Cursor, and Antigravity check eligible shell calls before rewriting them into the wrapper. OpenCode shell calls are not policy-checked in this version.

### Compaction policy and evaluation

Compaction is off until `JEVKIT_COMPACT=1` is set. Shadow mode records the
proposed closed-set disposition and byte savings without changing what the
agent sees:

```bash
export JEVKIT_COMPACT=1
export JEVKIT_COMPACT_SHADOW=1
```

Shadow decisions are recorded under the state directory in
`jevkit/decisions.jsonl`; `jevkit compact stats` summarizes them. Unset
`JEVKIT_COMPACT_SHADOW` when you want live compaction. Registered thresholds
remain uncalibrated until the labeled corpus meets the recall gate; use
`jevkit compact eval` to check the deterministic tier or
`jevkit compact eval --jev` to measure the live classifier on redacted corpus
fixtures.

Optionally create `<config-dir>/compaction.yaml` to protect or tune known
commands. Policies are declarative: they cannot run code or override
binary/source-data, redaction, confidence, or runtime-capability safeguards.

```yaml
version: 1
rules:
  - id: preserve-generated-files
    command: '^go generate'
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

Project policy lives at `.jevkit/compaction.yaml`; because it is repository
content, it may add only `never-compact` rules. The separate `JEVKIT_SHADOW=1`
MCP/registry gate forces decision tools to report fallback while logging what
Jev answered, which is useful while question sets remain uncalibrated.

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

Representative MCP assessment requests:

```json
{"assessment":"developer.change-risk","state":{"diffSummary":"widen auth token TTL from 15m to 1h","affectedAreas":["auth"],"testOutput":"go test ./internal/auth/... ok"}}
```

```json
{"assessment":"developer.failure-triage","state":{"testOutput":"FAIL TestLogin: dial tcp: i/o timeout after 30s","diffSummary":"no related changes in the last 3 commits"}}
```

The other registered assessments use the same shape with their required fields:
`developer.test-priority` and `developer.review-disposition` use `diffSummary`
and `affectedAreas`; `developer.release-readiness` uses `testOutput` and
`constraints`. Unknown fields are rejected and all values are redacted before
the request is sent.

`jevkit install` registers the MCP entry for each agent; `jevkit mcp config --merge <file>` can merge the same entry into an existing client config.

## Environment settings

### Jev model

Jevkit uses `jev-latest` by default. To select a model for all Jevkit calls,
including hooks, MCP, and SDLC routing, save it in your user config directory:

```bash
jevkit model set jev-1.13.0
jevkit model status
jevkit model clear
```

`JEVKIT_MODEL` overrides the saved model for the current process and its
children. A model supplied in an `ask request` file overrides both; that
command's `--model` flag overrides the file. Jevkit checks the name's syntax
locally; the API determines whether that model is available to your account.

### Other environment settings

| Variable | Effect |
| --- | --- |
| `JEVKIT_COMPACT=1` | Enable conservative post-tool compaction where the runtime can replace the result |
| `JEVKIT_COMPACT_SHADOW=1` (or `true`) | Measure would-have savings; do not replace agent-visible output |
| `JEVKIT_COMPACT_GENERIC=1` | Enable conservative generic head/tail compaction in the shell wrapper |
| `JEVKIT_SHADOW=1` | MCP/registry decisions: log Jev answers, return fallback to callers |
| `JEVKIT_HOOK_TIMEOUT_MS` | Override hook dispatch timeout (milliseconds) |
| `JEVKIT_SECURITY_POLICY` | Select a named security policy instead of the default (CLI: `--security-policy`) |
| `JEVKIT_SECURITY_SCORING=0/1` | Override the user-policy `jev_scoring` setting; builtin defaults to off ([security-check details](SECURITY-CHECK.md)) |
| `JEVKIT_YOLO=1` | Lift the workspace path guard (CLI: `--yolo`); killswitch and Jev checks still apply |
| `JEVKIT_ENDPOINT` | Override API endpoint (see `jevkit doctor`) |
| `JEVKIT_MODEL` | Override the saved Jev model for this process |
| `JEVKIT_TRANSPORT=fixture` | Offline fixture transport (no live calls) |

Recommended rollout:

```bash
export JEVKIT_COMPACT=1
export JEVKIT_COMPACT_SHADOW=1   # measure first
# …exercise the agent…
unset JEVKIT_COMPACT_SHADOW      # then enable live compaction
```

## Plugin packages

Generated plugin bundles live under `plugins/jevkit/<host>/`. Run `make plugins` after changing their templates. Each bundle calls the Jevkit binary and includes a bootstrap script that can suggest `go install` when the binary is missing.
