---
name: jev-decisions
description: Get a fast second opinion from Jev (jevkit MCP tools) at a decision point - before a broad, destructive or outward-facing action, before telling the user a task is done, when a test or build fails for an unclear reason, when deciding whether a review finding is real, or when deciding what to read first in long logs. Use it when you are about to commit to a judgment call, not for every step.
---

# Jev decision support

The jevkit MCP server exposes Jev, a small classifier model that answers closed
questions with calibrated probabilities in about a second. Each call returns the
answer, an act/gather/fallback decision, and one line of guidance. Calls never
change anything: you still decide and act.

## When to call

| Moment | Call | Required state |
| --- | --- | --- |
| About to edit many files, delete, push, deploy, or otherwise act beyond the literal request | `jev_developer_assess` `developer.proceed-check` | `request`, `plannedAction` |
| About to say "done" | `developer.done-check` | `request`, `diffSummary` (add `testOutput`) |
| A test or build failed and the cause is not obvious | `developer.failure-triage` | `testOutput` (add `diffSummary`, `environment`) |
| A reviewer, linter or other agent raised a finding | `developer.finding-validity` | `finding` (add `diffSummary`) |
| Sizing a change or deciding how much to test | `developer.change-risk`, `developer.test-priority` | `diffSummary`, `affectedAreas` |
| Long logs or search output | `jev_rank_relevance` | lines tagged `L000:`... in `state`, ids in `options` |
| Any other closed choice | `jev_ask` | your own options, one-line rubric each |

Skip it for trivial or fully specified steps; the value is at judgment calls.

## Writing state

- `request`: quote the user, or paraphrase closely. Do not soften it.
- `plannedAction`: name the files, commands and systems you will touch.
- `constraints` / `environment`: shared working tree, uncommitted work,
  production systems, OS or CI runner, anything the user ruled out.
- Send summaries and the lines that matter, not whole files. State is redacted
  before it leaves the machine.

## Reading the result

- **act**: confident enough to use as input to your decision.
- **gather**: a lean, not a decision. `guidance` names the missing evidence;
  add it and ask again, or proceed on your own judgment and say so.
- **fallback**: ignore the answer and follow the fallback in `guidance`.
- **unavailable** (no key, breaker open): continue without Jev; do not retry.

When Jev disagrees with you, treat it as a prompt to re-check the evidence,
not as an override. Mention the assessment to the user only when it changed
what you did.
