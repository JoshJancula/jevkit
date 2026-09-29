# Agent workflows

Jevkit can route a coding task through planning, implementation, verification, and assessment. It uses only agents you enroll in your personal roster. Finding an installed agent or a project suggestion does not enroll it.

Each new interactive SDLC run asks whether to install Jevkit hooks for its CLI
agents and enable Jevkit tool-output compaction. Choose “keep this as my
default” to stop the prompts for this project. For scripted runs, set a default
with `jevkit sdlc integrations --hooks on --compaction on`.
This is separate from native session compaction via `--session-strategy compact`.

## Set up once

From a Git project directory, add a CLI agent with a model supported by that CLI. One agent can cover the three roles needed by the default `lean` policy:

```bash
jevkit sdlc agents add codex --model YOUR_MODEL --rubric "General coding work" --role all
jevkit sdlc doctor --policy lean
jevkit sdlc agents capabilities   # tested per-runtime read-only / write boundaries
```

`jevkit sdlc agents` shows your roster. `jevkit sdlc agents discover` is optional inventory; it does not grant permission to use an agent. You can enroll separate planners, implementers, and assessors later.

## Run a task

```bash
jevkit sdlc run feature --task "Add rate limiting"
```

Built-in task kinds are `feature`, `bugfix`, `review`, and `release`. The planner may write `plan.md`, `checks.json`, and `subtasks.json`. The run pauses for plan approval before implementation. If the command has no interactive terminal, it prints the plan path; review the artifacts, then run `jevkit sdlc resume RUN_ID --approve-plan`. Use `--auto` only when you want to skip plan approval.

Planner-proposed argv checks still need an explicit authorization step (`jevkit sdlc resume RUN_ID --authorize-checks`). `--auto` does not authorize commands. After implementation, Jevkit runs those authorized checks as a supervisor-owned verification gate before assessors run. Passing, failing, timed-out, and stale (invalidated) receipts live under the run's artifacts; a failure returns a bounded summary to the implementer for repair, then reassessment.

If you already have a complete plan, pass `--plan-file path/to/plan.md`. Use `--task-file` for a task statement that still needs planning.

## Saved runs, storage, and logs

SDLC state lives under `$JEVKIT_STATE_DIR`, else `$XDG_STATE_HOME/jevkit`, else `~/.local/state/jevkit` (Windows: `%LOCALAPPDATA%\jevkit`). Each run is `<state>/sdlc/runs/<run-id>/` with `run.json`, `artifacts/`, and `logs/`. Project workflow YAML stays in `.jevkit/sdlc/` in the repo.

Default storage quotas: **5 GiB** total for all saved SDLC runs (`JEVKIT_SDLC_STORAGE_QUOTA_BYTES`), and **256 MiB** per run tree including children (`JEVKIT_SDLC_RUN_TREE_QUOTA_BYTES`). Hitting a quota pauses the run; Jevkit never deletes data on its own. Per-invocation stdout, stderr, and combined `lines.jsonl` each keep a rolling **1 MiB** tail (`JEVKIT_SDLC_LOG_TAIL_BYTES`). Rolled output is marked `[earlier output truncated: N bytes omitted]`; `sdlc show` / `sdlc logs` surface that count.

| Command | Use it to |
| --- | --- |
| `jevkit sdlc runs` | List saved runs, sizes, and quota use |
| `jevkit sdlc show RUN_ID` | Inspect one run's status, artifacts, and log availability |
| `jevkit sdlc logs RUN_ID` | Read agent output (redacted by default; `--raw` for saved original) |
| `jevkit sdlc delete RUN_ID` | Preview deleting a run tree; add `--apply` to delete |
| `jevkit sdlc prune --older-than 720h` | Preview pruning inactive runs; add `--apply` to delete |
| `jevkit sdlc prune --logs-only --older-than 168h --apply` | Drop diagnostic streams only; keep plans, receipts, status, usage |
| `jevkit sdlc watch RUN_ID` | Watch a saved run without controlling it |
| `jevkit sdlc resume RUN_ID` | Continue a paused run |
| `jevkit sdlc resume RUN_ID --step` | Run one action at a time |
| `jevkit sdlc usage RUN_ID` | See runtime tokens, tool calls, and linked Jev calls |

## Fan-out and sequential work

When the approved `subtasks.json` graph has independent work and isolation is available, Jevkit can run implementers in parallel worktrees. It stays **sequential** when subtasks depend on each other, share writable paths without isolation, isolation/worktrees are unavailable, or only one writable slot may run at a time. Fan-out can finish wall-clock sooner; it does **not** always use fewer tokens or lower cost—each subtask still incurs its own usage. Inspect subtask artifacts with `jevkit sdlc show RUN_ID` (lists `subtasks.json`, patches, `integration/decision.json`) and `jevkit sdlc logs RUN_ID --agent …`.

`jevkit sdlc doctor --policy lean` explains missing agents or permissions before a run. Runs stop at policy limits and can pause after an agent failure; the [SDLC reference](SDLC-REFERENCE.md) covers recovery, roster and project policy, profiles, storage contracts, verification receipts, and host integrations.

For project-specific questions and routes, see [custom workflows](SDLC-WORKFLOWS.md). You do not need a custom workflow for the built-in task kinds.
