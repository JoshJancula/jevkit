package compact

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
)

type decidingAsker struct {
	requests   []jev.Request
	badSalient bool
}

func rawFixture(t *testing.T, original string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original.log")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (a *decidingAsker) Ask(_ context.Context, req jev.Request) (*jev.Response, error) {
	a.requests = append(a.requests, req)
	if req.QuestionSetID == "compaction.triage.v2" {
		locus := "tail"
		outcome := "success"
		if strings.Contains(req.State, "markers_inside_elided_region: 1") {
			locus, outcome = "scattered", "failure"
		}
		return &jev.Response{Answers: map[string]jev.Answer{
			"evidence_locus":       jev.ChoiceAnswer{Choice: locus, Confidence: 0.98, Probabilities: map[string]float64{locus: 0.98}},
			"outcome":              jev.ChoiceAnswer{Choice: outcome, Confidence: 0.98, Probabilities: map[string]float64{outcome: 0.98}},
			"content_kind":         jev.ChoiceAnswer{Choice: "build-compile", Confidence: 0.98, Probabilities: map[string]float64{"build-compile": 0.98}},
			"retention_budget":     jev.ScoreAnswer{Score: 0.2, Confidence: 0.98},
			"tail_explains":        jev.NoulAnswer{Noul: 0.99},
			"head_explains":        jev.NoulAnswer{Noul: 0.01},
			"middle_omission_safe": jev.NoulAnswer{Noul: 0.99},
			"single_failure_site":  jev.NoulAnswer{Noul: 0.01},
		}}, nil
	}
	if req.QuestionSetID == "compaction.salient-lines.v2" {
		var chosen string
		for line := range strings.SplitSeq(req.State, "\n") {
			if strings.Contains(line, "ERROR: middle failure") {
				chosen = strings.Fields(line)[0]
				break
			}
		}
		answers := map[string]jev.Answer{}
		for _, role := range []string{"outcome_line", "cause_line", "location_line", "tally_line", "next_step_line", "second_site_line"} {
			pick := "NONE"
			if role == "cause_line" {
				pick = chosen
			}
			if role == "outcome_line" && a.badSalient {
				pick = "L999999"
			}
			answers[role] = jev.ChoiceAnswer{Choice: pick, Confidence: 0.98, Probabilities: map[string]float64{pick: 0.98}}
		}
		answers["has_failure"] = jev.NoulAnswer{Noul: 0.99}
		answers["selection_covers"] = jev.NoulAnswer{Noul: 0.99}
		return &jev.Response{Answers: answers}, nil
	}
	return nil, fmt.Errorf("unexpected set %s", req.QuestionSetID)
}

func TestV2RejectsMissingOrMismatchedRawPointer(t *testing.T) {
	original := strings.Repeat("compiling module 1234567890\n", 300)
	for _, pointer := range []string{filepath.Join(t.TempDir(), "missing.log"), rawFixture(t, "different output")} {
		a := &decidingAsker{}
		got, result := JevCompact("custom-build", original, "", 0, a, JevOptions{Enabled: true, RawPointer: pointer, ThresholdBytes: 200})
		if got.Body != original || result.Compacted || len(a.requests) != 0 {
			t.Fatalf("unretrievable original was compacted: %+v", result)
		}
	}
}

func TestV2CumulativeMassPassesRegistryThreshold(t *testing.T) {
	original := strings.Repeat("compiling module 1234567890\n", 300) + "SUMMARY: complete\n"
	answer := &jev.Response{Answers: map[string]jev.Answer{
		"evidence_locus":       jev.ChoiceAnswer{Choice: "tail", Confidence: 0.45, Probabilities: map[string]float64{"tail": 0.45, "head": 0.44, "throughout": 0.11}},
		"outcome":              jev.ChoiceAnswer{Choice: "success", Confidence: 0.99},
		"content_kind":         jev.ChoiceAnswer{Choice: "build-compile", Confidence: 0.99},
		"retention_budget":     jev.ScoreAnswer{Score: 0, Confidence: 0.99},
		"tail_explains":        jev.NoulAnswer{Noul: 0.99},
		"middle_omission_safe": jev.NoulAnswer{Noul: 0.99},
	}}
	a := &fakeAsker{resp: answer}
	got, result := JevCompact("custom-build", original, "", 0, a, JevOptions{Enabled: true, RawPointer: rawFixture(t, original), ThresholdBytes: 200, AuthoritativeExit: true})
	if !got.Used || !result.Compacted || !strings.Contains(got.Body, "SUMMARY: complete") || len(a.reqs) != 1 {
		t.Fatalf("cumulative mass not used: %+v, calls=%d", result, len(a.reqs))
	}
}

func TestV2NowhereRetainsSummaryOutsideTwoLineTail(t *testing.T) {
	original := strings.Repeat("compiling module 1234567890\n", 300) + "SUMMARY: complete\nfinishing a\nfinishing b\nfinishing c\nfinishing d\n"
	answer := &jev.Response{Answers: map[string]jev.Answer{
		"evidence_locus":       jev.ChoiceAnswer{Choice: "nowhere", Confidence: 0.99, Probabilities: map[string]float64{"nowhere": 0.99}},
		"outcome":              jev.ChoiceAnswer{Choice: "success", Confidence: 0.99},
		"content_kind":         jev.ChoiceAnswer{Choice: "build-compile", Confidence: 0.99},
		"retention_budget":     jev.ScoreAnswer{Score: 0, Confidence: 0.99},
		"tail_explains":        jev.NoulAnswer{Noul: 0.99},
		"middle_omission_safe": jev.NoulAnswer{Noul: 0.99},
	}}
	a := &fakeAsker{resp: answer}
	got, _ := JevCompact("custom-build", original, "", 0, a, JevOptions{Enabled: true, RawPointer: rawFixture(t, original), ThresholdBytes: 200, AuthoritativeExit: true})
	if !strings.Contains(got.Body, "SUMMARY: complete") {
		t.Fatalf("success summary was omitted: %s", got.Body)
	}
}

func TestV2SalientRegistryFallbackUsesDeterministicResult(t *testing.T) {
	var source strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&source, "detail chunk %d lorem ipsum dolor sit amet abcdefghijklmnopqrstuvwxyz\n", i)
	}
	source.WriteString("ERROR: middle failure\n")
	for i := 100; i < 200; i++ {
		fmt.Fprintf(&source, "detail chunk %d lorem ipsum dolor sit amet abcdefghijklmnopqrstuvwxyz\n", i)
	}
	source.WriteString("SUMMARY: failure\n")
	a := &decidingAsker{badSalient: true}
	got, result := JevCompact("custom-build", source.String(), "", 1, a, JevOptions{Enabled: true, ThresholdBytes: 200, RawPointer: rawFixture(t, source.String()), AuthoritativeExit: true})
	if len(a.requests) != 2 || got.Used || !result.Compacted {
		t.Fatalf("salient fallback not honored: result=%+v calls=%d", result, len(a.requests))
	}
}

func TestV2TailRedactsAndUsesOneCall(t *testing.T) {
	secret := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	var source strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&source, "compiling unit %d with token=%s\n", i, secret)
	}
	source.WriteString("SUMMARY: build successful\n")
	a := &decidingAsker{}
	got, result := JevCompact("build --token="+secret, source.String(), "", 0, a, JevOptions{Enabled: true, ThresholdBytes: 200, RawPointer: rawFixture(t, source.String()), AuthoritativeExit: true})
	if !result.Compacted || !got.Used || len(a.requests) != 1 {
		t.Fatalf("result=%+v used=%t calls=%d", result, got.Used, len(a.requests))
	}
	if strings.Contains(a.requests[0].State, secret) || strings.Contains(got.Body, secret) {
		t.Fatal("secret escaped redaction")
	}
	if len(a.requests[0].Questions) != 8 || !strings.Contains(got.Body, "SUMMARY: build successful") {
		t.Fatal("triage questions or summary missing")
	}
}

func TestV2ScatteredUsesTwoCallsAndOriginalTags(t *testing.T) {
	var source strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&source, "detail chunk %d lorem ipsum dolor sit amet abcdefghijklmnopqrstuvwxyz\n", i)
	}
	source.WriteString("ERROR: middle failure\n")
	for i := 100; i < 200; i++ {
		fmt.Fprintf(&source, "detail chunk %d lorem ipsum dolor sit amet abcdefghijklmnopqrstuvwxyz\n", i)
	}
	source.WriteString("SUMMARY: failure\n")
	a := &decidingAsker{}
	got, _ := JevCompact("custom-build", source.String(), "", 1, a, JevOptions{Enabled: true, ThresholdBytes: 200, RawPointer: rawFixture(t, source.String()), AuthoritativeExit: true})
	if len(a.requests) != 2 || a.requests[1].QuestionSetID != "compaction.salient-lines.v2" {
		t.Fatalf("calls=%d", len(a.requests))
	}
	if !strings.Contains(got.Body, "ERROR: middle failure") || !strings.Contains(got.Body, "SUMMARY: failure") {
		t.Fatalf("diagnostic lines missing: %s", got.Body)
	}
	if strings.Contains(a.requests[1].State, "line(s) omitted") {
		t.Fatal("synthesized marker offered as candidate")
	}
}

func TestBodyRejectsSpoofedMarkerAndReorderedSource(t *testing.T) {
	lines := []string{"one", "... (3 line(s) omitted) ...", "three"}
	var body Body
	body.AppendHeader("tail", "success", "other", 1)
	body.AppendMarker(1)
	body.AppendVerbatim(1, lines)
	body.AppendVerbatim(2, lines)
	if !body.Verify(lines) {
		t.Fatal("verbatim marker-shaped line rejected")
	}
	body.lines[2].Text = "HALLUCINATED"
	if body.Verify(lines) {
		t.Fatal("mutated source accepted")
	}
	body.lines[2].Text = lines[1]
	body.lines[3].SrcIdx = 0
	if body.Verify(lines) {
		t.Fatal("reordered source accepted")
	}
	body.lines[3].SrcIdx = 2
	body.lines[1].Omitted = 2
	if body.Verify(lines) {
		t.Fatal("incorrect omission count accepted")
	}
}

func TestCumulativeMassSelectsConservativeTail(t *testing.T) {
	locus, ok := cumulativeLocus(jev.ChoiceAnswer{Choice: "tail", Confidence: 0.45, Probabilities: map[string]float64{"tail": 0.45, "head": 0.44, "throughout": 0.11}}, 0.85)
	if !ok || locus != "tail" {
		t.Fatalf("locus=%q ok=%t", locus, ok)
	}
}

func TestCumulativeMassProtectsAmbiguousOutcomeAndKind(t *testing.T) {
	outcome, _, ok := cumulativeOrderedChoice(jev.ChoiceAnswer{Choice: "success", Confidence: 0.46, Probabilities: map[string]float64{"success": 0.46, "failure": 0.44, "unknown": 0.10}}, outcomeOrder, 0.85)
	if !ok || outcome != "failure" {
		t.Fatalf("outcome=%q ok=%t", outcome, ok)
	}
	kind, _, ok := cumulativeOrderedChoice(jev.ChoiceAnswer{Choice: "build-compile", Confidence: 0.46, Probabilities: map[string]float64{"build-compile": 0.46, "data-query": 0.44, "other": 0.10}}, kindOrder, 0.85)
	if !ok || kind != "data-query" {
		t.Fatalf("kind=%q ok=%t", kind, ok)
	}
	if _, _, ok := cumulativeOrderedChoice(jev.ChoiceAnswer{Choice: "success", Confidence: 0.99, Probabilities: map[string]float64{"invented": 0.99}}, outcomeOrder, 0.85); ok {
		t.Fatal("unregistered probability label accepted")
	}
}
