package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/registry"
)

// TestInitializeSendsInstructions checks the server tells agents when to use
// its tools: hosts that defer tool schemas otherwise show only bare names.
func TestInitializeSendsInstructions(t *testing.T) {
	h := start(t)
	var res struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(h.init.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Instructions != Instructions {
		t.Fatalf("instructions = %q", res.Instructions)
	}
	for _, id := range developerAssessmentIDs() {
		if !strings.Contains(res.Instructions, id) {
			t.Errorf("instructions never mention %s", id)
		}
	}
}

// TestDecisionIsCompact checks the result carries the decision once, without
// repeating the answers the decision log already records.
func TestDecisionIsCompact(t *testing.T) {
	resp := &jev.Response{Answers: map[string]jev.Answer{"cause": jev.ChoiceAnswer{Choice: "regression", Confidence: 0.9}}}
	h := start(t, withInner(staticAsker{resp}))
	tr, r := h.call("jev_developer_assess", map[string]any{"assessment": "developer.failure-triage", "state": developerAssessState("developer.failure-triage")})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	dec := mustGet[map[string]any](t, tr.Structured, "decision")
	for _, k := range []string{"answers", "timestamp", "registryVersion", "questionSetId"} {
		if _, ok := dec[k]; ok {
			t.Errorf("decision repeats %q: %v", k, dec)
		}
	}
	if got := mustGet[string](t, dec, "chosen"); got != "regression" {
		t.Errorf("chosen = %q", got)
	}
}

// TestGuidanceFollowsDecision checks every decision comes with one line the
// agent can act on, and a gather names the optional evidence that was left
// out plus the set's gather hint.
func TestGuidanceFollowsDecision(t *testing.T) {
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	set, _ := reg.Set("developer.failure-triage")
	for _, c := range []struct {
		confidence float64
		want       []string
	}{
		{0.9, []string{"rely on this answer"}},
		{0.7, []string{"lean", `"test-defect-flake"`, "state.diffSummary, state.environment", set.Policy.GatherHint}},
		{0.3, []string{"too low", set.Policy.Fallback}},
	} {
		resp := &jev.Response{Answers: map[string]jev.Answer{"cause": jev.ChoiceAnswer{Choice: "test-defect-flake", Confidence: c.confidence}}}
		h := start(t, withInner(staticAsker{resp}))
		tr, r := h.call("jev_developer_assess", map[string]any{"assessment": "developer.failure-triage", "state": map[string]any{"testOutput": "FAIL on windows only"}})
		if r.Error != nil || tr.IsError {
			t.Fatalf("error: %+v", r.Error)
		}
		guide := mustGet[string](t, tr.Structured, "guidance")
		for _, w := range c.want {
			if !strings.Contains(guide, w) {
				t.Errorf("confidence %v: guidance %q lacks %q", c.confidence, guide, w)
			}
		}
		if !strings.Contains(tr.Content[0].Text, guide) || !strings.Contains(tr.Content[0].Text, "cause=test-defect-flake") {
			t.Errorf("text content = %q", tr.Content[0].Text)
		}
	}
}

// TestEveryMCPSetHasGatherHint keeps gather actionable for every set an MCP
// tool can reach.
func TestEveryMCPSetHasGatherHint(t *testing.T) {
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	ids := developerAssessmentIDs()
	for _, c := range curated {
		ids = append(ids, c.set)
	}
	for _, id := range ids {
		set, ok := reg.Set(id)
		if !ok {
			t.Fatalf("missing set %s", id)
		}
		if set.Policy.GatherHint == "" {
			t.Errorf("%s has no gatherHint", id)
		}
	}
}

// TestRankRelevanceReturnsRanking checks the relevance tool orders every
// option with nonzero probability instead of returning only the top pick.
func TestRankRelevanceReturnsRanking(t *testing.T) {
	resp := &jev.Response{Answers: map[string]jev.Answer{
		"relevant_lines": jev.ChoiceAnswer{Choice: "L001", Confidence: 0.6, Probabilities: map[string]float64{"L000": 0, "L001": 0.6, "L002": 0.1, "L003": 0.3}},
		"has_failure":    jev.NoulAnswer{Noul: 0.9},
	}}
	h := start(t, withInner(staticAsker{resp}))
	tr, r := h.call("jev_rank_relevance", map[string]any{
		"state":   "L000 ok\nL001 boom\nL002 warn\nL003 cause",
		"options": []string{"L000", "L001", "L002", "L003"},
	})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	var order []string
	for _, e := range mustGet[[]any](t, tr.Structured, "ranked") {
		order = append(order, e.(map[string]any)["option"].(string))
	}
	if want := []string{"L001", "L003", "L002"}; !slices.Equal(order, want) {
		t.Errorf("ranked = %v, want %v", order, want)
	}
}
