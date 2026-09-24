# Adaptive SDLC runs

Jevkit routes SDLC work only to agents explicitly enrolled in the user's roster. A discovered native definition or installed CLI is inventory, not permission to use it.

## The everyday CLI path

An SDLC run moves through planning, implementation, and assessment. Jevkit
chooses an eligible enrolled agent for each step. You do not need to author a
workflow file for the built-in `feature`, `bugfix`, `review`, or `release` task
kinds.

| Command | What it does | When to use it |
| --- | --- | --- |
| `sdlc agents discover` | Optional inventory of definitions and installed CLI apps | Find reusable bindings |
| `sdlc agents add` | Adds one binding and its allowed roles to your personal roster | Authorize an agent for SDLC work |
| `sdlc agents` | Shows your roster, including inactive examples | Check agent setup |
| `sdlc doctor --policy lean` | Checks policy, reach, and quorum | Before a run, or to diagnose setup |
| `sdlc run feature --task "..."` | Starts and executes a new task | Normal path |
| `sdlc resume RUN_ID` | Continues an active run to completion or pause | After `run --step` |
| `sdlc resume RUN_ID --step` | Executes one question or agent action | Inspect progress between steps |
| `sdlc create NAME` | Writes an optional custom workflow YAML | Author project-specific questions and routes |

**You do not need to create a workflow or call another command before `run`.**
It checks setup, starts the task, and executes it. Use `run --step` to execute
one action, then `resume RUN_ID` to continue that active run. A run paused by a policy
limit or failure is stopped; address the cause and start a new task.
Omit `feature` to let Jevkit choose a built-in task kind; name a custom stage
workflow explicitly when you want its authored questions.

## Set up agents

The default `lean` run needs one planner, one implementer, and one assessor.
There are no required agent names. One reachable CLI agent can fill all three
roles, so a single roster entry is enough for the default policy. Replace
`YOUR_MODEL` with a model supported by your installed CLI:

```sh
jevkit sdlc agents add codex --model YOUR_MODEL --rubric "Handle general repository work" --role all
jevkit sdlc agents
jevkit sdlc doctor --policy lean
jevkit sdlc run feature --task "Add rate limiting"
```

`agents` creates a personal roster with three inactive examples if needed, so
its displayed file link opens a real file. These are YAML entries with
`disabled: true`; they cannot receive work. Choose each CLI's model and set
`disabled: false` to enroll an example, or use `add` to create another agent.
`add` also creates the file if needed.
`agents` shows which CLI apps are on your PATH; `doctor --policy lean` checks
whether the enrolled agent can actually perform each role under project policy.
For a discovered named definition, use `jevkit sdlc agents add ID --role ROLE`;
Jevkit copies its binding and rubric. Add more independently bound
assessors for `collaborative` (two) or `assured` (three) policies.

Native subagents and `host-self` need to be added too. Use `jevkit sdlc agents add host-self --role planner` for a host that exposes itself, or add a discovered native ID with `--role planner`. The CLI driver cannot invoke either; `sdlc doctor --driver host --host-native NAME --host-self` is a diagnostic for the capabilities a host integration would expose. A host's own permission controls still apply. Jevkit never treats a discovered native definition as proof that the host can enforce read-only execution, write scopes, or isolation.

`discover` has two different lists. **Named definitions** come from native host
agent files or a project catalog; they have an ID, binding, and rubric. Enroll
one with `jevkit sdlc agents add ID --role ROLE`. **CLI apps on PATH**
(`codex`, `claude`, `cursor-agent`, `opencode`) only mean that the executable is
installed. Jevkit cannot infer which models or named agents that app makes
available. Choose a model supported by your CLI and add it with
`jevkit sdlc agents add APP --model MODEL --rubric "..." --role ROLE`.
Use `--runtime APP` when you want a custom agent ID, for example `jevkit sdlc agents add my-reviewer --runtime codex --model MODEL --rubric "Review code" --role assessor`.

There is **one personal roster**: `sdlc/roster.yaml` in your Jevkit config
directory. `jevkit sdlc agents` creates a starter file with planner,
implementer, and assessor entries using Claude, Codex, and OpenCode as
examples. Each starts with `disabled: true` and `model: YOUR_MODEL`. Set the
runtime and model for each entry, then change
`disabled` to `false` to enroll it. `agents add` can add other agents to the
same file. `jevkit sdlc doctor --policy lean` checks whether all three roles
can run. You may edit that YAML directly.
`discover` only prints what it finds; it never writes a roster.

The three roles are minimum coverage, not a three-agent limit. You can enroll
many specialists under the same role, and one agent may have several roles.
At each planning, implementation, or assessment step, Jevkit filters the
roster to agents allowed for that role by your project policy and available
through the current driver. If several remain, Jev chooses an agent using
the task context and each candidate's `rubric`. Use `rubric` as the agent's
short “when to choose me” description, such as “Review database migrations
and query performance.” `sdlc agents` displays it. There is no separate
“pick a role” question: the current SDLC step determines the role. For an
assured run, multiple assessor entries must have independent bindings to
satisfy its quorum.

Projects may also have `.jevkit/sdlc/agents.yaml`. That is an optional
source of suggested agent definitions for `discover`, not another roster.
It never authorizes work. Adding one of its IDs copies its binding into
your personal roster and adds the roles you select.

The user roster is `${XDG_CONFIG_HOME:-~/.config}/jevkit/sdlc/roster.yaml` (or the configured Jevkit config directory). It is editable YAML with `version: 1` and an `agents` list. Each entry needs a unique `id`, `roles`, `rubric`, `via`, and a native `subagent` or CLI `runtime` and `model` binding. Use `--agent NAME` (or `agent: NAME` in YAML) to distinguish named agents within one CLI runtime. For example:

```yaml
version: 1
agents:
  - id: cursor-reviewer
    roles: [assessor]
    rubric: Assess a diff against its plan.
    via: runtime
    runtime: cursor
    model: YOUR_REVIEW_MODEL
```

## Project policy

Create `.jevkit/sdlc/policy.yaml` to narrow the default policy. The default permits all three roles to use enrolled host-self, native, or CLI agents, but grants no enrollment. The default profile is `lean`, the concurrency limit is 3, and the assignment limit is 20.

```yaml
version: 1
minimumProfile: collaborative
maxConcurrent: 2
maxAssignments: 20
maxRevisions: 3
maxInvocationSeconds: 1800
maxRunSeconds: 21600
maxEstimatedCostUsd: 5
roles:
  planner:
    via: [native, runtime]
    write: true
  implementer:
    via: [runtime]
    runtimes: [cursor, codex]
    write: true
  assessor:
    via: [native, runtime]
    write: true
```

Each role can set `via`, `runtimes`, `write`, `readOnly`, `isolated`, and `writeScopes`. Set `write: false` to require enforced read-only execution. A scoped or restricted assignment is eligible only when its host or runtime adapter reports that it can enforce the restriction. The built-in CLI reach probe detects binaries but does not claim read-only, isolation, or scoped-write enforcement. The project may set `quorums` by profile. `lean` defaults to one assessor, `collaborative` to two, and `assured` to three. Agents with different IDs but the same binding count once toward quorum.

Runs have hard loop and time limits. By default, a run allows at most 3 implementation revisions and 20 total agent assignments. Each CLI invocation has a 30-minute deadline, and the run expires 6 hours after creation. Custom stage workflows also have `maxSteps` (20 by default) to bound question and work transitions. Set `maxRevisions`, `maxAssignments`, `maxInvocationSeconds`, and `maxRunSeconds` in project policy to tighten or widen those limits. Hitting a limit pauses the run. `maxEstimatedCostUsd` is optional and depends on workers reporting an estimate.

For native agents invoked through a host, Jevkit stops further routing and rejects late reports after the run deadline. The host must also enforce its own timeout on an agent invocation; Jevkit cannot kill a process owned by the host.

## Run a task

Use `run` for the ordinary path:

```sh
jevkit sdlc list
jevkit sdlc doctor --policy collaborative
jevkit sdlc run feature --task "Add rate limiting" --policy collaborative
```

`run` checks setup, saves the task, prints the run ID, and performs the work:
it chooses eligible enrolled agents, launches their CLI runtimes, and saves
the plan, code diff, or assessment. For custom stage workflows, it also asks
the authored Jev questions. CLI execution requires a Git repository to
capture the actual workspace diff.

To inspect progress between steps, start with `sdlc run feature --task "..." --step`.
The output includes a run ID. Use `sdlc resume RUN_ID --step` for one more
action, or `sdlc resume RUN_ID` to continue until completion or pause.

`sdlc run` checks enrollment, project limits, driver reach, and assessor quorum before starting a task. A requested profile is never downgraded. Built-in task kinds `feature`, `bugfix`, `review`, and `release` use the adaptive loop. Custom stage workflows route the same enrolled roles through questions and work stages.

## Optional project workflows

A **custom workflow** is a project YAML file that defines stage questions,
answer choices, routes, fallback decisions, and standard SDLC work roles. A
choice can route to a `spawn` stage that runs a built-in task kind such as
`bugfix` or another project workflow. It
differs from the built-in task kinds by letting the project author its own
decisions. It does not enroll workers. See the [custom workflow guide](SDLC-WORKFLOWS.md)
for a worked YAML example and the stage rules.

`jevkit sdlc create custom-review` writes a starter at
`.jevkit/sdlc/custom-review.yaml`. Edit its stages, then run
`jevkit sdlc validate custom-review` and `jevkit sdlc explain custom-review`.
Run it with `jevkit sdlc run custom-review --task "..."`. The starter asks a scope question,
then plans, implements, and assesses. `create` rejects built-in task names.

`sdlc list` shows four task choices and any project YAML workflows. Its
`ready`, `setup`, and `blocked` labels refer to the lean policy; run
`sdlc doctor --policy NAME` for detailed setup problems or another policy.
Existing project stage files named like task kinds take precedence when named
in `sdlc run`.

`sdlc explain feature` shows the adaptive plan, implement, and assess flow. `sdlc explain custom-review` shows the authored questions, choices, work roles, and routes.

## Host integrations

The low-level `start`, `next`, and `report` commands are for integrations that
execute agents themselves, including native subagents. They are absent from
the everyday command help but remain available to existing scripts. `start`
saves a run without executing it. `next` reserves an eligible assignment and
returns its agent and invocation IDs as JSON. The host executes that agent,
then `report` records its result. A custom workflow question can be advanced
with `resume RUN_ID --step`.

```sh
jevkit sdlc start feature --task "Add rate limiting"
jevkit sdlc next RUN_ID
jevkit sdlc report RUN_ID --invocation INVOCATION_ID --agent AGENT_ID --outcome planned --file plan.md
```

The next assignment is the work the run currently needs: planning,
implementation, or assessment. `next` does not launch an agent. A planned
result stores a digest of `plan.md`; a changed result stores a digest of
`patch.diff`. Assessors report `approved` or `changes-required` against the
assigned diff digest. `auth-failed` excludes that binding from the run; work
routes to another eligible agent or pauses. Native subagents and host-self
need a host executor. Existing `drive` scripts still work, but new CLI use
should use `run` and `resume`.

The CLI driver saves worker explanations under `artifacts/responses/` and
captures the Git diff as the exact `patch.diff` for assessment. Tests use
fake reach and executors; they do not launch real agents.
