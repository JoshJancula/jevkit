# SDLC agent workflows

Jevkit can route a coding task through planning, implementation, verification, and assessment. It uses only agents you enroll in your personal roster. Finding an installed agent or a project suggestion does not enroll it.

- [Set up once](#set-up-once)
- [Run a task](#run-a-task)
- [Live run view](#live-run-view)
- [Saved runs, storage, and logs](#saved-runs-storage-and-logs)
- [Enrollment details](#enrollment-details)
- [Project policy](#project-policy)
- [Planner checks and supervisor verification](#planner-checks-and-supervisor-verification)
- [Fan-out and sequential work](#fan-out-and-sequential-work)
- [Sessions and runtime integration](#sessions-and-runtime-integration)
- [Recovery and progress](#recovery-and-progress)
- [Custom workflows](#custom-workflows)
- [Host integrations](#host-integrations)
- [Recovery design priorities](#recovery-design-priorities)

## Set up once

From a Git project directory, add a CLI agent with a model supported by that CLI. One agent can cover the three roles needed by the default `lean` policy:

```bash
jevkit sdlc agents add codex --model YOUR_MODEL --rubric "General coding work" --role all
jevkit sdlc doctor --policy lean
jevkit sdlc agents c                   # per-agent tool configuration and runtime support
```

`jevkit sdlc agents` shows your roster. `jevkit sdlc agents discover` is optional inventory; it does not grant permission to use an agent. You can enroll separate planners, implementers, and assessors later.

Use `jevkit sdlc agents c` or `jevkit sdlc agents caps` for the roster path, supported
tool settings, and copyable per-agent YAML examples. Focus on one runtime with
`jevkit sdlc agents c -r claude`; add `--details` for CLI versions, hooks, and
execution diagnostics. The full `agents capabilities` command remains available.

Inspect or configure project integration defaults with `jevkit sdlc i` or
`jevkit sdlc int`. For example:

```sh
jevkit sdlc i --hooks on --mcp on --compaction on
```

## Run a task

```bash
jevkit sdlc run feature --task "Add rate limiting"
```

Built-in task kinds are `feature`, `bugfix`, `review`, and `release`. The planner may write `plan.md`, `checks.json`, and `subtasks.json`. The run pauses for plan approval before implementation. If the command has no interactive terminal, it prints the plan path; review the artifacts, then run `jevkit sdlc resume RUN_ID --approve-plan`. Use `--auto` only when you want to skip plan approval.

Planner-proposed argv checks still need an explicit authorization step (`jevkit sdlc resume RUN_ID --authorize-checks`). `--auto` does not authorize commands. After implementation, Jevkit runs those authorized checks as a supervisor-owned verification gate before assessors run. Passing, failing, timed-out, and stale (invalidated) receipts live under the run's artifacts; a failure returns a bounded summary to the implementer for repair, then reassessment.

If you already have a complete plan, pass `--plan-file path/to/plan.md`. Use `--task-file` for a task statement that still needs planning.

`run` checks setup, saves the task, prints the run ID, and performs the work:
it chooses eligible enrolled agents, launches their CLI runtimes, and saves
the plan, change report, or assessment. For custom stage workflows, it also asks
the authored Jev questions. CLI execution requires a Git repository to
capture the actual workspace diff.

`sdlc run` checks enrollment, project limits, driver reach, and assessor quorum before starting a task. A requested profile is never downgraded. Use `jevkit sdlc doctor --policy NAME` to diagnose setup problems.

## Live run view

In a terminal, `run` and `resume` display the live run view. It follows the
newest agent, shows elapsed working time and saved runtime activity, and lets
you read a navigation guide that opens with the view. Press `?` to hide or
show the guide at any time. The footer keeps the main controls visible.
The view expands the active agent invocation and returns to the run overview
when that invocation completes. Use the up/down arrows to browse activity;
Page Up/Down jumps five entries. `j`/`k` (in
either case) scroll the selected message text.
Press `n` to switch to the next agent and `a` to show every agent. Press `l`
to show or hide logs, `t` to switch to the latest tool result, and `d` to
expand decision details. The mouse selects text for copying. Press `p` to
pause screen updates while selecting text, and press `p` again to resume.
The frozen view labels itself and shows the resume key. Press `Ctrl-C` to stop
an active run and return to the shell; the active agent process is cancelled.

The live TUI uses the terminal's alternate screen so refreshes do not fill
shell scrollback. Codex file
change events show project-relative paths and change kinds. The pane opens on
the latest activity and keeps the run
status, pause cause, and recovery keys visible in narrow terminals.
Claude planner tool requests and results appear as they stream into the saved
invocation log, while the planner is still running.
Tool output preserves code indentation and is bounded in the live pane; use
`sdlc logs RUN_ID` for the full saved stream. If a run pauses, the view shows
the cause and a `NEXT` line saying what a retry does for that pause (for
example, send the implementer back with the verification failures). Press `r`
to retry, or `g` to type guidance for the next agent and then retry. `f`, `s`,
and `c` retry in a new, the same saved, or a compacted agent session. Press `q`
to leave it paused. Runs redirected to a pipe keep plain output; `--silent` keeps its
short status output. `sdlc watch RUN_ID` offers the same saved view without
driving or resuming the run.

When `run` or `resume` stops, Jevkit prints a summary after the live view
closes. It shows the saved state and pause cause, recent completed actions,
agent runtime and linked Jev usage totals, a next action, and the logs command.
For an `answer` run, the summary also prints the saved agent answer in shell
scrollback, with redaction and terminal control filtering. Long answers are
shortened for display; the saved response artifact keeps the full text.
After an interactive review answers, Jevkit asks whether it suggested changes
and whether to start a bugfix SDLC. If accepted, the new run receives the
saved review as `review.md`, verifies the findings during planning, and still
requires plan approval before implementation. Declining leaves the review
complete without starting another run.
Unknown token and tool-call counts stay explicit. The summary shows runtime
invocations and tool calls separately; Jev requests have no tool-call count.
The same summary appears with plain output
when stdout is redirected. A paused run includes a recovery command when the
saved state allows it.

## Saved runs, storage, and logs

The state root is `$JEVKIT_STATE_DIR`, else `$XDG_STATE_HOME/jevkit`, else
`~/.local/state/jevkit` (Windows: `%LOCALAPPDATA%\jevkit`). SDLC runs live at
`<state>/sdlc/runs/<run-id>/` (`run.json`, `artifacts/`, `logs/`, nodes, events).
The shared usage log is `<state>/usage.jsonl`. Project workflow and policy YAML
stay under `.jevkit/sdlc/` in the workdir. Config (`JEVKIT_CONFIG_DIR` /
`JEVKIT_CONFIG_HOME`) is a separate tree from state.

Informational quotas pause further invocations; they never auto-delete:

| Bound | Default | Env override |
| --- | --- | --- |
| Total saved SDLC state | 5 GiB | `JEVKIT_SDLC_STORAGE_QUOTA_BYTES` |
| One run tree (root + children) | 256 MiB | `JEVKIT_SDLC_RUN_TREE_QUOTA_BYTES` |
| Per-stream log tail | 1 MiB | `JEVKIT_SDLC_LOG_TAIL_BYTES` |

Read-only inventory: `sdlc runs` lists each task, status, and size against the
total quota. Start with `sdlc show RUN_ID` to see a short timeline of completed
actions across the run and its children, commands for the related invocation
logs, full artifact paths, log availability (including pruned/truncated), and
the next action. The timeline shows the latest 12 events; use
`sdlc logs RUN_ID --stream decisions` for the full decision history. Preview
then apply: `sdlc delete RUN_ID [--apply]` removes a finished tree and
attributable usage rows; `sdlc prune --older-than … [--status done|paused]
[--apply]` deletes inactive trees; `sdlc prune --logs-only … --apply` strips
diagnostic streams (and verification log bodies) while keeping plans, receipts,
status, and usage, marking invocations with a `.pruned` sibling. External
runtime session stores are out of reach.

Agent stdout and stderr are saved as private, bounded 1 MiB tails per stream
(default `MaxLogTail`; override with `JEVKIT_SDLC_LOG_TAIL_BYTES`). The combined
`lines.jsonl` stream uses the same bound. When a stream rolls over, Jevkit
prepends `[earlier output truncated: N bytes omitted]` to the retained raw tail
and records the omitted byte count in a sibling `.lines.jsonl.truncated` marker;
`sdlc show` and `sdlc logs` surface that count. Truncation drops earlier bytes
from disk—it is not merely unread.

`sdlc logs RUN_ID` includes children; filter by `--agent`, `--runtime`,
`--invocation`, or `--stream stdout|stderr|decisions`. Decision events replay
from the run ledger, including after a restart. Display is redacted by default.
`--raw` shows the locally saved original. `--follow` and `watch` only read
the ledger and never control the worker process.

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

`jevkit usage` separates Jev calls from agent runtime invocations and their
tool calls. Use
`--source all|jev|runtime` and `--format json` for a version 2 report. Runtime
totals list input, output, cache-read, and cache-creation tokens separately when
providers emit them; cache is never folded into input. Jev cost is estimated
from configured rates; runtime cost appears only when a runtime reports it.
Unknown token and tool-call counts stay unknown. Older runtime ledgers remain readable; Jev
calls made before recording was enabled cannot be reconstructed. See the short
[usage guide](USAGE.md).

## Enrollment details

The personal `sdlc/roster.yaml` authorizes agents. `sdlc agents discover` only lists suggestions and installed CLI apps; it never enrolls them. Native subagents and `host-self` also require enrollment and a host driver. The default `lean` policy needs a planner, implementer, and assessor; one enrolled CLI binding may fill all three roles.

The first personal roster includes disabled examples with tool settings filled
in: `shell`, `web`, and `delegate` for Claude/Codex, and `tools: auto` for
OpenCode. The mapping values start at `true`, which preserves runtime settings
and approval requirements; change a value to `false` to disable that family.
Cursor, Antigravity, and native agent entries use `tools: auto`.

### Writing project agent suggestions

You only need this optional file to share suggestions with other people on
this project. To set up your own working agents, edit the personal roster
shown by `jevkit sdlc agents`, or use `jevkit sdlc agents add`.

The generated starter suggests nothing until you uncomment an example. Each
`- id:` starts a suggestion with a unique name, CLI runtime, model, and rubric
that describes when to choose it. Use spaces for YAML indentation and replace
`YOUR_MODEL` with a model supported by the CLI:

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

### Tool allowances for SDLC agents

Tool configuration follows the invocation's agent selection. An entry with
`agent: security-reviewer`, for example, launches Claude with
`--agent security-reviewer`; Claude owns that definition's tool allowances.
Jevkit does not generate tool restrictions, role-derived permission modes, or
permission-bypass flags for named Claude, OpenCode, or Antigravity agents.
Host-native subagents likewise keep their host configuration.

For a runtime/model entry without `agent`, you can narrow built-in tools in
the personal roster with an optional `tools` block:

```yaml
version: 1
agents:
  - id: focused-builder
    via: runtime
    runtime: codex
    model: YOUR_MODEL
    roles: [implementer]
    rubric: Implement the approved plan using local project context.
    tools:
      shell: true
      web: false
      delegate: false
```

Use `tools: auto` to explicitly inherit the runtime's existing tool settings
and approval rules. Omitting `tools` has the same behavior. `auto` works for
every runtime and for native agents, where the selected native definition owns
the settings. It does not infer permissions from the SDLC role or bypass
runtime denials. `tools: all` is not supported.

These controls currently support **Codex and Claude**. `false` disables the
specified built-in tool family; `true` or an omitted field keeps the runtime's
existing behavior and approval rules. `true` does not bypass approvals or
override a runtime's own denial. Omit the entire block to keep existing
behavior. Empty blocks, unknown fields, restrictions on unsupported runtimes,
and restriction mappings combined with a native agent selection are rejected
when loading the roster.
Optional project suggestions in `agents.yaml` can carry the same block; it
is copied when the suggestion is enrolled.

| Field | Codex | Claude |
| --- | --- | --- |
| `shell` | Default shell tool (`features.shell_tool`) | `Bash`, `PowerShell` |
| `web` | Built-in web search (`web_search`) | `WebSearch`, `WebFetch` |
| `delegate` | Built-in collaboration tools (`features.multi_agent`) | `Agent`, legacy `Task` |

These settings control those built-in tools. They do not filter MCP tools,
disable shell network access, or prevent shell commands from launching
another program. File write boundaries remain governed by the existing role
and agent settings. A CLI invocation selecting a named agent cannot satisfy
a requirement for Jevkit-enforced read-only execution: use a runtime/model entry for that
requirement, or let the native definition own permissions under a role that
does not require Jevkit enforcement.

Settings are passed only to the individual CLI invocation. `sdlc agents`
shows configured restrictions and native ownership; `sdlc agents capabilities`
shows how to configure each entry, with a compact runtime support table.
Use `jevkit sdlc agents c` as a shortcut. Sessions are reused only for the
same binding, role, and tool settings. Changes after an assignment was saved
pause that assignment; retry to select using the current settings.

Runtime controls: [Codex configuration](https://learn.chatgpt.com/docs/config-file/config-reference),
[Claude CLI reference](https://code.claude.com/docs/en/cli-reference).

### Additional runtime arguments

CLI entries (`via: runtime`) can set `runtimeArgs` to a string of additional
options. This works for Codex, Claude, Cursor, OpenCode, and Antigravity,
including entries selecting a named native agent:

```yaml
version: 1
agents:
  - id: autonomous-builder
    via: runtime
    runtime: claude
    model: YOUR_MODEL
    roles: [implementer]
    rubric: Implement approved changes.
    tools: auto
    runtimeArgs: '--dangerously-skip-permissions --effort high'
```

Jevkit splits the string on whitespace, respecting single quotes, double
quotes, and backslash escapes. Each resulting word is passed directly as a CLI
argument after generated defaults and before the task prompt. There is no shell
execution, variable expansion, command substitution, or glob expansion. For
example, `--append-system-prompt "Use local project context"` passes the quoted
text as one argument. Unclosed quotes and unfinished escapes are rejected.

Explicit permission options replace the adapter's default permission mode;
for example, Claude's `--dangerously-skip-permissions` is passed without an
additional role-derived `--permission-mode plan`. These are user-selected
options, so native agents receive them too. Empty or omitted `runtimeArgs`
preserves existing behavior. Runtime-specific options are validated by the CLI.

Jevkit reserves options for model and agent selection, prompt transport, output
format, working directory, and session routing. Use `model` and `agent` in the
roster for those bindings. Permission overrides cannot be combined with an
explicit `readOnly` requirement, and competing tool flags cannot override a
`tools` restriction mapping. Jevkit's Codex tool configuration overrides are
applied after user arguments. These checks cover known CLI controls; custom
configuration files and extensions remain governed by the runtime.

Project suggestions can carry `runtimeArgs`; enrollment copies them into the
personal roster. You can also set them when adding an entry:

```sh
jevkit sdlc agents add autonomous-builder --runtime claude --model MODEL --role implementer --rubric 'Implement approved changes' --runtime-args='--dangerously-skip-permissions --effort high'
```

`sdlc agents` displays the configured string. Changing it prevents reuse of an
existing session and pauses assignments reserved with the previous value.
Additional arguments do not create independent quorum bindings. They apply to
task invocations and Claude's in-session compaction, rather than the separate
Codex app-server maintenance command.

### Role and run policy

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

Each role can set `via`, `runtimes`, `write`, `readOnly`, `isolated`, and `writeScopes`. Set `write: false` to require enforced read-only execution. A scoped or restricted assignment is eligible only when its host or runtime adapter reports that it can enforce the restriction. For runtime/model entries without a native agent selection, Codex enforces an OS sandbox mode; Claude and Cursor use prompting modes; OpenCode has no read-only flag and fails closed for read-only roles; Antigravity writable invocations pass `--dangerously-skip-permissions` (surfaced by `jevkit sdlc agents capabilities`). Named native agents own their permission configuration and receive no such overrides. None claims isolation or scoped-write enforcement beyond worktree isolation for fan-out. The project may set `quorums` by profile. `lean` defaults to one assessor, `collaborative` to two, and `assured` to three. Agents with different IDs but the same binding count once toward quorum, even with different tool settings.

`adaptiveBuiltinDelegation` is `off` by default. `--delegate-builtins` enables
built-in delegation for a single `run` or `start`, including when the project
policy sets it to `off`. Set `adaptiveBuiltinDelegation: on` to enable it by
default. `opt-in` remains valid for existing policy files and, like `off`,
leaves delegation disabled unless the flag is passed. `--delegate-builtins=false`
disables it for a single run. The
`--policy` flag independently selects the review profile, so `collaborative`
and built-in delegation can be used together.
The choice is stored in the run ledger for resume. Authored `spawn` stages
retain their explicit targets.

Runs have hard loop and time limits. By default, a run allows at most 3 implementation revisions and 20 total agent assignments across its tree. Each CLI invocation has a 30-minute deadline, and the root run expires 6 hours after creation. A run can create at most eight children across three child levels. Custom stage workflows also have `maxSteps` (20 by default) to bound question and work transitions. Set `maxRevisions`, `maxAssignments`, `maxInvocationSeconds`, and `maxRunSeconds` in project policy to tighten or widen those limits. Hitting a limit pauses the run. `maxEstimatedCostUsd` is optional and applies when workers report an estimate.

## Planner checks and supervisor verification

A `planned` outcome may include `checks.json` (argv and manual checks) and
`subtasks.json`. Plan approval (`resume --approve-plan`) locks plan, checks, and
subtasks digests. Argv checks still require `resume --authorize-checks` (or an
exact `--authorize-checks-digest`); `--auto` is not permission. After
implementation, the supervisor runs authorized checks against the candidate
worktree, writes `verification/receipts.json` (and a bounded failure summary on
fail), and only then advances to assessment. Statuses include passed, failed,
timed-out, skipped (no argv checks), and invalidated (stale vs current
worktree). Hosts may report agent outcomes but must not forge supervisor
verification receipts.

A failed verification returns the run to the implementer, whose prompt includes
the bounded failure summary and the path to the full check output. This repeats
until the checks pass or the revision budget is spent. If every failed check
failed because of the supervisor environment rather than the candidate (a
command missing from PATH, exit 127, or a Go toolchain older than the module
needs), the run pauses as `verification-environment-failed` without spending a
revision. The cause names the binary the supervisor resolved. Fix the
environment, then `resume --retry-failed` re-runs verification on the same
candidate.

## Fan-out and sequential work

Approved fan-out graphs (`subtasks.json` mode `fan-out`) schedule independent
implementers up to `maxConcurrent` and remaining assignment budget. Parallel
writable work needs isolation (worktrees); when isolation is unavailable,
Jevkit admits at most one writable subtask at a time (sequential). Dependent
edges, shared writable paths without isolation, or a single ready slot also
force sequential progress. Integration writes `integration/decision.json`;
conflicts or failed joins can pause for repair before reassessment. Inspect
`subtasks.json`, per-subtask patches under `artifacts/`, integration decisions,
and `sdlc logs` / `sdlc show` for each child. Wall-clock savings from parallel
admission do not imply lower total tokens or cost—each subtask still records
its own measured usage.

## Sessions and runtime integration

`--session-strategy auto|fresh|resume|compact` is available on `run`, `start`,
and `resume`; a resume override is saved in the run. Sessions are keyed by run,
binding, and role. `auto` asks Jev when a prior session exists and records the
choice and policy fallback. Codex manual compaction uses its app server
protocol. Claude manual compaction sends `/compact` ahead of the work prompt
through the installed Claude CLI's stream JSON mode, as Ralph does. A
failed native compaction pauses the run with the error. Other runtimes resolve
`compact` to explicit resume and record that fallback.

Each new interactive run asks whether to install project-scoped Jevkit hooks,
enable tool-output compaction, and install the Jevkit MCP server for its SDLC
CLI agents. MCP lets agents call `jev_ask` when a Jev judgment would help.
It can be enabled without hooks. The MCP server resolves the Jev API key at
runtime; its project config does not contain the key.
The choice is saved with that run, so resume and child runs use the same
settings. Select “keep this as my default” to save the choice privately for
future runs in this project; otherwise the next run asks again. Hooks and
MCP config are installed as each enrolled CLI agent starts, including
agents running in a worktree. Tool-output compaction is separate from the
`--session-strategy compact` option above. It is passed only to SDLC agent
processes and requires hooks and a configured Jevkit API key; when Jevkit is
unavailable, the hooks keep the original tool output. Noninteractive runs do
not prompt and use these features off unless a default has been saved.
Configure them with `jevkit sdlc i --hooks on --compaction on --mcp on`,
or inspect the current choice with `jevkit sdlc i`. For MCP alone,
use `jevkit sdlc i --mcp on`.
Use `jevkit sdlc i --ask-every-run` to clear a saved default, or
`--hooks off` or `--mcp off` to change an individual auto-install default.
If hooks are declined for a run, previously installed Jevkit hooks pass
through during that run. Project hook files remain installed; remove them with
`jevkit uninstall AGENT --components hooks`.
An existing project MCP registration remains available when MCP auto-install
is off. Remove it with `jevkit uninstall AGENT --components mcp` if needed.

Antigravity runs through the installed `agy` CLI. Jevkit passes the enrolled
model string unchanged, captures its conversation ID and reported usage from
stream JSON, and resumes with `--conversation ID`.

For CLI runtimes, Ctrl-C, SIGTERM, and invocation timeouts stop the runtime and subprocesses in its process group (Unix) or job object (Windows). Jevkit also cleans up those subprocesses when a runtime exits. A forced SIGKILL of Jevkit itself bypasses cleanup, so use the operating system's process-group or job termination when an immediate forced stop is required.

For native agents invoked through a host, Jevkit stops further routing and rejects late reports after the run deadline. The host must also enforce its own timeout on an agent invocation; Jevkit cannot kill a process owned by the host.

## Recovery and progress

To inspect progress between steps, start with `sdlc run feature --task "..." --step`.
The output includes a run ID. Use `sdlc resume RUN_ID --step` for one more
action, or `sdlc resume RUN_ID` to continue until completion or pause.
After fixing the error that paused a run on an agent failure, run
`sdlc resume RUN_ID --retry-failed`. It clears failed-agent exclusions and resets the consecutive no-progress count,
keeps saved artifacts and successful assessments, and resumes the paused stage.
Run limits still apply. The flag is rejected unless the run paused after an
agent failure, a verification environment failure, or a review that needs
reassessment. `sdlc resume RUN_ID --guidance "..."` saves instructions for the
next agent prompt and, on a retryable pause, also retries. The guidance is
cleared after an agent completes with it.

If files change during assessment, the run saves the review and a bounded list
of observed paths without claiming which process edited them. A
`changes-required` review continues to implementation and passes those paths
and findings to the implementer. An `approved` review pauses so the changed
workspace can be assessed again with `sdlc resume RUN_ID --retry-failed`.
Recovery checks the saved invocation, reviewer binding, and `patch.diff`
digest before reusing an interrupted review. If the digest changed, the run
pauses and requires a new assessment.

An SDLC worker that triggers a prompt-injection review pauses its run with outcome `injection-review-required`. `sdlc status` and `sdlc watch` show the review ID in the pending reason. Run `jevkit security review ID` from a terminal; `jevkit sdlc resume RUN_ID` refuses to continue until the review is resolved.

### Repair progress

- A decisive assessor or specialist rejection persists its invocation,
  candidate revision, role, and bounded findings in `run.json`. The full
  supplied review remains in its invocation response artifact. Both Jev's
  implementer selection and the implementer's prompt receive this evidence.
  Late parallel reviews cannot replace the accepted rejection. New candidates
  invalidate the feedback along with prior assessments and check receipts.
- An implementer returning `answer`, `no-change`, or the same candidate
  revision cannot finish an existing candidate that still needs verification
  or review. The first such result keeps implementation active and requests a
  different approach grounded in the saved findings. A second attempt without
  a new candidate pauses as `implementer-failed`, with the cause saved.
  These attempts consume assignments, but do not consume new revisions.
  An initial answer/no-change with no existing candidate remains valid.
- `resume --retry-failed` resets the consecutive no-progress count while
  retaining the repair findings. A new candidate also resets that count and
  goes through supervisor verification and independent assessment.
- Review admission accounts separately for reserved work and remaining
  assignments. Exhausted time, cost, or assignment budgets produce a recorded
  failure instead of an empty drive loop. Already charged review reservations
  can still execute when no additional assignment slots remain.

These rules do not authorize extra commands, expand agent enrollment, waive
review independence, or raise budgets. Jev chooses within the existing policy;
supervisor receipts remain authoritative for check outcomes.

## Custom workflows

A custom workflow is a project YAML file that asks **your questions** and
routes the answers to standard SDLC work. Use one when the built-in `feature`,
`bugfix`, `review`, and `release` flows do not capture a project decision. You
do not need a custom workflow to start using SDLC.

The YAML controls the question wording, named choices, fallback, work
objectives, and routes. A choice can also start another SDLC workflow. The
file does **not** enroll an agent or grant permissions.
At each work stage, Jevkit selects from enrolled agents allowed by project
policy and reachable by the current driver.

### Create and run one

```sh
jevkit sdlc create custom-review
jevkit sdlc explain custom-review
jevkit sdlc validate custom-review
jevkit sdlc doctor --policy lean
jevkit sdlc run custom-review --task "Add a rate limit"
```

`create` writes `.jevkit/sdlc/custom-review.yaml`. `run` checks the roster and
policy, saves the run, then asks the questions and executes eligible enrolled
agents until the workflow finishes or pauses. A workflow with implementation
shows its plan for approval or change requests in an interactive terminal. A
redirected run pauses instead; use `sdlc resume RUN_ID --approve-plan` after
reviewing `plan.md`, or pass `--auto` to run autonomously. You can edit the YAML directly
before running it; each run keeps a snapshot of the workflow it started with.
Use `run custom-review --task "..." --step` to execute only the first stage,
then `resume RUN_ID --step` to advance the active run one stage at a time.

### Stage YAML

```yaml
version: 1
name: custom-review
description: Clarify scope before planning and implementation.
entry: scope
maxSteps: 20
stages:
  - id: scope
    question:
      prompt: Is there enough information to begin this task?
      options:
        ready: The goal and constraints are clear.
        unclear: A requirement needs clarification.
      routes: {ready: plan, unclear: needs-context}
      fallback: needs-context
      minConfidence: 0.85

  - id: plan
    work:
      role: planner
      objective: Plan the change and its acceptance checks.
      routes: {planned: implement, answer: done, no-change: done}

  - id: implement
    work:
      role: implementer
      objective: Implement the plan in the repository.
      focus: Apply the API rate limit without changing existing error responses.
      routes: {changed: assess, answer: done, no-change: done}

  - id: assess
    work:
      role: assessor
      objective: Assess the exact diff against the plan.
      routes: {approved: done, changes-required: implement}

  - id: needs-context
    finish: paused
  - id: done
    finish: succeeded
```

Jev receives the task, the question prompt, and the choice descriptions. If
available, the saved plan and diff are included as bounded context. Jevkit
redacts that material before sending it. An unavailable Jev service, an
unknown choice, or an answer below `minConfidence` takes `fallback`. The
fallback is a named stage, not another agent.

A `work` stage names one standard role. `planner` can report `planned`,
`answer`, or `no-change`; `implementer` can report `changed`, `answer`, or
`no-change`; `assessor` can report `approved` or `changes-required`. The YAML
must route every normal outcome for that role. Invocation failures reroute to
another eligible agent with the same role when the workspace is unchanged.
Other worker failures and timeouts pause under the SDLC policy; they are not
authored as success routes. Assessment uses the policy quorum and the exact
diff revision.

### Route a choice into another SDLC

A question can route to a `spawn` stage. This example runs the built-in
`bugfix` SDLC when Jev chooses `fix`:

```yaml
version: 1
name: issue-triage
description: Decide whether an issue needs a bug fix.
entry: decide
stages:
  - id: decide
    question:
      prompt: Does this issue need a code fix?
      options: {fix: Fix the bug, answer: Answer without a code change}
      routes: {fix: run-bugfix, answer: done}
      fallback: paused

  - id: run-bugfix
    spawn:
      workflow: bugfix
      objective: Resolve the reported issue.
      routes: {succeeded: done, paused: paused, aborted: paused}

  - id: paused
    finish: paused
  - id: done
    finish: succeeded
```

`workflow` may name a built-in task kind or another project workflow. The
child receives the parent task and the optional `objective`. It has its own
run ID, while the parent waits at the `spawn` stage. A completed child takes
the `succeeded` route; a paused child takes `paused`; an aborted child takes
`aborted`. `resume PARENT_RUN_ID --step` advances one child action at a time.

Children use the same policy and enrolled agents. Their assignments, revision
count, estimated cost, and stage transitions count toward the parent's limits.
Nesting stops after three child levels. `sdlc validate` checks that a named
child exists, and `sdlc explain` shows the child and its return routes.

Every transition, including child workflow transitions, counts against `maxSteps`. Project policy also bounds total
agent assignments, implementation revisions, invocation time, run time, and
optional estimated cost. `sdlc validate` checks stage IDs, routes, required
outcomes, and reachability; `sdlc explain` prints the decisions as a readable
chart. The CLI driver needs enrolled CLI agents for work stages. Native and
host-self agents require a host integration; its low-level `sdlc next` and
`sdlc report` calls serve work stages, while `sdlc resume RUN_ID --step`
handles question stages.

`create` rejects built-in task names.

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

## Recovery design priorities

The first recovery improvement is implemented. The remaining items are proposed.

1. **Separate handoff avoidance from failed-agent exclusions (implemented).**
   A handoff temporarily defers its binding for the current phase. A
   non-handoff result releases those deferrals and resets the handoff count;
   authentication and invocation failures remain excluded separately. A run
   paused after repeated handoffs can use `resume --retry-failed` to restore
   its prior phase and clear the handoff deferrals. A single-binding fallback
   can still retry that binding when no alternate is eligible.
2. **Give Jev a structured recovery decision.** Supply the current objective,
   failed check IDs or review findings, approaches already attempted, eligible
   capabilities, and remaining budgets. Offer only feasible actions such as
   targeted repair, another enrolled binding, read-only research, or a concrete
   escalation. Persist the selected action and its expected new evidence.
   Reject a retry proposal that changes neither the approach nor its inputs.
3. **Recognize repeated failure across different diffs.** The current guard
   detects unchanged candidate reports, not semantically equivalent edits or
   oscillation between revisions. Record candidate tree identities and
   normalized failure signatures. Repeated signatures should prompt diagnosis
   or a different eligible binding before consuming the revision budget.
   A failing check can persist during legitimate incremental repair, so use
   the history as decision evidence rather than declaring failure from one
   repeated check name.
4. **Recover interrupted work with ownership evidence.** Reconcile active
   invocations against durable leases, process liveness, and workspace state.
   A restarted driver should reattach to live work, recover a completed
   artifact, or retry a confirmed dead invocation. Silence alone must not
   launch a second writer into the same workspace.
5. **Avoid repeatedly selecting failed assessors.** Record assessor failures
   for the current candidate and prefer a distinct eligible binding on the
   next attempt; release the avoidance when the candidate changes.

### Validation targets

Regression coverage should include missing review feedback, late reviewer
responses, restart during repair, no-change after failed checks, exhausted
budgets, and concurrency near the assignment limit. The current tests cover
these cases using deterministic executors and saved run state.

Future scenario tests should exercise transient runtime failure, a healthy
alternate binding, handoff cycles across roles, repeating check failures,
candidate oscillation, and interrupted processes. Track useful candidate
changes per assignment, repeated failure signatures, avoidable pauses, and
whether each pause names a recovery action. Validate the proposed Jev recovery
decision against these scenarios before making it the default.
