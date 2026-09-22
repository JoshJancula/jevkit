# Agents

How coding agents connect to jevkit: install, plugins, compaction mechanisms, MCP tools, and environment gates.

## Install (hooks + MCP)

From a project directory (default `--scope project`):

```bash
jevkit install claude
jevkit install cursor
jevkit install opencode
jevkit install antigravity
jevkit install codex
jevkit install all
```

Useful flags:

```bash
jevkit install claude --dry-run
jevkit install cursor --scope user
jevkit install claude --binary /usr/local/bin/jevkit
jevkit uninstall claude
jevkit doctor
```

Install is idempotent and marker-based. The first write keeps a `.jevkit-original` backup so uninstall can restore byte-exact config. `jevkit doctor` reports whether each agent binary is on `PATH` and whether hooks/MCP look installed.

## Plugin packages

Distributable packages live under `plugins/jevkit/<host>/` (Claude Code, Cursor, Antigravity, OpenCode). Regenerate from templates with:

```bash
make plugins
```

Each package registers hooks/MCP that call the `jevkit` binary and ships `shared/jevkit-plugin-bootstrap.sh`, which probes `PATH` and prints (or can apply) a pinned `go install …` remediation when `jevkit` is missing.

## Capability matrix

| Agent | CLI name | Config / registration | Compaction path | Output replace |
| --- | --- | --- | --- | --- |
| Claude Code | `claude` | `.claude/settings.json` hooks | PostToolUse Bash → compact | Yes (`updatedToolOutput`), success only |
| Cursor | `cursor` | `.cursor/hooks.json` | PreToolUse Shell → `jevkit exec`; post-tool for native/MCP | Shell: no; Read/Grep/Glob/MCP: yes |
| Antigravity | `antigravity` | `.agents/hooks.json` | PreToolUse `run_command` → `jevkit exec` | No |
| OpenCode | `opencode` | `.opencode/plugins/*.ts` | `tool.execute.before` → `jevkit exec` | Unproven |
| Codex | `codex` | `.codex/hooks.json` | PreToolUse Bash / `command_execution` → `jevkit exec` | Unproven |

**Why `jevkit exec`?** No agent supplies a reliable exit code in PostToolUse. The wrapper runs the command itself, sees the exit status, compacts, and prints the result—so failure-aware ranking and preserve-line checks work everywhere PreToolUse rewrite is available.

Spike-level payload notes and fixtures: [AGENT-CAPABILITIES.md](AGENT-CAPABILITIES.md).

## Hook CLI

Install writes commands of the form `jevkit hook <agent> <event>`. Events:

| Event | Meaning |
| --- | --- |
| `pre-tool` | Rewrite / allow a tool call |
| `post-tool` | Compact or observe tool output |
| `stop` | Stop / end-of-turn hooks |

Adapters always **fail open**: garbage input, panics, timeouts, and protocol-version mismatches exit 0 with a valid allow/empty response so the agent is never blocked.

Manual dry-run (stdin JSON → stdout JSON):

```bash
jevkit hook claude post-tool < payload.json
jevkit hook cursor pre-tool < payload.json
```

## Exec wrapper

```bash
jevkit exec -- make test
```

Runs the command, then optionally compacts combined stdout/stderr. Gated by environment (below). Source-output families such as `git diff`, `rg`, `ls`, and `find` hard-passthrough.

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
| `jev_ask` | Ask a registered question set |

The server resolves the API key itself; client configs never embed it. Without a key (or with an open circuit breaker), tools return a successful envelope with `available: false` so agents fall back natively.

`jevkit install` registers the MCP entry for each agent; `jevkit mcp config --merge <file>` can merge the same entry into an existing client config.

## Environment gates

| Variable | Effect |
| --- | --- |
| `JEVKIT_COMPACT=1` | Enable compaction in `exec` and post-tool adapters |
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
