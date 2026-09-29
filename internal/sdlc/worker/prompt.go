package worker

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Prompt size budgets keep runtime prompts cache-friendly and bounded.
// Exact source stays on disk (DiffPath, review artifacts, logs); only the
// inlined excerpts are capped.
const (
	MaxDiffInlineBytes           = 64 * 1024
	MaxVerificationSummaryBytes  = 4 * 1024
	MaxHandoffFieldBytes         = 512
	MaxPlanFeedbackInlineBytes   = 4 * 1024
	MaxResearchAdviceInlineBytes = 8 * 1024
)

// PromptLayout is the assembled runtime prompt with content-free telemetry for
// the stable prefix. Fingerprint and byte count never include variable task,
// plan, revision, or diff text, and never cache prompt contents locally.
type PromptLayout struct {
	Prompt                  string
	StablePrefix            string
	StablePrefixBytes       int
	StablePrefixFingerprint string
}

// buildPrompt places stable role/safety/output contracts first and variable
// task, plan, revision, and diff details afterward (Ralph's stable-prefix
// pattern). makePrompt returns only the combined string for CLI argv/stdin.
func makePrompt(req Request) string {
	return buildPrompt(req).Prompt
}

func buildPrompt(req Request) PromptLayout {
	stable := stablePromptPrefix(req.Assignment.Role)
	variable := variablePromptBody(req)
	prompt := stable
	if variable != "" {
		prompt = stable + "\n\n" + variable
	}
	return PromptLayout{
		Prompt:                  prompt,
		StablePrefix:            stable,
		StablePrefixBytes:       len(stable),
		StablePrefixFingerprint: stablePrefixFingerprint(stable),
	}
}

// stablePromptPrefix is role-scoped only: safety and output contracts that can
// stay byte-identical across invocations of the same role. Agent ID, task,
// plan, and diffs belong in the variable body.
func stablePromptPrefix(role string) string {
	allowed := allowedOutcomes(role)
	example := "answer"
	if len(allowed) > 0 {
		example = allowed[0]
	}
	outcomes := strings.Join(allowed, ", ")
	if outcomes == "" {
		outcomes = "(none configured for this role)"
	}
	base := strings.TrimSpace(fmt.Sprintf(`You are enrolled for the %s role in a Jevkit SDLC run.

Return exactly one JSON object with outcome and content fields. For this role, outcome MUST be exactly one of: %s. Use a bare outcome value, for example {"outcome":"%s","content":"..."}. Never include the role name in the outcome value. Complete the assigned role with available tools when possible. Return handoff with required focus and reason only when you cannot proceed; another agent may not be available. A handoff must leave the workspace unchanged. Keep handoff focus and reason concise; put detailed notes in workspace files or saved artifacts, not in the handoff fields. For planned, content is the complete plan. For changed, content is a concise description; Jevkit computes the change report from the workspace. For other outcomes, content is a concise explanation. Do not include Markdown fences. Reviewers must review the change report and changed files for this revision and must not edit files.`, role, outcomes, example))
	if role == "planner" {
		base += "\n\n" + plannerStableContract()
	}
	return base
}

func plannerStableContract() string {
	return strings.TrimSpace(`For outcome "planned", also include nextSteps (non-empty actionable steps), acceptanceCriteria (non-empty), and checks (zero or more). Each check needs a stable id and exactly one of: argv (string array, no shell) with optional workingDir and timeoutSeconds, or manual (explicit manual verification when a command is unsuitable). Do not invent default checks. Do not run any proposed command during planning. Jevkit persists content as plan.md plus validated checks.json and subtasks.json. Optionally include subtasks with independenceReason, integrationOwner for shared files or interfaces, latencyBenefit when parallelizing, and per-subtask id, objective, dependsOn, expectedOutput, ownedPaths, mergeOrder, and acceptanceCriteria. Prefer one implementer for small or tightly coupled work; propose fan-out only when at least two ready subtasks have a plausible latency benefit and isolatable ownedPaths. Invalid graphs fall back to one implementer. A planned outcome is valid only when it hands off enough information for implementation and assessment. For handoff, focus and reason must name what is needed next.`)
}

func variablePromptBody(req Request) string {
	diff := boundDiff(req.Diff, req.DiffPath)
	var b strings.Builder
	fmt.Fprintf(&b, "Agent: %s\n", req.Agent.ID)
	fmt.Fprintf(&b, "Task: %s\n", req.Task)
	fmt.Fprintf(&b, "Focus: %s\n", BoundText(req.Assignment.Objective, MaxHandoffFieldBytes))
	fmt.Fprintf(&b, "Routing context: %s\n", BoundText(req.Assignment.Reason, MaxHandoffFieldBytes))
	fmt.Fprintf(&b, "Plan revision: %s\n", req.Assignment.Revision)
	fmt.Fprintf(&b, "Plan:\n%s\n", req.Plan)
	fmt.Fprintf(&b, "Change report revision: %s\n", req.Assignment.Revision)
	fmt.Fprintf(&b, "Change report:\n%s", diff)
	if req.Assignment.Role == "planner" {
		b.WriteString("\n\n")
		b.WriteString(plannerTaskContract(req.Task))
	}
	return strings.TrimRight(b.String(), "\n")
}

func plannerTaskContract(task string) string {
	task = strings.TrimSpace(task)
	if task == "" {
		task = "(task statement not provided)"
	}
	return strings.TrimSpace(fmt.Sprintf(`Task-specific planner contract:
- Derive nextSteps and acceptanceCriteria for this task only: %s
- Propose zero or more checks that verify this task; use manual when a command is unsuitable.
- Subtask fan-out is optional and must stay bounded to independent work for this task.
- Do not discover shell commands from prose, invent default checks, or execute proposed commands while planning.`, BoundText(task, MaxHandoffFieldBytes)))
}

func boundDiff(diff, diffPath string) string {
	if len(diff) > MaxDiffInlineBytes {
		diff = diff[:maxRuneBoundary(diff, MaxDiffInlineBytes)] + "\n[report excerpt ends here; inspect the saved artifact or workspace for the rest]"
	}
	if diffPath != "" {
		diff = fmt.Sprintf("Saved full change artifact: %s\nInspect this file and the workspace as needed. The excerpt below is bounded so the runtime accepts the prompt.\n%s", diffPath, diff)
	}
	return diff
}

// BoundText head/tail-excerpts text to at most maxBytes. maxBytes <= 0 means
// unbounded. Exact retained source stays local; callers should point agents at
// the on-disk artifact when content is truncated.
func BoundText(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	marker := fmt.Sprintf("\n...[%d bytes omitted; inspect the saved local artifact or logs for the rest]...\n", len(text)-maxBytes)
	if len(marker) >= maxBytes {
		return text[:maxRuneBoundary(text, maxBytes)]
	}
	remaining := maxBytes - len(marker)
	headLen := maxRuneBoundary(text, remaining/2)
	tailStart := minRuneBoundary(text, len(text)-(remaining-headLen))
	if tailStart < headLen {
		tailStart = headLen
	}
	return text[:headLen] + marker + text[tailStart:]
}

// BoundVerificationSummary bounds assessment / verification text injected into
// the next prompt and returns a retrieval hint when truncated.
func BoundVerificationSummary(content, localPath string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = MaxVerificationSummaryBytes
	}
	bound := BoundText(content, maxBytes)
	if localPath == "" || len(content) <= maxBytes {
		return bound
	}
	return fmt.Sprintf("Full assessment saved locally: %s\nBounded excerpt for the prompt:\n%s", localPath, bound)
}

func stablePrefixFingerprint(stable string) string {
	if stable == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(stable))
	return fmt.Sprintf("%x", sum[:8])
}

func maxRuneBoundary(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

func minRuneBoundary(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(s) {
		return len(s)
	}
	for n < len(s) && !utf8.RuneStart(s[n]) {
		n++
	}
	return n
}
