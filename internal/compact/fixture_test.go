package compact

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type fixtureCase struct {
	Command, Stdout, Stderr string
	Exit                    int
}

func loadFixture(t *testing.T, name string) fixtureCase {
	t.Helper()
	dir := filepath.Join("testdata", "compactors", name)
	read := func(base string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, base))
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		return string(b)
	}
	exitRaw := strings.TrimSpace(read("exit_status.txt"))
	exit, err := strconv.Atoi(exitRaw)
	if err != nil {
		t.Fatalf("exit_status: %v", err)
	}
	return fixtureCase{
		Command: strings.TrimSuffix(read("command.txt"), "\n"),
		Stdout:  read("stdout.txt"),
		Stderr:  read("stderr.txt"),
		Exit:    exit,
	}
}

func assertPreserved(t *testing.T, name string, wantFamily string) {
	t.Helper()
	fx := loadFixture(t, name)
	got := Compact(fx.Command, fx.Stdout, fx.Stderr, fx.Exit, Options{ThresholdBytes: 1})
	if got.Compacted || got.Status != StatusNotCompacted {
		t.Fatalf("%s: compacted=%v status=%q", name, got.Compacted, got.Status)
	}
	if got.Stdout != fx.Stdout || got.Stderr != fx.Stderr {
		t.Fatalf("%s: output mutated", name)
	}
	if wantFamily != "" && got.Family != wantFamily {
		t.Fatalf("%s: family=%q want %q", name, got.Family, wantFamily)
	}
	if wantFamily == "" && got.Family != "" {
		t.Fatalf("%s: family=%q want empty", name, got.Family)
	}
}
