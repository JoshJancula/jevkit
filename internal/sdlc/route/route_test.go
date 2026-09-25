package route

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/registry"
)

// fakeAsker is an in-process Asker: no network, matching the repo's
// "no real process spawned / fixture transport" testing discipline.
type fakeAsker struct {
	resp    *jev.Response
	err     error
	lastReq jev.Request
}

func (f *fakeAsker) Ask(_ context.Context, req jev.Request) (*jev.Response, error) {
	f.lastReq = req
	return f.resp, f.err
}

func plainRedact(s string) (string, []redact.Hit, error) { return s, nil, nil }

func rejectingRedact(string) (string, []redact.Hit, error) {
	return "", nil, errors.New("state contains a secret")
}

func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return reg
}

func newRouter(t *testing.T, asker *fakeAsker, unavailable func(context.Context) string) *Router {
	t.Helper()
	if unavailable == nil {
		unavailable = func(context.Context) string { return "" }
	}
	r, err := New(Config{
		Decider:     &registry.Decider{Registry: newTestRegistry(t)},
		Client:      asker,
		Redact:      plainRedact,
		Unavailable: unavailable,
		Now:         func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func choiceResponse(choice string, confidence float64) *jev.Response {
	return &jev.Response{
		Model: "test-model",
		Answers: map[string]jev.Answer{
			"agent": jev.ChoiceAnswer{Choice: choice, Confidence: confidence},
		},
	}
}

func TestDecideActAboveThreshold(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("codex-implementer", 0.9)}
	r := newRouter(t, asker, nil)
	res, err := r.Decide(context.Background(), "sdlc.agent-selection", "task text",
		CriteriaFromRubrics(map[string]string{"codex-implementer": "WHEN: mechanical.", "cursor-planner": "WHEN: underspecified."}))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !res.Available || res.Decision.Decision != registry.Act || res.Decision.Chosen == nil || *res.Decision.Chosen != "codex-implementer" {
		t.Fatalf("res = %+v", res)
	}
}

func TestDecideGatherBetweenThresholds(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("codex-implementer", 0.7)}
	r := newRouter(t, asker, nil)
	res, err := r.Decide(context.Background(), "sdlc.agent-selection", "task",
		CriteriaFromRubrics(map[string]string{"codex-implementer": "r1", "cursor-planner": "r2"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Decision != registry.Gather {
		t.Fatalf("Decision = %v, want gather", res.Decision.Decision)
	}
}

func TestDecideFallbackBelowThreshold(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("codex-implementer", 0.3)}
	r := newRouter(t, asker, nil)
	res, err := r.Decide(context.Background(), "sdlc.agent-selection", "task",
		CriteriaFromRubrics(map[string]string{"codex-implementer": "r1", "cursor-planner": "r2"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Decision != registry.Fallback || !res.Available {
		t.Fatalf("res = %+v", res)
	}
}

func TestDecideCandidateNotInCriteriaIsFallback(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("some-other-agent", 0.95)}
	r := newRouter(t, asker, nil)
	res, err := r.Decide(context.Background(), "sdlc.agent-selection", "task",
		CriteriaFromRubrics(map[string]string{"codex-implementer": "r1"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Decision != registry.Fallback || res.Decision.Reason != registry.ReasonOptionNotOffered {
		t.Fatalf("res = %+v, want fallback/option-not-offered", res.Decision)
	}
}

func TestDecideUnavailableYieldsFallbackWithAvailableFalse(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("codex-implementer", 0.95)}
	r := newRouter(t, asker, func(context.Context) string { return "no-key" })
	res, err := r.Decide(context.Background(), "sdlc.agent-selection", "task",
		CriteriaFromRubrics(map[string]string{"codex-implementer": "r1"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Available || res.Decision.Decision != registry.Fallback || res.Decision.Reason != "no-key" {
		t.Fatalf("res = %+v", res)
	}
	if asker.lastReq.Questions != nil {
		t.Fatal("Jev must not be called at all when unavailable")
	}
}

func TestDecideTransportErrorYieldsFallbackNeverAnError(t *testing.T) {
	asker := &fakeAsker{err: errors.New("connection refused")}
	r := newRouter(t, asker, nil)
	res, err := r.Decide(context.Background(), "sdlc.agent-selection", "task",
		CriteriaFromRubrics(map[string]string{"codex-implementer": "r1"}))
	if err != nil {
		t.Fatalf("Decide must never surface a transport failure as an error, got %v", err)
	}
	if res.Available || res.Decision.Decision != registry.Fallback {
		t.Fatalf("res = %+v", res)
	}
}

func TestDecideShadowModeReturnsDefaultAndLogsCounterfactual(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("codex-implementer", 0.95)}
	dir := t.TempDir()
	r, err := New(Config{
		Decider: &registry.Decider{Registry: newTestRegistry(t), StateDir: dir, Getenv: func(k string) string {
			if k == "JEVKIT_SHADOW" {
				return "1"
			}
			return ""
		}},
		Client: asker, Redact: plainRedact, Unavailable: func(context.Context) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Decide(context.Background(), "sdlc.agent-selection", "task",
		CriteriaFromRubrics(map[string]string{"codex-implementer": "r1"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Decision != registry.Fallback || res.Decision.Reason != registry.ReasonShadow {
		t.Fatalf("res = %+v, want a shadow fallback", res.Decision)
	}
	data, err := os.ReadFile(registry.DecisionsPath(dir))
	if err != nil {
		t.Fatalf("expected a decisions.jsonl counterfactual entry: %v", err)
	}
	if !strings.Contains(string(data), `"decision":"act"`) {
		t.Fatalf("expected the logged counterfactual to keep the would-have decision, got %s", data)
	}
}

func TestDecideSeededSecretNeverReachesTheAsker(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("codex-implementer", 0.95)}
	r, err := New(Config{
		Decider: &registry.Decider{Registry: newTestRegistry(t)},
		Client:  asker, Redact: rejectingRedact, Unavailable: func(context.Context) string { return "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Decide(context.Background(), "sdlc.agent-selection", "sk-live-super-secret-key",
		CriteriaFromRubrics(map[string]string{"codex-implementer": "r1"}))
	if err == nil {
		t.Fatal("expected redaction rejection to surface as an error")
	}
	if asker.lastReq.Questions != nil {
		t.Fatal("a secret that failed redaction must never reach the Asker")
	}
}

func TestDecideRedactsCandidateRubrics(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("safe", 0.95)}
	r, err := New(Config{Decider: &registry.Decider{Registry: newTestRegistry(t)}, Client: asker,
		Redact: func(s string) (string, []redact.Hit, error) {
			return strings.ReplaceAll(s, "sk-secret", "[REDACTED]"), nil, nil
		},
		Unavailable: func(context.Context) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Decide(context.Background(), "sdlc.agent-selection", "task", map[string]string{"safe": "Use sk-secret for review."})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range asker.lastReq.Questions {
		if choice, ok := q.(jev.ChoiceQuestion); ok {
			rubric := string(choice.Criteria["safe"])
			if strings.Contains(rubric, "sk-secret") || !strings.Contains(rubric, "[REDACTED]") {
				t.Fatalf("rubric not redacted: %s", rubric)
			}
		}
	}
}

func TestNoulGateDecision(t *testing.T) {
	asker := &fakeAsker{resp: &jev.Response{Answers: map[string]jev.Answer{"delegate": jev.NoulAnswer{Noul: 0.92}}}}
	r := newRouter(t, asker, nil)
	res, err := r.Decide(context.Background(), "sdlc.needs-delegation", "task", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Decision != registry.Act || res.Decision.Confidence != 0.92 {
		t.Fatalf("res = %+v", res.Decision)
	}
}

func TestFixedCriteriaSetRejectsCallTimeCriteria(t *testing.T) {
	asker := &fakeAsker{resp: &jev.Response{Answers: map[string]jev.Answer{"delegate": jev.NoulAnswer{Noul: 0.9}}}}
	r := newRouter(t, asker, nil)
	if _, err := r.Decide(context.Background(), "sdlc.needs-delegation", "task", map[string]string{"x": "y"}); err == nil {
		t.Fatal("expected an error: sdlc.needs-delegation has fixed criteria")
	}
}

func TestEmptyCriteriaSetRequiresCallTimeCriteria(t *testing.T) {
	asker := &fakeAsker{resp: choiceResponse("a", 0.9)}
	r := newRouter(t, asker, nil)
	if _, err := r.Decide(context.Background(), "sdlc.agent-selection", "task", nil); err == nil {
		t.Fatal("expected an error: sdlc.agent-selection needs call-time criteria")
	}
}

func TestUnknownQuestionSetIsAnError(t *testing.T) {
	r := newRouter(t, &fakeAsker{}, nil)
	if _, err := r.Decide(context.Background(), "sdlc.nonexistent", "task", nil); err == nil {
		t.Fatal("expected an error for an unknown question set")
	}
}

// TestCriteriaAssemblyIdenticalShapeForAgentAndWorkflowSelection asserts the
// plan's own requirement: "one implementation of call-time criteria assembly
// serves both" agent and workflow selection. Both call sites go through
// CriteriaFromRubrics and buildQuestions identically; this test drives both
// question sets with the same input shape and checks the resulting wire
// criteria are structurally identical (same option ids, same bounded text).
func TestCriteriaAssemblyIdenticalShapeForAgentAndWorkflowSelection(t *testing.T) {
	rubrics := map[string]string{"a": "rubric for a", "b": "rubric for b"}
	agentAsker := &fakeAsker{resp: choiceResponse("a", 0.9)}
	workflowAsker := &fakeAsker{resp: &jev.Response{Answers: map[string]jev.Answer{"workflow": jev.ChoiceAnswer{Choice: "a", Confidence: 0.9}}}}

	ra := newRouter(t, agentAsker, nil)
	if _, err := ra.Decide(context.Background(), "sdlc.agent-selection", "task", CriteriaFromRubrics(rubrics)); err != nil {
		t.Fatal(err)
	}
	rw := newRouter(t, workflowAsker, nil)
	if _, err := rw.Decide(context.Background(), "sdlc.workflow-selection", "task", CriteriaFromRubrics(rubrics)); err != nil {
		t.Fatal(err)
	}

	aq, ok := agentAsker.lastReq.Questions["agent"].(jev.ChoiceQuestion)
	if !ok {
		t.Fatalf("agent question = %T", agentAsker.lastReq.Questions["agent"])
	}
	wq, ok := workflowAsker.lastReq.Questions["workflow"].(jev.ChoiceQuestion)
	if !ok {
		t.Fatalf("workflow question = %T", workflowAsker.lastReq.Questions["workflow"])
	}
	aj, _ := json.Marshal(aq.Criteria)
	wj, _ := json.Marshal(wq.Criteria)
	if string(aj) != string(wj) {
		t.Fatalf("criteria shapes differ:\nagent:    %s\nworkflow: %s", aj, wj)
	}
}
