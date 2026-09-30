package sdlc

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func savedReviewAnswer(t *testing.T, a *App, id, answer string) {
	t.Helper()
	st, err := adaptive.New("review", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage, st.Outcome = adaptive.Done, "answer"
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(ledger.Run{RunID: id, Workflow: "review", WorkDir: a.WorkDir, Adaptive: &st}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("responses/inv.txt", []byte(answer)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDecision(ledger.Decision{RunID: id, Kind: "invocation-outcome", Invocation: "inv", Choice: "answer", Outcome: adaptive.Done}); err != nil {
		t.Fatal(err)
	}
}

func TestCompletedReviewCanStartBugfixWithSavedFindingsAndPlanApproval(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.Stdout, a.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Fix the reported race."}}}
	a.sdlcAutoChoice = true // An earlier review's --auto must not authorize implementation.
	a.sdlcSessionChoice = "resume"
	id := "review-source"
	savedReviewAnswer(t, a, id, "Must fix: a run state race.\nCheck the lock before writing.")
	answers := []bool{true, true, false, false, false} // suggestions, start, hooks, MCP, keep default
	var prompts []string
	a.Confirm = func(prompt string) (bool, error) {
		prompts = append(prompts, prompt)
		if len(answers) == 0 {
			t.Fatalf("unexpected prompt %q", prompt)
		}
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	if err := a.sdlcReviewFollowup(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 5 || !strings.Contains(prompts[0], "suggest changes") || !strings.Contains(prompts[1], "Start a bugfix SDLC") || !strings.Contains(prompts[2], "Install Jevkit hooks") || !strings.Contains(prompts[3], "Install Jevkit MCP") {
		t.Fatalf("follow-up prompts: %#v", prompts)
	}
	entries, err := os.ReadDir(a.SDLCRunsDir())
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected original and follow-up runs: %v, %v", entries, err)
	}
	var followup ledger.Run
	for _, entry := range entries {
		if entry.Name() != id {
			followup, err = ledger.Open(a.SDLCRunsDir(), entry.Name()).ReadRun()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if followup.Workflow != "bugfix" || !followup.RequirePlanApproval || followup.SessionStrategy != "" || followup.Adaptive.Stage != adaptive.Paused || followup.Adaptive.Outcome != "plan-approval-required" || !strings.Contains(followup.Task, "Must fix: a run state race") {
		t.Fatalf("follow-up did not preserve review or plan gate: %+v", followup)
	}
	artifact, err := ledger.Open(a.SDLCRunsDir(), followup.RunID).ReadArtifact("review.md")
	if err != nil || !strings.Contains(string(artifact), "Check the lock") {
		t.Fatalf("review handoff artifact: %q, %v", artifact, err)
	}
	decisions, err := ledger.Open(a.SDLCRunsDir(), id).ReadDecisions()
	if err != nil || decisions[len(decisions)-1].Kind != "review-followup" || decisions[len(decisions)-1].Next != followup.RunID {
		t.Fatalf("review follow-up link: %+v, %v", decisions, err)
	}
	if !a.sdlcAutoChoice || a.sdlcSessionChoice != "resume" {
		t.Fatal("original run choices were not restored")
	}
}

func TestCompletedReviewDeclineDoesNotStartImplementation(t *testing.T) {
	a := newApp(t)
	a.Stdout = &bytes.Buffer{}
	id := "review-declined"
	savedReviewAnswer(t, a, id, "Consider updating the tests.")
	prompts := 0
	a.Confirm = func(string) (bool, error) { prompts++; return false, nil }
	if err := a.sdlcReviewFollowup(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(a.SDLCRunsDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("declining created a run: %v, %v", entries, err)
	}
	if prompts != 1 {
		t.Fatalf("review without an obvious marker skipped operator confirmation: %d prompts", prompts)
	}
	decisions, err := ledger.Open(a.SDLCRunsDir(), id).ReadDecisions()
	if err != nil || decisions[len(decisions)-1].Choice != "no-changes" {
		t.Fatalf("decline not recorded: %+v, %v", decisions, err)
	}
}
