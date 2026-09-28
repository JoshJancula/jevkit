# Agent workflows

Jevkit can route a coding task through planning, implementation, and assessment. It uses only agents you enroll in your personal roster. Finding an installed agent or a project suggestion does not enroll it.

## Set up once

From a Git project directory, add a CLI agent with a model supported by that CLI. One agent can cover the three roles needed by the default `lean` policy:

```bash
jevkit sdlc agents add codex --model YOUR_MODEL --rubric "General coding work" --role all
jevkit sdlc doctor --policy lean
```

`jevkit sdlc agents` shows your roster. `jevkit sdlc agents discover` is optional inventory; it does not grant permission to use an agent. You can enroll separate planners, implementers, and assessors later.

## Run a task

```bash
jevkit sdlc run feature --task "Add rate limiting"
```

Built-in task kinds are `feature`, `bugfix`, `review`, and `release`. The run saves the plan and asks you to approve it or request changes before implementation. If the command has no interactive terminal, it pauses and prints the plan path; review it, then run `jevkit sdlc resume RUN_ID --approve-plan`. Use `--auto` only when you want to skip plan approval.

If you already have a complete plan, pass `--plan-file path/to/plan.md`. Use `--task-file` for a task statement that still needs planning.

## Watch or resume

| Command | Use it to |
| --- | --- |
| `jevkit sdlc watch RUN_ID` | Watch a saved run without controlling it |
| `jevkit sdlc logs RUN_ID --follow` | Read agent output |
| `jevkit sdlc resume RUN_ID` | Continue a paused run |
| `jevkit sdlc resume RUN_ID --step` | Run one action at a time |
| `jevkit sdlc usage RUN_ID` | See runtime usage and linked Jev calls |

`jevkit sdlc doctor --policy lean` explains missing agents or permissions before a run. Runs stop at policy limits and can pause after an agent failure; the [SDLC reference](SDLC-REFERENCE.md) covers recovery commands, roster and project policy, profiles, and host integrations.

For project-specific questions and routes, see [custom workflows](SDLC-WORKFLOWS.md). You do not need a custom workflow for the built-in task kinds.
