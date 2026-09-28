# API keys

Jevkit needs a TypeSafe API key for live Jev requests. The normal setup is `jevkit key set`, which uses a hidden terminal prompt and stores the key in your OS keychain when available. Status commands never print the key.

```bash
jevkit key set       # store a key
jevkit key status    # show which source wins, never the value
jevkit key test      # make a live acceptance check
jevkit key clear     # remove Jevkit-managed stored credentials
```

For a vault or secret helper, use `jevkit key set --command 'op read "op://Private/Typesafe/credential"'`. Do not pass the secret itself as a command argument.

## Where Jevkit looks

The first usable source wins:

| Priority | Source |
| --- | --- |
| 1 | `JEVKIT_API_KEY`, then `TYPESAFE_API_KEY` |
| 2 | `<workspace>/.env` (parsed as data, never run as a script) |
| 3 | A configured credential command |
| 4 | OS keychain |
| 5 | A private file under the config directory |

A configured backend that fails stops resolution; only an unavailable OS keychain is skipped. Jevkit stores backend selection in a private `jev-credentials.json` file. It does not write a key under the workspace.

The config directory is `$JEVKIT_CONFIG_DIR`, then `$JEVKIT_CONFIG_HOME`, then your user config directory's `jevkit` folder (typically `~/.config/jevkit`). State such as usage and audit logs goes to `$JEVKIT_STATE_DIR`, then `$XDG_STATE_HOME/jevkit`, then `~/.local/state/jevkit`.

`jevkit key clear` does not unset shell variables or edit `.env`. Prefer the keychain or a credential command for long-lived keys; use an environment variable in short-lived CI jobs. Jevkit's [redaction rules](REDACTION.md) apply to question text, while the key travels as the HTTP credential.
