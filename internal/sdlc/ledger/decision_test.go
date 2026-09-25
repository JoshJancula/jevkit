package ledger

import (
	"strings"
	"testing"
)

func TestDecisionReplayAndBoundedText(t *testing.T) {
	s := Open(t.TempDir(), "run")
	if got, err := s.ReadDecisions(); err != nil || len(got) != 0 {
		t.Fatalf("legacy ledger: %v %v", got, err)
	}
	d := Decision{RunID: "run", Kind: "agent-selection", Invocation: "inv", Candidates: []Candidate{{ID: "claude-builder"}}, Detail: strings.Repeat("x", 1000)}
	if err := s.AppendDecision(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadDecisions()
	if err != nil || len(got) != 1 {
		t.Fatalf("replay: %v %v", got, err)
	}
	if got[0].At == "" || len([]rune(got[0].Detail)) > 240 || got[0].Invocation != "inv" {
		t.Fatalf("decision: %+v", got[0])
	}
}
