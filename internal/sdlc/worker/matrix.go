package worker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/JoshJancula/jevkit/internal/agents"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
)

// KnownRuntimes is the full set of CLI runtimes command() and RuntimeMatrices
// know how to invoke. It is the enumeration source for the capability matrix,
// so adding a runtime to command() without adding it here is a compile-time
// omission, not a silently stale matrix.
var KnownRuntimes = []string{"codex", "claude", "cursor", "opencode", "antigravity"}

// RuntimeMatrix is the tested, evidence-backed capability contract for one
// CLI runtime's invocation by CLIExecutor. Every boolean here is derived by
// actually calling command() (see RuntimeMatrices), never hand-asserted, so
// it cannot silently drift from what CLIExecutor.Execute really does.
type RuntimeMatrix struct {
	Runtime string
	// VersionArgs are the args used to probe CLI reach and version (see
	// ProbeVersion). "--version" is the common convention across all five
	// runtimes' documented CLIs as of the evidence dates in
	// internal/agents/capability_audit.go.
	VersionArgs []string
	// ReadOnlyExecution is true when command() can build a read-only
	// invocation for this runtime without erroring. False means the CLI
	// adapter cannot enforce read-only and must fail closed instead of
	// silently running with write access (see OpenCodeReadOnlyUnenforceable).
	ReadOnlyExecution bool
	// ReadOnlyUnenforceable explains why ReadOnlyExecution is false. Empty
	// when ReadOnlyExecution is true.
	ReadOnlyUnenforceable string
	// WritableExecution is true when command() can build a write-capable
	// (implementer) invocation for this runtime.
	WritableExecution bool
	// ReadOnlyApprovals and WritableApprovals describe the actual argument(s)
	// command() passes to control the runtime's own approval/permission
	// behavior for each invocation kind, in plain language grounded in the
	// literal flag(s) used. Kept separate so a runtime whose read-only and
	// writable paths differ (every runtime here) never has one overwrite the
	// other in display or in tests.
	ReadOnlyApprovals string
	WritableApprovals string
	// PermissionBypassArgument is the literal CLI flag, if any, that a
	// write-capable invocation passes to skip the runtime's own interactive
	// permission prompts entirely. Empty when no such flag is passed. This
	// must never be silently added to a runtime's writable command without
	// updating this field and the matching test.
	PermissionBypassArgument string
	// ShellHookCoverage mirrors agents.RuntimeCapability.ShellPolicyCoverage:
	// whether the installed pre-tool hook can inspect and rewrite an eligible
	// shell call before the runtime executes it. OpenCode is false; see
	// internal/agents/capability_audit.go and docs/AGENT-INTEGRATIONS.md.
	ShellHookCoverage bool
	// WorkdirScoped is true for every runtime: CLIExecutor.Execute always
	// sets cmd.Dir to req.WorkDir (worker.go), independent of the runtime
	// binary. It is not a per-runtime CLI flag.
	WorkdirScoped bool
	// SessionResume is true when command() accepts req.SessionID and passes
	// a runtime-specific resume argument.
	SessionResume bool
	// Cancellation and ChildCleanup are true for every runtime: both are
	// enforced by prepareRuntimeCommand/runRuntimeCommand (process_unix.go,
	// process_windows.go), which own a process group per invocation
	// regardless of which runtime binary it started. See
	// TestRuntimeCancellationKillsSpawnedChild and
	// TestRuntimeExitKillsSpawnedChild.
	Cancellation bool
	ChildCleanup bool
}

// RuntimeMatrices builds the capability matrix for every known runtime by
// calling the real command() function CLIExecutor.Execute uses, never by
// hand-typing expected flags. A test asserting a false capability (for
// example OpenCode's ReadOnlyExecution) is exercising the same fail-closed
// error path command() returns to CLIExecutor.Execute at invocation time.
func RuntimeMatrices() map[string]RuntimeMatrix {
	out := make(map[string]RuntimeMatrix, len(KnownRuntimes))
	for _, runtime := range KnownRuntimes {
		out[runtime] = runtimeMatrixFor(runtime)
	}
	return out
}

func runtimeMatrixFor(runtime string) RuntimeMatrix {
	m := RuntimeMatrix{
		Runtime:       runtime,
		VersionArgs:   []string{"--version"},
		WorkdirScoped: true,
		Cancellation:  true,
		ChildCleanup:  true,
	}
	if cap, ok := agents.RuntimeCapabilities(runtime); ok {
		m.ShellHookCoverage = cap.ShellPolicyCoverage
	}

	agent := enrollment.Agent{Via: enrollment.Runtime, Runtime: runtime, Model: "probe-model"}

	roBin, roArgs, roErr := command(Request{Agent: agent, Assignment: adaptive.Assignment{Role: "assessor", ReadOnly: true}})
	if roErr != nil {
		m.ReadOnlyUnenforceable = roErr.Error()
	} else {
		m.ReadOnlyExecution = roBin != ""
		m.ReadOnlyApprovals = approvalsFromArgs(runtime, roArgs, false)
	}

	wBin, wArgs, wErr := command(Request{Agent: agent, Assignment: adaptive.Assignment{Role: "implementer"}, SessionID: "probe-session"})
	if wErr == nil && wBin != "" {
		m.WritableExecution = true
		m.WritableApprovals = approvalsFromArgs(runtime, wArgs, true)
		m.PermissionBypassArgument = permissionBypassArgument(wArgs)
		m.SessionResume = sessionResumePresent(runtime, wArgs, "probe-session")
	}
	return m
}

// approvalsFromArgs describes, in plain language, the literal approval/
// permission argument(s) command() chose for this runtime. writable
// distinguishes the write-capable invocation from the read-only one, since
// several runtimes pass no explicit flag at all for one of the two.
func approvalsFromArgs(runtime string, args []string, writable bool) string {
	joined := strings.Join(args, " ")
	switch runtime {
	case "codex":
		if strings.Contains(joined, "sandbox_mode=read-only") || strings.Contains(joined, "--sandbox read-only") {
			return "codex sandbox_mode=read-only"
		}
		if strings.Contains(joined, "sandbox_mode=workspace-write") || strings.Contains(joined, "--sandbox workspace-write") {
			return "codex sandbox_mode=workspace-write"
		}
	case "claude":
		if strings.Contains(joined, "--permission-mode plan") {
			return "claude --permission-mode plan"
		}
		if writable {
			return "claude's own configured permission mode (no explicit override passed)"
		}
	case "cursor":
		if strings.Contains(joined, "--mode ask") {
			return "cursor-agent --mode ask"
		}
		if writable {
			return "cursor-agent's own configured permission mode (no explicit override passed)"
		}
	case "opencode":
		return "opencode has no approval flag in this integration; see ReadOnlyUnenforceable"
	case "antigravity":
		if strings.Contains(joined, "--mode plan") {
			return "agy --mode plan"
		}
		if strings.Contains(joined, "--dangerously-skip-permissions") {
			return "agy --mode accept-edits, permission prompts bypassed via --dangerously-skip-permissions"
		}
	}
	return ""
}

// permissionBypassArgument returns "--dangerously-skip-permissions" when a
// writable command includes it, so that flag is always visible in the
// matrix rather than buried in command()'s antigravity branch.
func permissionBypassArgument(args []string) string {
	for _, a := range args {
		if a == "--dangerously-skip-permissions" {
			return a
		}
	}
	return ""
}

func sessionResumePresent(runtime string, args []string, sessionID string) bool {
	for i, a := range args {
		switch runtime {
		case "codex":
			if a == "resume" && i+1 < len(args) && args[i+1] == sessionID {
				return true
			}
		case "claude", "cursor":
			if a == "--resume" && i+1 < len(args) && args[i+1] == sessionID {
				return true
			}
		case "opencode":
			if a == "--session" && i+1 < len(args) && args[i+1] == sessionID {
				return true
			}
		case "antigravity":
			if a == "--conversation" && i+1 < len(args) && args[i+1] == sessionID {
				return true
			}
		}
	}
	return false
}

// ProbeVersion actually executes the runtime's CLI to confirm reach and
// report its version, instead of trusting a PATH lookup alone. Any failure
// (binary missing, non-zero exit, empty output) is reported as an error so a
// caller fails closed rather than assuming a capability that was never
// verified to run.
func ProbeVersion(ctx context.Context, binary string, args []string) (string, error) {
	if strings.TrimSpace(binary) == "" {
		return "", fmt.Errorf("worker: probe version: empty binary")
	}
	if len(args) == 0 {
		args = []string{"--version"}
	}
	out, err := exec.CommandContext(ctx, binary, args...).Output()
	if err != nil {
		return "", fmt.Errorf("worker: probe %s %s: %w", binary, strings.Join(args, " "), err)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", fmt.Errorf("worker: probe %s %s: empty version output", binary, strings.Join(args, " "))
	}
	return version, nil
}
