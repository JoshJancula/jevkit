package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRuntimeMatricesCoverEveryKnownRuntime(t *testing.T) {
	matrices := RuntimeMatrices()
	if len(matrices) != len(KnownRuntimes) {
		t.Fatalf("expected %d runtimes, got %d", len(KnownRuntimes), len(matrices))
	}
	for _, name := range KnownRuntimes {
		m, ok := matrices[name]
		if !ok {
			t.Fatalf("missing matrix entry for %s", name)
		}
		if !m.WorkdirScoped || !m.Cancellation || !m.ChildCleanup {
			t.Fatalf("%s: workdir scoping and process-group cancellation/cleanup are enforced uniformly and must be true: %+v", name, m)
		}
		if len(m.VersionArgs) == 0 {
			t.Fatalf("%s: matrix must declare version probe args", name)
		}
	}
}

func TestRuntimeMatrixReadOnlyExecutionMatchesCommandBehavior(t *testing.T) {
	matrices := RuntimeMatrices()
	for _, name := range KnownRuntimes {
		m := matrices[name]
		wantReadOnly := name != "opencode"
		if m.ReadOnlyExecution != wantReadOnly {
			t.Fatalf("%s: ReadOnlyExecution=%v, want %v (matrix must reflect command()'s actual fail-closed behavior)", name, m.ReadOnlyExecution, wantReadOnly)
		}
		if !wantReadOnly && m.ReadOnlyUnenforceable == "" {
			t.Fatalf("%s: ReadOnlyExecution is false but no reason was recorded", name)
		}
		if wantReadOnly && m.ReadOnlyUnenforceable != "" {
			t.Fatalf("%s: ReadOnlyExecution is true but ReadOnlyUnenforceable is set: %q", name, m.ReadOnlyUnenforceable)
		}
		if !m.WritableExecution {
			t.Fatalf("%s: every known runtime must support a writable invocation", name)
		}
	}
}

func TestRuntimeMatrixOpenCodeDeclaresMissingShellHookCoverage(t *testing.T) {
	m := RuntimeMatrices()["opencode"]
	if m.ShellHookCoverage {
		t.Fatal("OpenCode's plugin never receives a pre-tool event; ShellHookCoverage must be false")
	}
	if m.ReadOnlyExecution {
		t.Fatal("OpenCode cannot enforce read-only; a falsely advertised read-only capability would let a read-only role write")
	}
}

func TestRuntimeMatrixAntigravityDeclaresPermissionBypassArgumentExplicitly(t *testing.T) {
	m := RuntimeMatrices()["antigravity"]
	if m.PermissionBypassArgument != "--dangerously-skip-permissions" {
		t.Fatalf("Antigravity's writable invocation passes --dangerously-skip-permissions; matrix must name it explicitly, got %q", m.PermissionBypassArgument)
	}
	if !strings.Contains(m.WritableApprovals, "--dangerously-skip-permissions") {
		t.Fatalf("Antigravity writable-approvals description must name the bypass flag: %q", m.WritableApprovals)
	}
	if strings.Contains(m.ReadOnlyApprovals, "--dangerously-skip-permissions") {
		t.Fatalf("Antigravity's read-only (plan mode) approvals must never mention the bypass flag: %q", m.ReadOnlyApprovals)
	}
	roOnly := RuntimeMatrices()["antigravity"]
	if strings.Contains(roOnly.ReadOnlyUnenforceable, "dangerously-skip-permissions") {
		t.Fatal("the bypass flag must never appear on the read-only (plan mode) path")
	}
}

func TestRuntimeMatrixOtherRuntimesNeverPassPermissionBypass(t *testing.T) {
	for _, name := range []string{"codex", "claude", "cursor", "opencode"} {
		if m := RuntimeMatrices()[name]; m.PermissionBypassArgument != "" {
			t.Fatalf("%s must not carry a permission-bypass argument, got %q", name, m.PermissionBypassArgument)
		}
	}
}

func TestRuntimeMatrixSessionResumeCoversEveryRuntime(t *testing.T) {
	matrices := RuntimeMatrices()
	for _, name := range KnownRuntimes {
		if !matrices[name].SessionResume {
			t.Fatalf("%s: command() must accept SessionID and pass a runtime-specific resume argument", name)
		}
	}
}

func TestRuntimeMatrixShellHookCoverageMatchesAgentsPackage(t *testing.T) {
	matrices := RuntimeMatrices()
	for _, name := range KnownRuntimes {
		want := name != "opencode"
		if matrices[name].ShellHookCoverage != want {
			t.Fatalf("%s: ShellHookCoverage=%v, want %v", name, matrices[name].ShellHookCoverage, want)
		}
	}
}

func TestProbeVersionRunsBinaryAndReportsVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture is a POSIX shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-cli")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'fake-cli 9.9.9'; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, err := ProbeVersion(ctx, bin, []string{"--version"})
	if err != nil || version != "fake-cli 9.9.9" {
		t.Fatalf("probe version: %q %v", version, err)
	}
}

func TestProbeVersionFailsClosedWhenBinaryIsMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ProbeVersion(ctx, filepath.Join(t.TempDir(), "does-not-exist"), nil); err == nil {
		t.Fatal("expected a fail-closed error for a missing binary, not a silently assumed capability")
	}
}

func TestProbeVersionFailsClosedOnEmptyOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture is a POSIX shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "silent-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ProbeVersion(ctx, bin, []string{"--version"}); err == nil {
		t.Fatal("empty version output must not be treated as a successful probe")
	}
}
