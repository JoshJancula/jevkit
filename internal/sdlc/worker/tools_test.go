package worker

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
)

func TestNamedAgentInvocationsLeaveToolsAndPermissionModesToRuntime(t *testing.T) {
	for _, runtime := range []string{"claude", "opencode", "antigravity"} {
		for _, role := range []string{"planner", "implementer", "assessor"} {
			for _, session := range []string{"", "prior-session"} {
				t.Run(runtime+"/"+role+"/"+session, func(t *testing.T) {
					req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: runtime, RuntimeAgent: "custom-agent", Model: "m"}, Assignment: adaptive.Assignment{Role: role}, SessionID: session}
					_, args, err := command(req)
					if err != nil {
						t.Fatal(err)
					}
					i := slices.Index(args, "--agent")
					if i < 0 || i+1 >= len(args) || args[i+1] != "custom-agent" {
						t.Fatalf("native selection missing: %v", args)
					}
					for _, flag := range []string{"--tools", "--allowedTools", "--disallowedTools", "--permission-mode", "--mode", "--dangerously-skip-permissions", "--sandbox"} {
						if slices.Contains(args, flag) {
							t.Fatalf("native configuration overridden by %s: %v", flag, args)
						}
					}
					req.Assignment.ReadOnly = true
					if _, _, err := command(req); err == nil {
						t.Fatal("named agent incorrectly claimed to enforce Jevkit read-only")
					}
				})
			}
		}
	}
}

func TestToolRestrictionsApplyOnLaunchAndResume(t *testing.T) {
	off := false
	for _, tc := range []struct {
		runtime string
		want    []string
	}{
		{"codex", []string{"features.shell_tool=false", `web_search="disabled"`, "features.multi_agent=false", "--strict-config"}},
		{"claude", []string{"--disallowedTools", "Bash,PowerShell,WebSearch,WebFetch,Agent,Task"}},
	} {
		for _, session := range []string{"", "prior-session"} {
			t.Run(tc.runtime+"/"+session, func(t *testing.T) {
				req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: tc.runtime, Model: "m", Tools: &enrollment.ToolPolicy{Shell: &off, Web: &off, Delegate: &off}}, Assignment: adaptive.Assignment{Role: "implementer"}, SessionID: session}
				_, args, err := command(req)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range tc.want {
					if !slices.Contains(args, want) {
						t.Fatalf("missing %s: %v", want, args)
					}
				}
			})
		}
	}
}

func TestToolAllowancesNeverBypassRuntimeApprovals(t *testing.T) {
	on, off := true, false
	for _, runtime := range []string{"codex", "claude"} {
		req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: runtime, Model: "m"}, Assignment: adaptive.Assignment{Role: "assessor"}}
		_, baseline, err := command(req)
		if err != nil {
			t.Fatal(err)
		}
		req.Agent.Tools = &enrollment.ToolPolicy{Shell: &on, Web: &on, Delegate: &on}
		_, args, err := command(req)
		if err != nil || !slices.Equal(args, baseline) {
			t.Fatalf("true expanded native permissions: %v %v", args, err)
		}
		req.Agent.Tools = &enrollment.ToolPolicy{Web: &off}
		_, args, err = command(req)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--allowedTools") || strings.Contains(joined, "features.shell_tool") || strings.Contains(joined, "features.multi_agent") || strings.Contains(joined, "Bash") {
			t.Fatalf("web restriction changed another tool family: %v", args)
		}
	}
}

func TestToolPolicyRejectedBeforeExecution(t *testing.T) {
	off := false
	for _, runtime := range []string{"opencode", "cursor", "antigravity"} {
		req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: runtime, Tools: &enrollment.ToolPolicy{Web: &off}}}
		if _, _, err := command(req); err == nil {
			t.Fatalf("unsupported controls accepted for %s", runtime)
		}
	}
	req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: "claude", RuntimeAgent: "reviewer", Tools: &enrollment.ToolPolicy{Web: &off}}}
	if _, _, err := command(req); err == nil {
		t.Fatal("native tool override accepted")
	}
	req.Agent.RuntimeAgent = ""
	req.Assignment.Runtime = "claude"
	if _, err := (CLIExecutor{}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "tool settings changed") {
		t.Fatalf("stale assignment reached execution: %v", err)
	}
}

func TestAutoToolsPreserveLaunchAndResumeConfiguration(t *testing.T) {
	for _, runtime := range []string{"codex", "claude", "opencode", "cursor", "antigravity"} {
		for _, session := range []string{"", "prior-session"} {
			for _, native := range []bool{false, true} {
				if native && !enrollment.NamedAgentSupported(runtime) {
					continue
				}
				req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: runtime, Model: "m"}, Assignment: adaptive.Assignment{Role: "implementer"}, SessionID: session}
				if native {
					req.Agent.RuntimeAgent = "custom-agent"
				}
				_, baseline, err := command(req)
				if err != nil {
					t.Fatal(err)
				}
				req.Agent.Tools = &enrollment.ToolPolicy{Auto: true}
				_, args, err := command(req)
				if err != nil || !slices.Equal(baseline, args) || len(toolArgs(req)) != 0 {
					t.Fatalf("%s session=%q native=%t: auto changed invocation: %v %v", runtime, session, native, args, err)
				}
			}
		}
	}
}
