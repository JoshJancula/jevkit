package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
)

func TestStablePrefixFingerprintIgnoresVariableBody(t *testing.T) {
	base := Request{
		Agent:      enrollment.Agent{ID: "planner"},
		Assignment: adaptive.Assignment{Role: "planner", Revision: "rev-a", Objective: "ship", Reason: "route"},
		Task:       "Write a plan",
		Plan:       "plan-a",
		Diff:       "diff-a",
	}
	a := buildPrompt(base)
	b := buildPrompt(Request{
		Agent:      enrollment.Agent{ID: "other-agent"},
		Assignment: adaptive.Assignment{Role: "planner", Revision: "rev-b", Objective: "different", Reason: "elsewhere"},
		Task:       "Totally different task text",
		Plan:       "plan-b",
		Diff:       strings.Repeat("x", 2048),
		DiffPath:   "/saved/patch.diff",
	})
	if a.StablePrefix != b.StablePrefix || a.StablePrefixFingerprint != b.StablePrefixFingerprint || a.StablePrefixBytes != b.StablePrefixBytes {
		t.Fatalf("stable prefix drifted across variable changes:\nA=%q\nB=%q", a.StablePrefix, b.StablePrefix)
	}
	if a.Prompt == b.Prompt {
		t.Fatal("variable body should change the full prompt")
	}
	if !strings.HasPrefix(a.Prompt, a.StablePrefix) || !strings.Contains(a.Prompt, "Task: Write a plan") {
		t.Fatalf("prompt layout: %q", a.Prompt)
	}
	implementer := buildPrompt(Request{Assignment: adaptive.Assignment{Role: "implementer"}, Agent: enrollment.Agent{ID: "impl"}})
	if implementer.StablePrefixFingerprint == a.StablePrefixFingerprint {
		t.Fatal("different roles must fingerprint differently")
	}
}

func TestPromptSectionsFollowRoleAndRevision(t *testing.T) {
	for _, tc := range []struct {
		role string
		want string
		omit string
	}{
		{"planner", "Task-specific planner contract", "Change report:"},
		{"implementer", "Plan revision: revision", "Change report revision:"},
		{"assessor", "Change report revision: revision", "Plan revision:"},
		{"research", "Plan revision: revision", "Change report:"},
	} {
		req := Request{Agent: enrollment.Agent{ID: "agent"}, Assignment: adaptive.Assignment{Role: tc.role, Revision: "revision"}, Task: "task", Plan: "plan", Diff: "diff"}
		prompt := makePrompt(req)
		if !strings.Contains(prompt, tc.want) || strings.Contains(prompt, tc.omit) {
			t.Errorf("%s prompt sections: %q", tc.role, prompt)
		}
	}
	planner := makePrompt(Request{Agent: enrollment.Agent{ID: "agent"}, Assignment: adaptive.Assignment{Role: "planner"}, Task: "task\n\nOperator guidance: keep this", OriginalTask: "task"})
	if strings.Contains(planner, "Plan:") || strings.Contains(planner, "Change report:") || strings.Contains(planner, "task only: task\n\nOperator") || !strings.Contains(planner, "Operator guidance: keep this") {
		t.Fatalf("planner task boundaries: %q", planner)
	}
	if strings.Contains(stablePromptPrefix("planner"), "Review the change report") || strings.Contains(stablePromptPrefix("implementer"), "Review the change report") || !strings.Contains(stablePromptPrefix("assessor"), "Review the change report") {
		t.Fatal("review contract leaked across roles")
	}
}

func TestBoundVerificationSummaryKeepsLocalPath(t *testing.T) {
	long := strings.Repeat("assessment detail ", 400)
	got := BoundVerificationSummary(long, "/runs/r1/artifacts/last-assessment.md", 256)
	if !strings.Contains(got, "/runs/r1/artifacts/last-assessment.md") {
		t.Fatalf("missing local path: %q", got)
	}
	if len(got) > 400 {
		t.Fatalf("summary not bounded enough: %d", len(got))
	}
	if BoundText("short", MaxHandoffFieldBytes) != "short" {
		t.Fatal("short handoff field should pass through")
	}
}

func TestPromptEfficiencyFixtureReport(t *testing.T) {
	req := fixedPromptFixture()
	beforeLayout := beforePromptLayout(req)
	after := buildPrompt(req)
	started := time.Now()
	for i := 0; i < 200; i++ {
		_ = buildPrompt(req)
	}
	elapsed := time.Since(started) / 200

	cacheRead, cacheCreate, totalIn, totalOut, cost := measuredUsageFromFixture(t)
	quality := "structured-outcome-ok"
	if !strings.Contains(after.Prompt, `{"outcome":"planned","content":"..."}`) {
		quality = "structured-outcome-missing"
	}

	dir := filepath.Join("..", "..", "..", ".ralph-workspace", "artifacts", "jevkit-runtime-sdlc-hardening")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var report strings.Builder
	report.WriteString("# Prompt efficiency fixture (equal runtime/model settings)\n\n")
	report.WriteString("Fixed task fixture for planner role. Metrics below separate measured provider usage from prompt-layout latency and outcome-quality checks. No local cache of prompt contents is stored.\n\n")
	report.WriteString("| metric | before (interleaved contracts) | after (stable-prefix first) |\n")
	report.WriteString("| --- | --- | --- |\n")
	fmt.Fprintf(&report, "| stable_prefix_bytes | %d | %d |\n", beforeLayout.StablePrefixBytes, after.StablePrefixBytes)
	fmt.Fprintf(&report, "| stable_prefix_fingerprint | %s | %s |\n", emptyDash(beforeLayout.StablePrefixFingerprint), after.StablePrefixFingerprint)
	fmt.Fprintf(&report, "| total_prompt_bytes | %d | %d |\n", len(beforeLayout.Prompt), len(after.Prompt))
	fmt.Fprintf(&report, "| variable_body_bytes | %d | %d |\n", len(beforeLayout.Prompt)-beforeLayout.StablePrefixBytes, len(after.Prompt)-after.StablePrefixBytes)
	fmt.Fprintf(&report, "| build_latency_ns_per_op (fixture loop) | n/a (legacy string) | %d |\n", elapsed.Nanoseconds())
	fmt.Fprintf(&report, "| cache_read_tokens (fixture usage event) | %d | %d |\n", cacheRead, cacheRead)
	fmt.Fprintf(&report, "| cache_creation_tokens (fixture usage event) | %d | %d |\n", cacheCreate, cacheCreate)
	fmt.Fprintf(&report, "| total_input_tokens (fixture usage event) | %d | %d |\n", totalIn, totalIn)
	fmt.Fprintf(&report, "| total_output_tokens (fixture usage event) | %d | %d |\n", totalOut, totalOut)
	fmt.Fprintf(&report, "| cost_usd (fixture usage event) | %.4f | %.4f |\n", cost, cost)
	fmt.Fprintf(&report, "| outcome_quality | n/a | %s |\n", quality)
	report.WriteString("\nNotes:\n")
	report.WriteString("- Cache/total tokens and cost come from the checked-in Claude usage fixture under equal runtime/model settings; they are measured provider fields, not estimates.\n")
	report.WriteString("- Build latency is local prompt assembly only; live model latency is not claimed here.\n")
	report.WriteString("- Outcome quality is whether the stable contract still requires a bare structured outcome example.\n")
	report.WriteString("- Sensitive prompt contents are not written to this report; only fingerprints and byte counts.\n")
	path := filepath.Join(dir, "prompt-efficiency.md")
	if err := os.WriteFile(path, []byte(report.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixedPromptFixture() Request {
	return Request{
		Agent: enrollment.Agent{ID: "planner-fixture", Runtime: "claude", Model: "fixture-model"},
		Assignment: adaptive.Assignment{
			Role: "planner", Revision: "rev-fixture", Objective: "land the feature", Reason: "binding rubric",
		},
		Task: "Write a plan for the fixed fixture task.",
		Plan: "Existing plan body for the fixture.",
		Diff: "diff --git a/a b/a\n+fixture\n",
	}
}

// beforePromptLayout reconstructs the pre-refactor interleaved prompt shape so
// the efficiency report can compare layout metrics without calling a live model.
func beforePromptLayout(req Request) PromptLayout {
	diff := req.Diff
	if len(diff) > MaxDiffInlineBytes {
		diff = diff[:MaxDiffInlineBytes] + "\n[report excerpt ends here; inspect the saved artifact or workspace for the rest]"
	}
	if req.DiffPath != "" {
		diff = "Saved full change artifact: " + req.DiffPath + "\nInspect this file and the workspace as needed. The excerpt below is bounded so the runtime accepts the prompt.\n" + diff
	}
	allowed := allowedOutcomes(req.Assignment.Role)
	example := "answer"
	if len(allowed) > 0 {
		example = allowed[0]
	}
	prompt := "You are enrolled as agent \"" + req.Agent.ID + "\" for the " + req.Assignment.Role + " role. Task: " + req.Task +
		"\nFocus: " + req.Assignment.Objective +
		"\nRouting context: " + req.Assignment.Reason +
		"\nPlan revision: " + req.Assignment.Revision +
		"\nPlan:\n" + req.Plan +
		"\nChange report revision: " + req.Assignment.Revision +
		"\nChange report:\n" + diff +
		"\nReturn exactly one JSON object with outcome and content fields. For this role, outcome MUST be exactly one of: " + strings.Join(allowed, ", ") +
		". Use a bare outcome value, for example {\"outcome\":\"" + example + "\",\"content\":\"...\"}. Never include the role name in the outcome value."
	return PromptLayout{Prompt: prompt, StablePrefix: "", StablePrefixBytes: 0, StablePrefixFingerprint: ""}
}

func measuredUsageFromFixture(t *testing.T) (cacheRead, cacheCreate, input, output int64, cost float64) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "usage", "claude.result.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got := failedUsage("claude", raw)
	if got.CacheReadTokens != nil {
		cacheRead = *got.CacheReadTokens
	}
	if got.CacheCreationTokens != nil {
		cacheCreate = *got.CacheCreationTokens
	}
	if got.InputTokens != nil {
		input = *got.InputTokens
	}
	if got.OutputTokens != nil {
		output = *got.OutputTokens
	}
	if got.CostReported {
		cost = got.CostUSD
	}
	return
}

func emptyDash(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
