package compact

import (
	"os"
	"path/filepath"
	"testing"
)

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "compaction.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPolicyMatchAndHardProtections(t *testing.T) {
	p, err := LoadPolicy(writePolicy(t, "version: 1\nrules:\n  - id: keep-generated\n    command: '^go generate'\n    action: never-compact\n  - id: tests\n    command: '^go test'\n    action: eligible\n    threshold_bytes: 2048\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Match("go test ./...", ""); got == nil || got.Threshold != 2048 {
		t.Fatalf("match = %+v", got)
	}
	large := "x\n"
	for len(large) < 9000 {
		large += "x\n"
	}
	if got := Compact("git diff", large, "", 0, Options{Policy: p, ThresholdBytes: 1}); got.Compacted {
		t.Fatal("policy bypassed hard source protection")
	}
	if got := Compact("go generate ./...", large, "", 0, Options{Policy: p, ThresholdBytes: 1}); got.Compacted {
		t.Fatal("never rule did not protect output")
	}
}

func TestProjectPolicyAndInvalidRulesRejected(t *testing.T) {
	for _, body := range []string{
		"version: 1\nrules:\n  - id: unsafe\n    command: '.*'\n    action: eligible\n",
		"version: 1\nrules:\n  - id: bad\n    command: '['\n    action: never-compact\n",
	} {
		if _, err := LoadPolicy(writePolicy(t, body), true); err == nil {
			t.Fatalf("accepted unsafe policy: %s", body)
		}
	}
}
