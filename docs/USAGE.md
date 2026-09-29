# Usage and local state

Jevkit records measured Jev calls and agent runtime invocations in local files only. Status and usage commands never invent token or cost figures: unknown counts stay unknown.

## Where state lives

| Kind | Path |
| --- | --- |
| State root | `$JEVKIT_STATE_DIR`, else `$XDG_STATE_HOME/jevkit`, else `~/.local/state/jevkit` (Windows: `%LOCALAPPDATA%\jevkit`) |
| Shared usage log | `<state>/usage.jsonl` (every Jev call and SDLC run) |
| SDLC runs | `<state>/sdlc/runs/<run-id>/` |
| Config (keys, roster) | `$JEVKIT_CONFIG_DIR` / `$JEVKIT_CONFIG_HOME` / `~/.config/jevkit` — separate from state |

External agent session files (Claude, Codex, Cursor, OpenCode, Antigravity) stay in each CLI's own store. Jevkit only keeps opaque session IDs inside `run.json`.

## Summarize usage

```bash
jevkit usage                          # Jev + agent runtime totals
jevkit usage --source jev|runtime|all
jevkit usage --format json
jevkit sdlc usage                     # all saved run trees
jevkit sdlc usage RUN_ID              # one run tree
```

Runtime reports separate **input**, **output**, **cache-read**, and **cache-creation** tokens when a provider emits them, plus supplied cost when reported. Cache counters are never folded into input. Runtimes that omit a field show it as unknown rather than zero. Provenance labels (for example `claude.result`) name the stream event that supplied the counts.

Prompt assembly keeps a stable prefix first so providers can reuse cache across turns. Jevkit reports prefix fingerprint and byte counts for layout telemetry; it does **not** store prompt contents as a local cache.

## Storage you can reclaim

`jevkit sdlc runs` shows total bytes against the informational SDLC quota (default 5 GiB). Free space with preview-then-apply retention commands — never automatic deletion:

```bash
jevkit sdlc prune --older-than 720h              # preview
jevkit sdlc prune --older-than 720h --apply      # delete inactive trees
jevkit sdlc prune --logs-only --older-than 168h --apply
jevkit sdlc delete RUN_ID --apply
```

Logs-only prune removes diagnostic streams and marks invocations pruned; plans, verification receipts, status, and usage totals remain. See [SDLC storage and logs](SDLC.md#saved-runs-storage-and-logs) for quotas, truncation markers, and retention behavior.
