package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
)

func newTestInvocationLog(t *testing.T, req Request, stream string) *invocationLog {
	t.Helper()
	log, err := newInvocationLog(req, stream)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.close(); err != nil {
			t.Error(err)
		}
	})
	return log
}

func TestInvocationLogRetainsBoundedTailAndMarksTruncation(t *testing.T) {
	dir := t.TempDir()
	req := Request{Agent: enrollment.Agent{ID: "reviewer", Runtime: "codex"}, Assignment: adaptive.Assignment{InvocationID: "inv-1"}, LogDir: dir}
	log := newTestInvocationLog(t, req, "stderr")
	if _, err := log.Write([]byte(strings.Repeat("a", MaxLogTail))); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write([]byte("last line\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "inv-1.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	wantMarker := "[earlier output truncated: 10 bytes omitted]\n"
	if !strings.HasPrefix(string(got), wantMarker) || !strings.HasSuffix(string(got), "last line\n") || len(got) > MaxLogTail+len(wantMarker) {
		t.Fatalf("invalid bounded tail: len=%d body=%q", len(got), truncate(string(got), 80))
	}
	if info, err := os.Stat(filepath.Join(dir, "inv-1.stderr")); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("private log: %v %v", info, err)
	}
}

func TestInvocationLogAccumulatesOmittedByteCountAcrossMultipleRollovers(t *testing.T) {
	dir := t.TempDir()
	req := Request{Agent: enrollment.Agent{ID: "reviewer", Runtime: "codex"}, Assignment: adaptive.Assignment{InvocationID: "inv-2"}, LogDir: dir, LogTailBytes: 100}
	log := newTestInvocationLog(t, req, "stdout")
	// Three writes of 60 bytes each against a 100-byte tail: the first stays
	// under bound, the second and third each roll the tail over once more,
	// so the omitted count must keep growing rather than reset per write.
	for i := 0; i < 3; i++ {
		if _, err := log.Write([]byte(strings.Repeat("b", 60))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "inv-2.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	wantMarker := "[earlier output truncated: 80 bytes omitted]\n"
	if !strings.HasPrefix(string(got), wantMarker) {
		t.Fatalf("expected cumulative omitted count in marker, got %q", truncate(string(got), 80))
	}
	if got2, _ := os.ReadFile(filepath.Join(dir, "inv-2.stdout")); len(got2)-len(wantMarker) != 100 {
		t.Fatalf("retained tail should stay at the configured bound: got %d bytes of body", len(got2)-len(wantMarker))
	}
}

func TestInvocationLogRespectsConfigurableTailBound(t *testing.T) {
	dir := t.TempDir()
	req := Request{Agent: enrollment.Agent{ID: "reviewer", Runtime: "codex"}, Assignment: adaptive.Assignment{InvocationID: "inv-3"}, LogDir: dir, LogTailBytes: 50}
	log := newTestInvocationLog(t, req, "stdout")
	if _, err := log.Write([]byte(strings.Repeat("c", 200))); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "inv-3.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "[earlier output truncated: 150 bytes omitted]\n") {
		t.Fatalf("custom LogTailBytes was not honored: %q", truncate(string(got), 80))
	}
}

func TestInvocationLogLinesJSONLTruncationRecordsOmittedBytes(t *testing.T) {
	dir := t.TempDir()
	req := Request{Agent: enrollment.Agent{ID: "agent", Runtime: "codex"}, Assignment: adaptive.Assignment{InvocationID: "inv-4"}, LogDir: dir, LogTailBytes: 200}
	out := newTestInvocationLog(t, req, "stdout")
	for i := 0; i < 40; i++ {
		if _, err := out.Write([]byte("a line of moderate length here\n")); err != nil {
			t.Fatal(err)
		}
	}
	markerPath := filepath.Join(dir, "inv-4.lines.jsonl.truncated")
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("expected a truncation marker with an omitted-byte count: %v", err)
	}
	omitted, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || omitted <= 0 {
		t.Fatalf("expected a positive omitted byte count, got %q", raw)
	}
	lines, err := os.ReadFile(filepath.Join(dir, "inv-4.lines.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(lines)) > 200 {
		t.Fatalf("lines.jsonl exceeded its configured bound: %d bytes", len(lines))
	}
}

func TestInvocationLogPreservesInterleavedCompleteLines(t *testing.T) {
	dir := t.TempDir()
	req := Request{Agent: enrollment.Agent{ID: "agent", Runtime: "codex"}, Assignment: adaptive.Assignment{InvocationID: "inv"}, LogDir: dir}
	out := newTestInvocationLog(t, req, "stdout")
	stderr := newTestInvocationLog(t, req, "stderr")
	_, _ = out.Write([]byte("first "))
	_, _ = out.Write([]byte("line\n"))
	_, _ = stderr.Write([]byte("second line\n"))
	_, _ = out.Write([]byte("third line\n"))
	raw, err := os.ReadFile(filepath.Join(dir, "inv.lines.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var got []LogLine
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var item LogLine
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatal(err)
		}
		got = append(got, item)
	}
	if len(got) != 3 || got[0].Text != "first line" || got[1].Stream != "stderr" || got[2].Text != "third line" || got[0].At > got[1].At || got[1].At > got[2].At {
		t.Fatalf("interleaved lines: %+v", got)
	}
}
