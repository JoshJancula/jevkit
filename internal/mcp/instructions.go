package mcp

// Instructions is sent in the initialize result. MCP hosts show it to the
// agent ahead of the tool list, and it is often the only thing an agent reads
// before deciding whether these tools are worth loading, so it says when to
// call them, not how they work.
const Instructions = `jevkit gives you a fast, cheap second opinion from Jev, a small classifier model, at decision points. Every call returns a typed answer with calibrated probabilities, an act/gather/fallback decision, and one line of guidance. Calls take about a second and never change anything: you still decide and act.

Reach for it at these moments:
- About to do something broad, destructive or outward-facing, or unsure the plan matches what the user asked: jev_developer_assess developer.proceed-check (state.request, state.plannedAction).
- About to tell the user a task is done: developer.done-check (state.request, state.diffSummary, state.testOutput).
- A test or build failed and the cause is not obvious: developer.failure-triage (state.testOutput, plus state.diffSummary and state.environment when known).
- Weighing a reviewer's, linter's or another agent's finding before fixing it: developer.finding-validity (state.finding).
- Sizing a change, deciding how much to test, or whether it is ready to ship: developer.change-risk, developer.test-priority, developer.review-disposition, developer.release-readiness.
- Long logs or search output and you need to know what to read first: jev_rank_relevance.
- Any other closed-choice judgment: jev_ask with your own options and a one-line rubric each.

Reading the result: act means the answer is confident enough to rely on as input. gather means it is a lean; the guidance names the evidence to add before asking again, or you can proceed on your own judgment. fallback means ignore the answer. An unavailable result (no key, breaker open) means continue without Jev; do not retry.

State is redacted before it leaves the machine, but send summaries and the lines that matter, not whole files.`
