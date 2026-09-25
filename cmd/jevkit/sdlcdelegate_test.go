package main

import (
	"context"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

type delegateChoices struct{ calls int }

func (d *delegateChoices) Ask(_ context.Context, req jev.Request) (*jev.Response, error) {
	d.calls++
	choice := "continue"
	if d.calls == 1 {
		choice = "bugfix"
	}
	return &jev.Response{Answers: map[string]jev.Answer{"target": jev.ChoiceAnswer{Choice: choice, Confidence: 0.99}}}, nil
}

func TestAutomaticBuiltInChildRefreshesParentDiff(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeFile(t, a.sdlcPolicyPath(), "version: 1\nadaptiveBuiltinDelegation: opt-in\n")
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	choices := &delegateChoices{}
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return choices }
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}, {Outcome: "approved"}}}
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "fix behavior", "--delegate-builtins=true", "--auto")
	if code != exitOK {
		t.Fatalf("delegated run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	parent, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if !parent.DelegateBuiltins || parent.AutoChildRunID == "" || parent.Adaptive.Stage != adaptive.Done || parent.Adaptive.Outcome != "approved" || parent.Adaptive.DiffRevision == "" {
		t.Fatalf("parent: %+v", parent)
	}
	child, err := ledger.Open(a.sdlcRunsDir(), parent.AutoChildRunID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentRunID != id || child.Adaptive.Stage != adaptive.Done || choices.calls != 2 || parent.TreeUsage.ChildRuns != 1 {
		t.Fatalf("child: %+v calls=%d usage=%+v", child, choices.calls, parent.TreeUsage)
	}
}

func TestDelegationFalseOverridesPolicyOn(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeFile(t, a.sdlcPolicyPath(), "version: 1\nadaptiveBuiltinDelegation: on\n")
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "add behavior", "--delegate-builtins=false", "--auto")
	if code != exitOK {
		t.Fatalf("disabled delegation: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	r, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.DelegateBuiltins || r.AutoChildRunID != "" || r.Adaptive.Stage != adaptive.Done {
		t.Fatalf("disabled delegation run: %+v", r)
	}
}

func TestUnavailableDelegationContinuesInParent(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeFile(t, a.sdlcPolicyPath(), "version: 1\nadaptiveBuiltinDelegation: on\n")
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "add behavior", "--auto")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	r, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Done || r.AutoChildRunID != "" || !r.AutoDecisionDone || !strings.Contains(r.AutoDecisionReason, "continued in parent") {
		t.Fatalf("delegation fallback: %+v", r)
	}
}
