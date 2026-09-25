package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
)

func TestInvocationLogRetainsBoundedTailAndMarksTruncation(t *testing.T) {
	dir := t.TempDir()
	req := Request{Agent: enrollment.Agent{ID: "reviewer", Runtime: "codex"}, Assignment: adaptive.Assignment{InvocationID: "inv-1"}, LogDir: dir}
	log, err := newInvocationLog(req, "stderr")
	if err != nil {
		t.Fatal(err)
	}
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
	if !strings.HasPrefix(string(got), "[earlier output truncated]\n") || !strings.HasSuffix(string(got), "last line\n") || len(got) > MaxLogTail+40 {
		t.Fatalf("invalid bounded tail: len=%d", len(got))
	}
	if info, err := os.Stat(filepath.Join(dir, "inv-1.stderr")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private log: %v %v", info, err)
	}
}

func TestInvocationLogPreservesInterleavedCompleteLines(t *testing.T) {
	dir := t.TempDir()
	req := Request{Agent: enrollment.Agent{ID: "agent", Runtime: "codex"}, Assignment: adaptive.Assignment{InvocationID: "inv"}, LogDir: dir}
	out, err := newInvocationLog(req, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := newInvocationLog(req, "stderr")
	if err != nil {
		t.Fatal(err)
	}
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
