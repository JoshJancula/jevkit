package adaptive

import (
	"strings"
	"testing"
)

func TestValidatePlannedHandoffRequiresStructuredFields(t *testing.T) {
	err := ValidatePlannedHandoff("plan", nil, []string{"done"}, nil)
	if err == nil || !strings.Contains(err.Error(), "nextSteps") {
		t.Fatalf("expected nextSteps error, got %v", err)
	}
	err = ValidatePlannedHandoff("plan", []string{"implement"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "acceptanceCriteria") {
		t.Fatalf("expected acceptanceCriteria error, got %v", err)
	}
	if err := ValidatePlannedHandoff("plan", []string{"implement"}, []string{"tests pass"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateChecksRequireArgvOrManual(t *testing.T) {
	err := ValidateChecks([]Check{{ID: "c1", Argv: []string{"go", "test"}, Manual: "look"}})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected exactly-one error, got %v", err)
	}
	if err := ValidateChecks([]Check{{ID: "c1", Manual: "click through the UI"}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateChecks([]Check{{ID: "c1", Argv: []string{"go", "test", "./..."}}}); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeSubtaskGraphFallsBackOnCyclesAndAmbiguity(t *testing.T) {
	cyclic := &SubtaskGraph{
		IndependenceReason: "independent packages",
		IntegrationOwner:   "integrator",
		Subtasks: []Subtask{
			{ID: "a", Objective: "A", ExpectedOutput: "a", OwnedPaths: []string{"a/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}, DependsOn: []string{"b"}},
			{ID: "b", Objective: "B", ExpectedOutput: "b", OwnedPaths: []string{"b/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}, DependsOn: []string{"a"}},
		},
	}
	got := NormalizeSubtaskGraph(cyclic)
	if got.Mode != "single" || got.FallbackReason != "cycles" {
		t.Fatalf("cycle fallback: %+v", got)
	}

	overlap := &SubtaskGraph{
		IndependenceReason: "independent",
		Subtasks: []Subtask{
			{ID: "a", Objective: "A", ExpectedOutput: "a", OwnedPaths: []string{"pkg/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}},
			{ID: "b", Objective: "B", ExpectedOutput: "b", OwnedPaths: []string{"pkg/foo"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}},
		},
	}
	got = NormalizeSubtaskGraph(overlap)
	if got.Mode != "single" || got.FallbackReason != "ambiguous ownership" {
		t.Fatalf("overlap fallback: %+v", got)
	}
}

func TestNormalizeSubtaskGraphKeepsValidFanOut(t *testing.T) {
	g := &SubtaskGraph{
		IndependenceReason: "separate packages with no shared interfaces",
		LatencyBenefit:     "two packages can build in parallel",
		Parallelize:        true,
		Subtasks: []Subtask{
			{ID: "api", Objective: "API", ExpectedOutput: "api patch", OwnedPaths: []string{"internal/api/"}, MergeOrder: 1, AcceptanceCriteria: []string{"api tests"}},
			{ID: "ui", Objective: "UI", ExpectedOutput: "ui patch", OwnedPaths: []string{"web/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ui tests"}},
		},
	}
	got := NormalizeSubtaskGraph(g)
	if got.Mode != "fan-out" || len(got.Subtasks) != 2 || got.FallbackReason != "" {
		t.Fatalf("valid fan-out: %+v", got)
	}
	bound := BoundGraphConcurrency(got, 3, 10)
	if bound.EffectiveConcurrency != 2 {
		t.Fatalf("effective concurrency: %+v", bound)
	}
	capped := BoundGraphConcurrency(got, 1, 10)
	if capped.EffectiveConcurrency != 1 || capped.Parallelize {
		t.Fatalf("policy cap: %+v", capped)
	}
}

func TestNormalizeSubtaskGraphRejectsParallelizeWithoutReadyPair(t *testing.T) {
	g := &SubtaskGraph{
		IndependenceReason: "sequential layers",
		Parallelize:        true,
		LatencyBenefit:     "faster",
		IntegrationOwner:   "integrator",
		Subtasks: []Subtask{
			{ID: "a", Objective: "A", ExpectedOutput: "a", OwnedPaths: []string{"a/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}},
			{ID: "b", Objective: "B", ExpectedOutput: "b", OwnedPaths: []string{"b/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}, DependsOn: []string{"a"}},
		},
	}
	got := NormalizeSubtaskGraph(g)
	if got.Mode != "single" || !strings.Contains(got.FallbackReason, "two ready") {
		t.Fatalf("parallelize fallback: %+v", got)
	}
}

func TestNormalizeSubtaskGraphRequiresIntegrationOwnerWhenShared(t *testing.T) {
	g := &SubtaskGraph{
		IndependenceReason: "mostly independent",
		SharedPaths:        []string{"internal/api/types.go"},
		Subtasks: []Subtask{
			{ID: "a", Objective: "A", ExpectedOutput: "a", OwnedPaths: []string{"a/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}},
			{ID: "b", Objective: "B", ExpectedOutput: "b", OwnedPaths: []string{"b/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}},
		},
	}
	got := NormalizeSubtaskGraph(g)
	if got.Mode != "single" || got.FallbackReason != "no usable integration path" {
		t.Fatalf("integration fallback: %+v", got)
	}
}

func TestMarshalArtifactsRoundTrip(t *testing.T) {
	raw, err := MarshalChecks([]Check{{ID: "t", Argv: []string{"go", "test", "./..."}}})
	if err != nil || !strings.Contains(string(raw), `"id": "t"`) {
		t.Fatalf("checks: %s %v", raw, err)
	}
	raw, err = MarshalSubtasks(SingleImplementerGraph("no subtasks proposed"))
	if err != nil || !strings.Contains(string(raw), `"mode": "single"`) {
		t.Fatalf("subtasks: %s %v", raw, err)
	}
}
