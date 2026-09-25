package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/security/config"
)

type fakeAsker struct {
	answer *jev.Response
	err    error
	calls  int
}

func (f *fakeAsker) Ask(context.Context, jev.Request) (*jev.Response, error) {
	f.calls++
	return f.answer, f.err
}

func TestEvaluateOrderingAndJevError(t *testing.T) {
	cfg, err := config.Builtin(config.LoadOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg.JevScoring = true
	asker := &fakeAsker{err: errors.New("offline")}
	cfg.Asker = asker
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	decider := &registry.Decider{Registry: reg}
	if decision, err := Evaluate(context.Background(), cfg, Request{Command: "rm -rf /"}, decider); err != nil || !decision.Deny || asker.calls != 0 {
		t.Fatalf("killswitch must run first: %+v %v calls=%d", decision, err, asker.calls)
	}
	if decision, err := Evaluate(context.Background(), cfg, Request{Command: "cat " + filepath.Join(t.TempDir(), "passwd"), Workspace: t.TempDir()}, decider); err != nil || !decision.Deny || asker.calls != 0 {
		t.Fatalf("sandbox must run before Jev: %+v %v calls=%d", decision, err, asker.calls)
	}
	if decision, err := Evaluate(context.Background(), cfg, Request{Command: "echo hi"}, decider); err != nil || decision.Deny || asker.calls != 1 {
		t.Fatalf("Jev error must allow: %+v %v calls=%d", decision, err, asker.calls)
	}
}

func TestShadowModeNeverDenies(t *testing.T) {
	state := t.TempDir()
	cfg, err := config.Builtin(config.LoadOptions{StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "shadow"
	decision, err := Evaluate(context.Background(), cfg, Request{Command: "rm -rf /"}, nil)
	if err != nil || decision.Deny || !decision.Shadow {
		t.Fatalf("shadow=%+v err=%v", decision, err)
	}
	raw, err := os.ReadFile(filepath.Join(state, "jevkit", "security-shadow.jsonl"))
	if err != nil || !strings.Contains(string(raw), "killswitch") {
		t.Fatalf("shadow decision was not recorded: %s %v", raw, err)
	}
}

func TestJevRiskDeniesOnlyHighConfidence(t *testing.T) {
	cfg, _ := config.Builtin(config.LoadOptions{})
	cfg.JevScoring = true
	asker := &fakeAsker{answer: &jev.Response{Answers: map[string]jev.Answer{"risk": jev.ScoreAnswer{Score: 4, Confidence: 0.95}}}}
	cfg.Asker = asker
	reg, _ := registry.Load()
	decider := &registry.Decider{Registry: reg}
	decision, err := Evaluate(context.Background(), cfg, Request{Command: "npm install suspicious-package"}, decider)
	if err != nil || !decision.Deny || decision.Score == nil {
		t.Fatalf("high risk=%+v err=%v", decision, err)
	}
	asker.answer.Answers["risk"] = jev.ScoreAnswer{Score: 4, Confidence: 0.5}
	decision, err = Evaluate(context.Background(), cfg, Request{Command: "npm install suspicious-package"}, decider)
	if err != nil || decision.Deny {
		t.Fatalf("low confidence=%+v err=%v", decision, err)
	}
}
