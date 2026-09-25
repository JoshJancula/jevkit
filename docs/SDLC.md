# Adaptive SDLC runs

Jevkit routes SDLC work only to agents explicitly enrolled in the user's roster. A discovered native definition or installed CLI is inventory, not permission to use it.

## The everyday CLI path

An SDLC run moves through planning, implementation, and assessment. Jevkit
chooses an eligible enrolled agent for each step. You do not need to author a
workflow file for the built-in `feature`, `bugfix`, `review`, or `release` task
kinds.

By default, `sdlc run` saves the planner's output as `plan.md`, displays the
plan in an interactive terminal, and asks you to approve it or request changes
before implementation. Use the arrow keys and Enter to choose an action. Choosing
**Request changes** opens a text box; type the feedback and press Enter to send
it, or Escape to return to the menu. A change request returns to the planner
with the prior plan and your feedback; the revised plan appears for another review. You can
leave the run paused and return later. When input or output is redirected,
Jevkit pauses and prints the plan path; review it, then approve that exact
revision with `jevkit sdlc resume RUN_ID --approve-plan`. To use the previous
fully autonomous behavior, add `--auto` to `sdlc run`.

If you already have a complete plan, pass `--plan-file path/to/plan.md` to a
built-in `sdlc run` command. The run stores the original as `plan.md` and waits
for the same approval before implementation. `--task-file` reads a task statement and still runs a
planner; use `--plan-file` for a prepared plan. You can combine `--plan-file`
with `--task` for a short objective.

| Command | What it does | When to use it |
| --- | --- | --- |
| `sdlc agents discover` | Optional inventory of definitions and installed CLI apps | Find reusable bindings |
| `sdlc agents add` | Adds one binding and its allowed roles to your personal roster | Authorize an agent for SDLC work |
| `sdlc agents` | Shows your roster, including inactive examples | Check agent setup |
| `sdlc doctor --policy lean` | Checks policy, reach, and quorum | Before a run, or to diagnose setup |
| `sdlc run feature --task "..."` | Shows the plan and asks for approval or changes in a terminal | Normal path |
| `sdlc run bugfix --task "..." --auto` | Runs through planning, implementation, and assessment without plan approval | Autonomous path |
| `sdlc resume RUN_ID --approve-plan` | Approves the saved plan revision and continues | After reviewing `plan.md` |
| `sdlc resume RUN_ID` | Continues an active run to completion or pause | After approval or `run --step` |
| `sdlc resume RUN_ID --retry-failed` | Retries agents whose invocation failed | After you fix the error that paused the run |
| `sdlc resume RUN_ID --step` | Executes one question or agent action | Inspect progress between steps |
| `sdlc watch RUN_ID` | Attaches a read-only run-tree view | Watch a running task in another terminal |
| `sdlc logs RUN_ID --follow` | Streams saved agent output, including child runs | Inspect a CLI invocation |
| `sdlc usage [RUN_ID]` | Shows runtime usage and linked Jev calls for one run tree or all saved runs | Audit usage |
| `sdlc create NAME` | Writes an optional custom workflow YAML | Author project-specific questions and routes |

**You do not need to create a workflow or call another command before `run`.**
It checks setup, starts the task, and executes it through planning. Use
`run --step` to execute one action, then `resume RUN_ID` to continue that
active run. The terminal prompts for plan approval during normal `run` and
`resume` commands; `--step` stops after one action. A pending
specialist or delegation decision from an older run can be retried with
`resume` after its cause is addressed. A hard policy limit stops the run.
Omit `feature` to let Jevkit choose among built-ins and valid project workflows.
Name a workflow to bypass selection. An invalid project workflow reports its file name.
Use `--silent` on `run` or `resume` for only the run ID and final status; `--step`
reports the completed step. Errors still go to stderr.

## Set up agents

There is one team list: your personal `sdlc/roster.yaml`. Jevkit can assign
work only to enabled agents in that file. The commands do three different
things:

| Command | Plain meaning | Does it let an agent work? |
| --- | --- | --- |
| `jevkit sdlc agents discover` | Look at possible agents and installed CLI apps | No |
| `jevkit sdlc agents add ...` | Put one agent in your roster | Yes, if its binding works |
| `jevkit sdlc agents` | Show your roster | Shows which entries are still inactive |

You do **not** need to run `discover` first. You can add your own agent
directly, or edit the roster. A discovered agent is only a suggestion until
you add it. Adding copies its settings once; later edits to the suggestion
do not update your roster.
Jevkit does not configure an agent's MCP tools or skills.

The default `lean` run needs one planner, one implementer, and one assessor.
There are no required agent names. One reachable CLI agent can fill all three
roles, so a single roster entry is enough for the default policy. Replace
`YOUR_MODEL` with a model supported by your installed CLI:

```sh
jevkit sdlc agents add codex --model YOUR_MODEL --rubric "API endpoint changes" --role all --role-rubric planner="Plan endpoint contracts" --role-rubric assessor="Review API behavior"
jevkit sdlc agents
jevkit sdlc doctor --policy lean
jevkit sdlc run feature --task "Add rate limiting"
# Choose approve or request changes when the plan appears.
```

Run from the Git project directory. If you launch from its parent, a
`--task-file` or `--plan-file` path inside the project also identifies the repository; Jevkit
saves that project path so `resume` uses the same workspace.

`agents` creates a personal roster with four inactive examples if needed, so
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

`discover` shows two kinds of possibilities. **Named suggestions** come from
native host agent files or the optional project suggestions file; they have
an ID, binding, and rubric. Add
one with `jevkit sdlc agents add ID --role ROLE`. **CLI apps on PATH**
(`codex`, `claude`, `cursor-agent`, `opencode`) only mean that the executable is
installed. Jevkit cannot infer which models or named agents that app makes
available. Choose a model supported by your CLI and add it with
`jevkit sdlc agents add APP --model MODEL --rubric "..." --role ROLE`.
Use `--runtime APP` when you want a custom agent ID, for example `jevkit sdlc agents add security-reviewer --runtime codex --model MODEL --rubric "Audit authentication and secrets" --role security --read-only`.

There is **one personal roster**: `sdlc/roster.yaml` in your Jevkit config
directory. `jevkit sdlc agents` creates a starter file with planner,
implementer, assessor, and multi-role entries using Claude, Codex, and OpenCode as
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
the task context and each candidate's `roleRubrics` entry, falling back to `rubric`. Use `rubric` as the agent's
short “when to choose me” description, such as “Review database migrations
and query performance.” `sdlc agents` displays it. There is no separate
“pick a role” question: the current SDLC step determines the role. For an
assured run, multiple assessor entries must have independent bindings to
satisfy its quorum.

Projects may also have `.jevkit/sdlc/agents.yaml`. Think of it as a sheet of
suggestions for `discover`. Its agents do **not** appear in your team list
from `jevkit sdlc agents` until you add them, and they cannot work before
then. Keep `.jevkit/sdlc/` for project policy and custom workflows.

### Writing project agent suggestions

You only need this optional file to share suggestions with other people on
this project. To set up your own working agents, edit the personal roster
shown by `jevkit sdlc agents`, or use `jevkit sdlc agents add`.

This starter suggests nothing until you uncomment an example:

```yaml
version: 1
agents:
  # Remove the leading "# " from each of the six lines below to use this example.
#   - id: my-reviewer
#     via: runtime
#     runtime: opencode
#     model: YOUR_MODEL
#     rubric: Review database migrations and query performance.
```

Each `- id:` starts a new agent. Keep the spaces at the start of the lines;
use spaces, not tabs. Choose a unique name, choose your CLI (`claude`,
`codex`, `cursor`, or `opencode`), replace `YOUR_MODEL`, and describe when
to choose the agent in `rubric`. Copy the whole block to suggest another
agent. The uncommented example looks like this:

```yaml
version: 1
agents:
  - id: my-reviewer
    via: runtime
    runtime: opencode
    model: YOUR_MODEL
    rubric: Review database migrations and query performance.
```

After replacing `YOUR_MODEL`, run `jevkit sdlc agents discover` to see the
suggestion. Run `jevkit sdlc agents add my-reviewer --role assessor` to copy
it into your personal roster. Choose the role at this step; the project
suggestions file does not contain roles. You can add several agents with
the same role, and Jev chooses among eligible agents using their rubrics.
Changes to a project suggestion do not update a copy already in your roster.

### Personal roster format

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

The default workflow uses planner, implementer, and assessor roles. These are
lightweight routing labels for choosing a runtime and model. Native agent
definitions own specialized instructions, tools, and skills. Optional
`research`, `qa`, `security`, and `code-review` checks are off by default.
Set `specialistMode: advisory` to enable those checks when agents with those
roles are enrolled, or `required` to pause when a needed check is unavailable.
An ordinary assessor still reviews the change when optional checks are off.

`jevkit sdlc agents discover` scans project and global agent directories for
Claude, Codex, Cursor, OpenCode, and Antigravity. Named runtime suggestions use
IDs such as `claude/security-reviewer`; add one with
`jevkit sdlc agents add claude/security-reviewer --role assessor --model opus`.
Claude, OpenCode, and Antigravity support direct native agent selection in the
CLI adapter. Codex and Cursor definitions are shown for discovery, but their
named agents are not directly selected by the current CLI adapter. For those
runtimes, enroll a model and rubric scaffold and let the runtime invoke its
own native subagents during the turn.

```yaml
version: 1
specialistMode: off
minimumProfile: collaborative
maxConcurrent: 2
maxAssignments: 20
maxRevisions: 3
maxInvocationSeconds: 1800
maxRunSeconds: 21600
maxEstimatedCostUsd: 5
adaptiveBuiltinDelegation: opt-in
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

Each role can set `via`, `runtimes`, `write`, `readOnly`, `isolated`, and `writeScopes`. Set `write: false` to require enforced read-only execution. A scoped or restricted assignment is eligible only when its host or runtime adapter reports that it can enforce the restriction. Codex, Claude, Cursor, and Antigravity CLI adapters use read-only modes; OpenCode does not. None claims isolation or scoped-write enforcement. The project may set `quorums` by profile. `lean` defaults to one assessor, `collaborative` to two, and `assured` to three. Agents with different IDs but the same binding count once toward quorum.

`adaptiveBuiltinDelegation` is `off` by default. Set it to `opt-in` to allow
`run` or `start --delegate-builtins=true`, or `on` to enable automatic
built-in delegation by default. `--delegate-builtins=false` always disables it.
The choice is stored in the run ledger for resume. Authored `spawn` stages
retain their explicit targets.

Runs have hard loop and time limits. By default, a run allows at most 3 implementation revisions and 20 total agent assignments across its tree. Each CLI invocation has a 30-minute deadline, and the root run expires 6 hours after creation. A run can create at most eight children across three child levels. Custom stage workflows also have `maxSteps` (20 by default) to bound question and work transitions. Set `maxRevisions`, `maxAssignments`, `maxInvocationSeconds`, and `maxRunSeconds` in project policy to tighten or widen those limits. Hitting a limit pauses the run. `maxEstimatedCostUsd` is optional and applies when workers report an estimate.

Agent stdout and stderr are saved as private, bounded 1 MiB tails per stream.
`sdlc logs RUN_ID` includes children; filter by `--agent`, `--runtime`,
`--invocation`, or `--stream stdout|stderr|decisions`. Decision events replay
from the run ledger, including after a restart. Display is redacted by default.
`--raw` shows the locally saved original. `--follow` and `watch` only read
the ledger and never control the worker process.

`--session-strategy auto|fresh|resume|compact` is available on `run`, `start`,
and `resume`; a resume override is saved in the run. Sessions are keyed by run,
binding, and role. `auto` asks Jev when a prior session exists and records the
choice and policy fallback. Codex manual compaction uses its app server
protocol. Claude manual compaction sends `/compact` ahead of the work prompt
through the installed Claude CLI's stream JSON mode, as Ralph does. A
failed native compaction pauses the run with the error. Other runtimes resolve
`compact` to explicit resume and record that fallback.

Antigravity runs through the installed `agy` CLI. Jevkit passes the enrolled
model string unchanged, captures its conversation ID and reported usage from
stream JSON, and resumes with `--conversation ID`.

For CLI runtimes, Ctrl-C, SIGTERM, and invocation timeouts stop the runtime and subprocesses in its process group (Unix) or job object (Windows). Jevkit also cleans up those subprocesses when a runtime exits. A forced SIGKILL of Jevkit itself bypasses cleanup, so use the operating system's process-group or job termination when an immediate forced stop is required.

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
the plan, change report, or assessment. For custom stage workflows, it also asks
the authored Jev questions. CLI execution requires a Git repository to
capture the actual workspace diff.

In a terminal, `run` and `resume` display the live run view. It follows the
newest agent, shows elapsed working time and saved runtime activity, and lets
you read a navigation guide that opens with the view. Press `?` to hide or
show the guide at any time. The footer keeps the main controls visible.
Use the up/down arrows, `j`/`k`, or mouse wheel over the agent pane to browse activity;
Page Up/Down jumps five entries. `J` and `K` scroll the selected message text.
Press `n` to switch to the next agent and `a` to show every agent. Press `l`
to show or hide logs, `t` to switch to the latest tool result, and `d` to
expand decision details.

The live TUI uses the terminal's alternate screen so wheel
scrolling does not expose old dashboard frames in shell scrollback. Codex file
change events show project-relative paths and change kinds. The pane opens on
the latest activity and keeps the run
status, pause cause, and recovery keys visible in narrow terminals.
Tool output preserves code indentation and is bounded in the live pane; use
`sdlc logs RUN_ID` for the full saved stream. If a run pauses, the view shows
the cause and offers explicit retry choices: `r` for automatic session choice,
`f` for fresh, `s` for resume, and `c` for compact. Press `q` to leave it
paused. Runs redirected to a pipe keep plain output; `--silent` keeps its
short status output. `sdlc watch RUN_ID` offers the same saved view without
driving or resuming the run.

When `run` or `resume` stops, Jevkit prints a summary after the live view
closes. It shows the saved state and pause cause, recent completed actions,
agent runtime and linked Jev usage totals, a next action, and the logs command.
Unknown token counts stay explicit. The same summary appears with plain output
when stdout is redirected. A paused run includes a recovery command when the
saved state allows it.

To inspect progress between steps, start with `sdlc run feature --task "..." --step`.
The output includes a run ID. Use `sdlc resume RUN_ID --step` for one more
action, or `sdlc resume RUN_ID` to continue until completion or pause.
After fixing the error that paused a run on an agent failure, run
`sdlc resume RUN_ID --retry-failed`. It clears only the failed-agent exclusions,
keeps saved artifacts and successful assessments, and resumes the paused stage.
Run limits still apply. The flag is rejected unless the run paused after an
agent failure or a review that needs reassessment.

If files change during assessment, the run saves the review and a bounded list
of observed paths without claiming which process edited them. A
`changes-required` review continues to implementation and passes those paths
and findings to the implementer. An `approved` review pauses so the changed
workspace can be assessed again with `sdlc resume RUN_ID --retry-failed`.
Recovery checks the saved invocation, reviewer binding, and `patch.diff`
digest before reusing an interrupted review. If the digest changed, the run
pauses and requires a new assessment.

`jevkit usage` separates Jev calls from agent runtime invocations. Use
`--source all|jev|runtime` and `--format json` for a version 2 report. Jev
cost is estimated from configured rates; runtime cost appears only when a
runtime reports it. Unknown token counts stay unknown. Older runtime ledgers
remain readable; Jev calls made before recording was enabled cannot be
reconstructed.

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
assigned diff digest. Report `invocation-failed` when an agent could not be
invoked and left the workspace unchanged. Jevkit excludes that agent and its
binding, then selects another eligible agent for the same role or pauses if
none remain. `auth-failed` also excludes the failed runtime. CLI runs report
these outcomes automatically. Native subagents and host-self need a host
executor. Existing `drive` scripts still work, but new CLI use
should use `run` and `resume`.

The CLI driver saves worker explanations under `artifacts/responses/`. For a
CLI implementer it captures private Git trees before and after the invocation
and saves a bounded change report as `patch.diff`. The report contains changed
paths, object hashes, and text patch excerpts; binary payloads are omitted.
Pre-existing workspace changes do not enter that report. Reports from later
implementation invocations are appended for assessment. Host executors supply
their own changed artifact. Tests use fake reach and executors as well as
fake CLI binaries for the worker path.
