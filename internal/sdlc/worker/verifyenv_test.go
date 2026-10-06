package worker

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/security"
)

func TestClassifyEnvironmentFailure(t *testing.T) {
	goroot := security.ArgvResult{ExitCode: 1, Stderr: []byte("cmd/x/ui.go:4:2: package cmp is not in GOROOT (/usr/local/go/src/cmp)\n")}
	if got := classifyEnvironmentFailure([]string{"go", "test", "./..."}, goroot, nil); !strings.Contains(got, "Go toolchain") || !strings.Contains(got, "package cmp is not in GOROOT") || !strings.Contains(got, "go ") {
		t.Fatalf("stale toolchain not classified: %q", got)
	}
	missing := fmt.Errorf("exec: %q: %w", "nope", exec.ErrNotFound)
	if got := classifyEnvironmentFailure([]string{"nope"}, security.ArgvResult{}, missing); !strings.Contains(got, "not found") {
		t.Fatalf("missing binary not classified: %q", got)
	}
	if got := classifyEnvironmentFailure([]string{"sh", "-c", "x"}, security.ArgvResult{ExitCode: 127}, nil); !strings.Contains(got, "exit 127") {
		t.Fatalf("exit 127 not classified: %q", got)
	}
	testFail := security.ArgvResult{ExitCode: 1, Stdout: []byte("--- FAIL: TestThing\nFAIL\n")}
	if got := classifyEnvironmentFailure([]string{"go", "test", "./..."}, testFail, nil); got != "" {
		t.Fatalf("ordinary test failure classified as environment: %q", got)
	}
	if got := classifyEnvironmentFailure([]string{"go", "test"}, security.ArgvResult{TimedOut: true, ExitCode: -1}, nil); got != "" {
		t.Fatalf("timeout classified as environment: %q", got)
	}
}
