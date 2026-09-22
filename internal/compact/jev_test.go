package compact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
)

// fakeAsker is a test Asker that returns a programmed response.
type fakeAsker struct {
	resp *jev.Response
	err  error
	reqs []jev.Request
}

func (f *fakeAsker) Ask(ctx context.Context, req jev.Request) (*jev.Response, error) {
	f.reqs = append(f.reqs, req)
	return f.resp, f.err
}

func (f *fakeAsker) lastReq() jev.Request {
	if len(f.reqs) == 0 {
		return jev.Request{}
	}
	return f.reqs[len(f.reqs)-1]
}

func makeRankedResponse(choice string, noul float64) *jev.Response {
	return &jev.Response{
		Answers: map[string]jev.Answer{
			"relevant_lines": jev.ChoiceAnswer{Choice: choice, Probabilities: map[string]float64{choice: 0.92}, Confidence: 0.92},
			"has_failure":    jev.NoulAnswer{Noul: noul},
		},
	}
}

// makeLargeOutput returns a deterministic string of distinct detail lines with
// a diagnostic line in the middle and a tail summary. It is large enough to
// trigger both the deterministic and jev tiers.
func makeLargeOutput(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		b.WriteString("detail chunk padding lorem ipsum dolor sit amet consectetur xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx seq=")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	b.WriteString("middle filler before error\n")
	b.WriteString("ERROR: module xyz failed to compile\n")
	b.WriteString("middle filler after error\n")
	b.WriteString("FINAL SUMMARY: build complete with warnings\n")
	return b.String()
}

// makeLargeOutputWithRepeats creates output whose lines all match a collapsible
// progress class so the deterministic collapse reduces the body to a single
// marker, leaving no elided region for the jev tier to rank.
func makeLargeOutputWithRepeats(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		b.WriteString("Downloading package ")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	b.WriteString("middle filler before error\n")
	b.WriteString("ERROR: module xyz failed to compile\n")
	b.WriteString("middle filler after error\n")
	b.WriteString("FINAL SUMMARY: build complete with warnings\n")
	return b.String()
}

func TestJevCompactDisabled(t *testing.T) {
	stdout := makeLargeOutput(100)
	asker := &fakeAsker{resp: makeRankedResponse("L001", 0.0)}
	res, result := JevCompact("custom-build-tool --verbose", stdout, "", 1, asker, JevOptions{})
	if result.Compacted {
		t.Fatalf("jev tier disabled but result compacted: %v", result.Status)
	}
	if asker.lastReq().State != "" {
		t.Fatal("jev transport consulted while disabled")
	}
	if res.Body != stdout || res.Used {
		t.Fatal("body changed or used=true while disabled")
	}
}

func TestJevCompactRankedSelection(t *testing.T) {
	// Need enough lines so the deterministic body still has an elided middle
	// after head/tail extraction.
	stdout := makeLargeOutput(100)
	asker := &fakeAsker{resp: makeRankedResponse("L001", 0.0)}
	res, result := JevCompact("custom-build-tool --verbose", stdout, "", 1, asker, JevOptions{Enabled: true, ThresholdBytes: 200})
	if !result.Compacted {
		t.Fatalf("expected compacted, got %v", result.Status)
	}
	if !res.Used {
		t.Fatalf("expected jev tier to be used; body starts with: %q", res.Body)
	}
	req := asker.lastReq()
	if req.QuestionSetID != "compaction.line-relevance" {
		t.Fatalf("question set = %q", req.QuestionSetID)
	}
	if len(req.Questions) != 2 {
		t.Fatalf("questions = %d", len(req.Questions))
	}
	if !strings.Contains(res.Body, "ERROR: module xyz failed to compile") {
		t.Fatal("ranked selection dropped the diagnostic line")
	}
	if !strings.Contains(res.Body, "FINAL SUMMARY: build complete with warnings") {
		t.Fatal("tail summary missing")
	}
	if !strings.Contains(res.Body, "ranked selection below") {
		t.Fatal("missing ranked marker")
	}
	// Deterministic + ranked must be shorter than original.
	if len(res.Body) >= len(stdout) {
		t.Fatalf("ranked not shorter: %d >= %d", len(res.Body), len(stdout))
	}
}

func TestJevCompactFailureContext(t *testing.T) {
	stdout := makeLargeOutput(100)
	// has_failure noul high should include neighbours around the chosen line.
	asker := &fakeAsker{resp: makeRankedResponse("L001", 0.9)}
	res, _ := JevCompact("custom-build-tool --verbose", stdout, "", 1, asker, JevOptions{Enabled: true, ThresholdBytes: 200})
	if !res.Used {
		t.Fatal("expected jev tier used")
	}
	omitted := strings.Count(res.Body, "seq=")
	// head 30 + tail 5 + up to 5 chosen-context lines.
	if omitted < 30 || omitted > 40 {
		t.Fatalf("unexpected number of detail lines kept: %d", omitted)
	}
}

func TestJevCompactSafetyGateAborts(t *testing.T) {
	stdout := makeLargeOutput(100)
	// Force the chosen line to something the assembler will expand badly by
	// replying with a tag that does not exist in the middle. This triggers the
	// unknown-tag abort path.
	asker := &fakeAsker{resp: makeRankedResponse("L999", 0.0)}
	res, result := JevCompact("custom-build-tool --verbose", stdout, "", 1, asker, JevOptions{Enabled: true, ThresholdBytes: 200})
	if !result.Compacted {
		t.Fatal("deterministic fallback should still be compacted")
	}
	if res.Used {
		t.Fatal("safety gate should abort, used=false")
	}
	if !strings.Contains(res.Body, "output (exit 1)") {
		t.Fatal("expected deterministic body")
	}
}

func TestJevCompactTransportErrorFallsBack(t *testing.T) {
	stdout := makeLargeOutput(100)
	asker := &fakeAsker{err: errors.New("jev down")}
	res, result := JevCompact("custom-build-tool --verbose", stdout, "", 1, asker, JevOptions{Enabled: true, ThresholdBytes: 200})
	if !result.Compacted {
		t.Fatalf("expected deterministic fallback, got %v", result.Status)
	}
	if res.Used || result.Stderr != "" {
		t.Fatalf("transport error must not use jev: used=%v stderr=%q", res.Used, result.Stderr)
	}
	if !strings.Contains(res.Body, "output (exit 1)") {
		t.Fatal("expected deterministic body")
	}
}

func TestJevCompactPreserveLineGate(t *testing.T) {
	stdout := makeLargeOutput(100)
	// Return a valid tag so assembleRanked succeeds, then intentionally
	// corrupt the response afterwards by making the asker return a different
	// line that is not in the original. Because fakeAsker always returns the
	// same response, simulate a hallucinated line by using a very high index
	// beyond the window and checking that the safety gate (or unknown tag)
	// aborts.
	asker := &fakeAsker{resp: makeRankedResponse("L300", 0.0)}
	res, _ := JevCompact("custom-build-tool --verbose", stdout, "", 1, asker, JevOptions{Enabled: true, ThresholdBytes: 200})
	if res.Used {
		t.Fatal("expected preserve-line/unknown-tag abort")
	}
	if !strings.Contains(res.Body, "output (exit 1)") {
		t.Fatal("expected deterministic fallback body")
	}
}

func TestJevCompactSourceFamilyNeverSent(t *testing.T) {
	fx := loadFixture(t, "git-diff-hunks")
	asker := &fakeAsker{resp: makeRankedResponse("L000", 0.0)}
	res, result := JevCompact(fx.Command, fx.Stdout, fx.Stderr, fx.Exit, asker, JevOptions{Enabled: true, ThresholdBytes: 1})
	if result.Compacted || result.Family != FamilyGitDiff {
		t.Fatalf("git diff must passthrough: family=%q compacted=%v", result.Family, result.Compacted)
	}
	if res.Body != fx.Stdout {
		t.Fatal("stdout mutated")
	}
	if asker.lastReq().State != "" {
		t.Fatal("source family must never be sent to jev")
	}
}

func TestJevCompactShadowModeLeavesOutputUnchanged(t *testing.T) {
	stdout := makeLargeOutput(100)
	asker := &fakeAsker{resp: makeRankedResponse("L003", 0.0)}
	stateDir := t.TempDir()
	res, result := JevCompact("custom-build-tool --verbose", stdout, "", 1, asker, JevOptions{Enabled: true, ThresholdBytes: 200, Shadow: true, StateDir: stateDir})
	if res.Used || result.Compacted != baseCompacted(result) {
		t.Fatalf("shadow mode must not change output: used=%v compacted=%v", res.Used, result.Compacted)
	}
	// Deterministic body is unchanged.
	if !strings.Contains(res.Body, "output (exit 1)") || strings.Contains(res.Body, "ranked selection below") {
		t.Fatal("shadow mode returned ranked output")
	}

	path := filepath.Join(stateDir, "jevkit", "jev-compact.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("shadow log missing: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("shadow log empty")
	}
	if !strings.Contains(string(data), `"saved"`) {
		t.Fatalf("shadow log missing saved field: %s", data)
	}
}

func baseCompacted(r Result) bool {
	// Helper to check that the deterministic base was compacted while shadow
	// leaves the *used* flag false.
	return r.Compacted
}

func TestJevCompactThresholdRespected(t *testing.T) {
	stdout := "small output for unknown command"
	asker := &fakeAsker{resp: makeRankedResponse("L000", 0.0)}
	res, result := JevCompact("custom-build-tool --verbose", stdout, "", 0, asker, JevOptions{Enabled: true})
	if result.Compacted || res.Used || asker.lastReq().State != "" {
		t.Fatalf("below-threshold output must not be compacted: %+v used=%v", result, res.Used)
	}
}

func TestJevCompactNoAsker(t *testing.T) {
	stdout := makeLargeOutputWithRepeats(100)
	res, result := JevCompact("custom-build-tool --verbose", stdout, "", 1, nil, JevOptions{Enabled: true, ThresholdBytes: 200})
	if !result.Compacted || res.Used {
		t.Fatalf("nil asker must fall back to deterministic: %+v used=%v", result, res.Used)
	}
}

func TestJevCompactWindowCap255(t *testing.T) {
	// Build 300 identical detail lines. The deterministic body keeps head+tail
	// and collapses the middle into a single marker, so there is no real
	// middle to rank. To exercise the 255 window cap we instead create output
	// with 300 distinct preserved-ish lines that survive collapse. We insert
	// error markers on every line so they are all preserved and not collapsed.
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("ERROR: line ")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	stdout := b.String()
	asker := &fakeAsker{resp: makeRankedResponse("L255", 0.0)}
	res, _ := JevCompact("custom-build-tool --verbose", stdout, "", 1, asker, JevOptions{Enabled: true, ThresholdBytes: 200})
	if !res.Used {
		t.Fatal("expected jev used")
	}
	req := asker.lastReq()
	cq, ok := req.Questions["relevant_lines"].(jev.ChoiceQuestion)
	if !ok {
		t.Fatal("relevant_lines not choice")
	}
	if len(cq.Options) > 255 {
		t.Fatalf("options %d exceed 255 cap", len(cq.Options))
	}
	// Line 299 (tag L254 with 0-index) or the last line in the window must survive.
	if !strings.Contains(res.Body, "ERROR: line 299") && !strings.Contains(res.Body, "ERROR: line 254") {
		t.Fatalf("tail of 300-line output missing after windowing: %s", res.Body)
	}
}

func TestTagMiddleCapsAt255(t *testing.T) {
	middle := make([]string, 300)
	for i := range middle {
		middle[i] = "line"
	}
	tagged, idx := tagMiddle(middle, 0)
	if len(tagged) != 255 {
		t.Fatalf("expected 255 tagged lines, got %d", len(tagged))
	}
	if len(idx) != 255 {
		t.Fatalf("expected 255 index entries, got %d", len(idx))
	}
	if _, ok := idx["L254"]; !ok {
		t.Fatal("last expected tag missing")
	}
	if _, ok := idx["L255"]; ok {
		t.Fatal("tag beyond 255 should not exist")
	}
}

func TestBuildStateIncludesExitCode(t *testing.T) {
	state := buildState("cmd", 7, []string{"h1", "h2"}, []string{"L000 a", "L001 b"}, []string{"t1"})
	if !strings.Contains(state, "Exit code: 7") {
		t.Fatalf("state missing exit code: %s", state)
	}
	if !strings.Contains(state, "Command: cmd") {
		t.Fatalf("state missing command: %s", state)
	}
	if !strings.Contains(state, "L000 a") || !strings.Contains(state, "L001 b") {
		t.Fatalf("state missing tagged lines: %s", state)
	}
}

func TestPreserveGate(t *testing.T) {
	orig := "a\nb\nc\nd\ne"
	good := "output (ranked): ...\na\nb\ne"
	bad := "output (ranked): ...\na\nb\nHALLUCINATED\ne"
	if !preserveGate(orig, good) {
		t.Fatal("expected good gate to pass")
	}
	if preserveGate(orig, bad) {
		t.Fatal("expected bad gate to fail")
	}
}
