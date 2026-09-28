# SDLC reference

Start with [Agent workflows](SDLC.md) to enroll an agent and run a task. This page covers roster files, project policy, runtime behavior, recovery, and host integrations.

## Enrollment details

The personal `sdlc/roster.yaml` authorizes agents. `sdlc agents discover` only lists suggestions and installed CLI apps; it never enrolls them. Native subagents and `host-self` also require enrollment and a host driver. The default `lean` policy needs a planner, implementer, and assessor; one enrolled CLI binding may fill all three roles.

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
