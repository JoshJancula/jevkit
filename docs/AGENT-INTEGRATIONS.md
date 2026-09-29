# Connect an agent

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

Use `--components hooks` or `--components mcp` when you need only one integration. The agent's existing permissions still apply.

`jevkit install claude --injection-guard` adds broad pre-tool and post-tool hooks for [prompt-injection review](SECURITY-CHECK.md). The `*` pre-tool matcher runs Jevkit before every Claude tool call, so it adds hook startup latency to each call. Codex, Cursor, and Antigravity can also use `--injection-guard` to enable their installed hooks and shell wrappers. Review mode is off by default; try a shadow policy before enforcing it.

## What the integration does

- **MCP tools** let an agent classify requests or failures, rank relevant lines, and ask typed questions. `jevkit mcp status` checks the server. Without a usable API key, tools report that Jev is unavailable so the agent can continue.
- **Output compaction** can shorten supported tool results. It is off until you set `JEVKIT_COMPACT=1`. Jevkit saves the full original output locally and includes a retrieval path in a compacted result. `JEVKIT_COMPACT_SHADOW=1` measures what it would change without altering agent-visible output.
- **Command checks** inspect supported shell calls before execution. They are a [path and command guard](SECURITY-CHECK.md), not an operating-system sandbox. OpenCode shell calls are not policy-checked in this version.
- **SDLC capability matrix**: `jevkit sdlc agents capabilities` reports, per installed CLI runtime, what jevkit's SDLC worker actually does — whether read-only execution is enforced (and why not, when it isn't), the writable-invocation approval argument, any permission-bypass flag such as Antigravity's `--dangerously-skip-permissions`, whether the pre-tool hook covers shell calls, session resume, and a CLI version obtained by actually running the binary, not just a PATH lookup.

Jevkit redacts known secrets and configured patterns locally before an API request. Pattern matching has limits; [test redaction](REDACTION.md) with the output your agents see.

## Useful settings

```bash
jevkit model status                 # see the selected Jev model
jevkit model set jev-1.13.0        # optional model selection
export JEVKIT_COMPACT=1            # opt in to compaction
export JEVKIT_COMPACT_SHADOW=1     # measure before enabling live changes
jevkit compact stats
```

Unset `JEVKIT_COMPACT_SHADOW` when you want live compaction. The model defaults to `jev-latest`; `JEVKIT_MODEL` overrides the saved selection for one process.

For runtime-specific hook behavior, MCP tool schemas, compaction policies, and every environment setting, see the [agent reference](AGENT-REFERENCE.md). For keys, see [API keys](KEYS.md).
