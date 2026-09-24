# Custom SDLC workflows

A custom workflow is a project YAML file that asks **your questions** and
routes the answers to standard SDLC work. Use one when the built-in `feature`,
`bugfix`, `review`, and `release` flows do not capture a project decision. You
do not need a custom workflow to start using SDLC.

The YAML controls the question wording, named choices, fallback, work
objectives, and routes. A choice can also start another SDLC workflow. The
file does **not** enroll an agent or grant permissions.
At each work stage, Jevkit selects from enrolled agents allowed by project
policy and reachable by the current driver.

## Create and run one

```sh
jevkit sdlc create custom-review
jevkit sdlc explain custom-review
jevkit sdlc validate custom-review
jevkit sdlc doctor --policy lean
jevkit sdlc run custom-review --task "Add a rate limit"
```

`create` writes `.jevkit/sdlc/custom-review.yaml`. `run` checks the roster and
policy, saves the run, then asks the questions and executes eligible enrolled
agents until the workflow finishes or pauses. You can edit the YAML directly
before running it; each run keeps a snapshot of the workflow it started with.
Use `run custom-review --task "..." --step` to execute only the first stage,
then `resume RUN_ID --step` to advance the active run one stage at a time.

## Stage YAML

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
must route every normal outcome for that role. Worker failures and timeouts
pause or reroute under the SDLC policy; they are not authored as success
routes. Assessment uses the policy quorum and the exact diff revision.

## Route a choice into another SDLC

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

See the [main SDLC guide](SDLC.md) for enrollment and project policy.
