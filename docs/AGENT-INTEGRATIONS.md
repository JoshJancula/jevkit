# Connect an agent

Run `jevkit install` from the project where you use your coding agent:

```bash
jevkit install codex       # or claude, opencode, cursor, antigravity, all
jevkit doctor              # check the binary, hooks, and MCP setup
```

The default install adds hooks and an MCP server. It is project scoped; add `--scope user` to install for your user account. Jevkit keeps a backup of configuration it changes, and `jevkit uninstall codex` restores it. Preview changes with `jevkit install codex --dry-run`.

Use `--components hooks` or `--components mcp` when you need only one integration. The agent's existing permissions still apply.

## What the integration does

- **MCP tools** let an agent classify requests or failures, rank relevant lines, and ask typed questions. `jevkit mcp status` checks the server. Without a usable API key, tools report that Jev is unavailable so the agent can continue.
- **Output compaction** can shorten supported tool results. It is off until you set `JEVKIT_COMPACT=1`. Jevkit saves the full original output locally and includes a retrieval path in a compacted result. `JEVKIT_COMPACT_SHADOW=1` measures what it would change without altering agent-visible output.
- **Command checks** inspect supported shell calls before execution. They are a [path and command guard](SECURITY-CHECK.md), not an operating-system sandbox. OpenCode shell calls are not policy-checked in this version.

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
