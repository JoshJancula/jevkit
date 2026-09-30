package worker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
)

func TestRuntimeArgsApplyBeforePromptOnLaunchAndResume(t *testing.T) {
	for _, tc := range []struct {
		runtime, raw string
		want         []string
		defaultMode  string
	}{
		{"codex", `-c 'model_reasoning_effort="high"'`, []string{"-c", `model_reasoning_effort="high"`}, ""},
		{"claude", "--dangerously-skip-permissions --effort high", []string{"--dangerously-skip-permissions", "--effort", "high"}, "--permission-mode"},
		{"cursor", "--force", []string{"--force"}, "--mode"},
		{"opencode", "--variant high", []string{"--variant", "high"}, ""},
		{"antigravity", "--dangerously-skip-permissions --mode accept-edits", []string{"--dangerously-skip-permissions", "--mode", "accept-edits"}, ""},
	} {
		for _, role := range []string{"planner", "implementer", "assessor"} {
			for _, session := range []string{"", "prior-session"} {
				req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: tc.runtime, Model: "m", RuntimeArgs: tc.raw}, Assignment: adaptive.Assignment{Role: role}, SessionID: session}
				_, args, err := command(req)
				if err != nil {
					t.Fatal(err)
				}
				i := slices.Index(args, tc.want[0])
				if tc.runtime == "codex" && session != "" {
					i = len(args) - len(tc.want)
				}
				if i < 0 || i+len(tc.want) > len(args) || !slices.Equal(args[i:i+len(tc.want)], tc.want) {
					t.Fatalf("%s/%s/%s: arguments missing or reordered: %v", tc.runtime, role, session, args)
				}
				if tc.defaultMode != "" && slices.Contains(args, tc.defaultMode) {
					t.Fatalf("user permission choice overridden: %v", args)
				}
				if tc.runtime == "codex" && session == "" && !slices.Contains(args, "--sandbox") {
					t.Fatalf("reasoning setting removed default sandbox: %v", args)
				}
				if tc.runtime == "cursor" || tc.runtime == "opencode" || tc.runtime == "antigravity" {
					if args[len(args)-1] != makePrompt(req) {
						t.Fatalf("runtime arguments changed positional prompt: %v", args)
					}
				}
			}
		}
	}
}

func TestRuntimeArgsRespectExplicitReadOnlyAndToolRestrictions(t *testing.T) {
	for _, runtime := range []string{"codex", "claude", "cursor", "antigravity"} {
		raw := "--dangerously-skip-permissions"
		switch runtime {
		case "codex":
			raw = "--dangerously-bypass-approvals-and-sandbox"
		case "cursor":
			raw = "--force"
		}
		req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: runtime, RuntimeArgs: raw}, Assignment: adaptive.Assignment{Role: "implementer", ReadOnly: true}}
		if _, _, err := command(req); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("%s override removed enforced read-only: %v", runtime, err)
		}
	}
	off := false
	req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: "codex", Model: "m", RuntimeArgs: `-c 'features.shell_tool=true'`, Tools: &enrollment.ToolPolicy{Shell: &off}}, Assignment: adaptive.Assignment{Role: "implementer"}}
	_, args, err := command(req)
	if err != nil || slices.Index(args, "features.shell_tool=true") >= slices.Index(args, "features.shell_tool=false") {
		t.Fatalf("raw config superseded enforced tool settings: %v %v", args, err)
	}
	for _, runtime := range []string{"claude", "opencode", "antigravity"} {
		req.Agent = enrollment.Agent{Via: enrollment.Runtime, Runtime: runtime, RuntimeAgent: "native", Model: "m", RuntimeArgs: "--future-option 'explicit value'"}
		_, args, err = command(req)
		if err != nil || !slices.Contains(args, "explicit value") || !slices.Contains(args, "--agent") {
			t.Fatalf("%s native agent lost explicit options: %v %v", runtime, args, err)
		}
	}
}

func TestRuntimeArgsExecuteLiteralValuesAndRejectStaleAssignment(t *testing.T) {
	requireUnixShellFixture(t)
	dir := gitFixture(t)
	outside := t.TempDir()
	capture, marker := filepath.Join(outside, "argv"), filepath.Join(outside, "marker")
	t.Setenv("JEVKIT_TEST_ARGV_CAPTURE", capture)
	bin := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$JEVKIT_TEST_ARGV_CAPTURE\"\ncat >/dev/null\nprintf '%s\\n' '{\"outcome\":\"answer\",\"content\":\"ok\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	literal := "$(touch " + marker + ") $HOME *.go"
	ag := enrollment.Agent{ID: "a", Via: enrollment.Runtime, Runtime: "claude", Model: "m", Binary: bin, RuntimeArgs: `--append-system-prompt "` + literal + `"`}
	req := Request{Agent: ag, Assignment: adaptive.Assignment{Runtime: "claude", Role: "planner", RuntimeArgsFingerprint: ag.RuntimeArgsFingerprint()}, WorkDir: dir}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil || reply.Outcome != "answer" {
		t.Fatalf("literal invocation: %+v %v", reply, err)
	}
	captured, err := os.ReadFile(capture)
	if err != nil || !strings.Contains(string(captured), literal+"\n") {
		t.Fatalf("argument expanded or split: %s %v", captured, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command substitution executed: %v", err)
	}
	req.Agent.RuntimeArgs = "--dangerously-skip-permissions"
	if _, err := (CLIExecutor{}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "runtimeArgs changed") {
		t.Fatalf("changed runtimeArgs reached execution: %v", err)
	}
}
