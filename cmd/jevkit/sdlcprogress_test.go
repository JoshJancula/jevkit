package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

func TestProgressRendererKeepsRoutingReadable(t *testing.T) {
	a := newApp(t)
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.MaxAssignments = 20
	now := time.Now().UTC().Format(time.RFC3339)
	r := ledger.Run{RunID: "progress", Workflow: "feature", CreatedAt: now, UpdatedAt: now, Adaptive: &st, TreeUsage: &ledger.TreeUsage{}}
	store := ledger.Open(a.sdlcRunsDir(), r.RunID)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	reason := "Plan complex changes and clarify the approach before implementation or help solving or architecting a complex issue with many dependent components."
	if err := store.AppendEvent(ledger.Event{At: now, RunID: r.RunID, Stage: "planning", Agent: "claude-architect", Runtime: "claude", Invocation: "first", Reason: reason}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.sdlcProgress = &sdlcProgress{root: r.RunID, out: &out}
	a.progressFlush()
	a.progressFlush()
	got := out.String()
	if strings.Count(got, "feature: planning") != 1 || strings.Count(got, "left:") != 1 || strings.Count(got, "agent: claude-architect (claude)") != 1 {
		t.Fatalf("repeated progress: %q", got)
	}
	if strings.Contains(got, "run progress:") || !strings.Contains(got, "route: ") || !strings.Contains(got, "…") {
		t.Fatalf("unreadable progress: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if len([]rune(line)) > 120 {
			t.Fatalf("progress line exceeds 120 columns: %q", line)
		}
	}
}

func TestProgressRendererShowsPendingNoKeyRecovery(t *testing.T) {
	a := newApp(t)
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.PendingFocus = "research"
	st.PendingReason = "specialist decision unavailable: no-key"
	st.Pause("specialist-decision-unavailable")
	now := time.Now().UTC().Format(time.RFC3339)
	r := ledger.Run{RunID: "progress", Workflow: "feature", CreatedAt: now, UpdatedAt: now, Adaptive: &st, TreeUsage: &ledger.TreeUsage{}}
	if err := ledger.Open(a.sdlcRunsDir(), r.RunID).WriteRun(r); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.sdlcProgress = &sdlcProgress{root: r.RunID, out: &out}
	a.progressFlush()
	got := out.String()
	if !strings.Contains(got, "pending research: specialist decision unavailable: no-key") ||
		!strings.Contains(got, "jevkit key set; then jevkit sdlc resume progress") ||
		strings.Count(got, "specialist decision unavailable: no-key") != 1 {
		t.Fatalf("pending decision progress: %q", got)
	}
}

func TestProgressRendererExplainsAdvisoryDecision(t *testing.T) {
	a := newApp(t)
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.SpecialistDecisions = []adaptive.SpecialistDecision{{Role: "research", Revision: "plan", Choice: "skip", Confidence: 0.03, Reason: "skipped: fallback"}}
	now := time.Now().UTC().Format(time.RFC3339)
	r := ledger.Run{RunID: "progress", Workflow: "feature", CreatedAt: now, UpdatedAt: now, Adaptive: &st, TreeUsage: &ledger.TreeUsage{}}
	if err := ledger.Open(a.sdlcRunsDir(), r.RunID).WriteRun(r); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.sdlcProgress = &sdlcProgress{root: r.RunID, out: &out}
	a.progressFlush()
	a.progressFlush()
	got := out.String()
	if !strings.Contains(got, "specialist research: skipped: fallback (answer skip, confidence 0.03)") || strings.Count(got, "specialist research:") != 1 {
		t.Fatalf("decision progress: %q", got)
	}
}
