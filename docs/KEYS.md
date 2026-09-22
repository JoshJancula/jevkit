# API keys

How jevkit finds and stores the Typesafe API key. The key is never printed by status commands, never written into agent hook or MCP config, and never accepted as a positional CLI argument.

## Resolution order

First usable source wins. A configured backend that **fails** does not fall through to a later one, except that an **unavailable** OS keychain is skipped silently.

| # | Source | How it is set | Notes |
| --- | --- | --- | --- |
| 1 | Environment | `JEVKIT_API_KEY`, then `TYPESAFE_API_KEY` | Highest priority; good for CI |
| 2 | Env file | `<workspace>/.env` | Parsed for the key line only; **never sourced** as a shell script |
| 3 | Credential command | `jevkit key set --command '…'` | Command stdout is the key; no secret stored on disk |
| 4 | OS keychain | `jevkit key set` (default) | Service `jevkit`, account `TYPESAFE_API_KEY` |
| 5 | File | Fallback when no keychain | `0600` file under the config dir |

Backend selection for command / keychain / file lives in `jev-credentials.json` (mode `0600`) in the config directory. Keys are never written under the workspace.

## Config and state directories

| Purpose | Resolution |
| --- | --- |
| Config dir (credentials, key file, user `redact.yaml`) | `$JEVKIT_CONFIG_DIR`, else `$JEVKIT_CONFIG_HOME`, else `<user config>/jevkit` (e.g. `~/.config/jevkit`) |
| State dir (usage, audit, breaker, shadow logs) | `$JEVKIT_STATE_DIR`, else `$XDG_STATE_HOME/jevkit`, else `~/.local/state/jevkit` |

Credential command timeout defaults to 4s; override with `JEVKIT_KEY_TIMEOUT_MS`.

## Commands

```bash
# Store (terminal: hidden prompt; non-TTY: read stdin)
jevkit key set

# Store a vault/helper command instead of the secret itself
jevkit key set --command 'op read "op://Private/Typesafe/credential"'

# Show which source would win (never the key value)
jevkit key status

# One live acceptance check (or fixture transport offline)
jevkit key test

# Remove keychain entry, key file, and credential command selection
jevkit key clear
```

Refuse patterns that put the secret in argv (they are rejected):

```bash
# wrong — do not do this
# jevkit key set sk-...
```

Pipe instead:

```bash
printf '%s' "$TYPESAFE_API_KEY" | jevkit key set
```

## Migrate from ralph

If you already configured ralph’s jev integration:

```bash
jevkit migrate-from-ralph
jevkit migrate-from-ralph --force
```

This copies ralph’s credential command (`~/.config/ralph/jev-credentials.json`, or `$RALPH_CONFIG_HOME` / `$XDG_CONFIG_HOME/ralph`) and the `ralph.jev` keychain entry (or ralph’s `0600` key file) into jevkit. Ralph’s copy is left untouched. Without `--force`, the command refuses to overwrite an existing jevkit key.

## Usage of the key

Once resolved, the same store feeds:

- `jevkit key test`
- `jevkit exec` / hooks when `JEVKIT_COMPACT=1`
- `jevkit mcp start` (lazy resolve; tools return `available: false` if missing)

Check the local picture without sending the key anywhere:

```bash
jevkit doctor
jevkit mcp status
```

## Security notes

- Prefer keychain or a credential command over a plaintext file.
- Prefer `JEVKIT_API_KEY` in ephemeral environments over committing `.env`.
- Redaction still runs before any payload reaches the API; the key travels only as the HTTP credential, never inside the classified text. See [REDACTION.md](REDACTION.md).
- `jevkit key clear` removes every stored backend jevkit manages; it does not unset your shell environment or edit `.env`.
