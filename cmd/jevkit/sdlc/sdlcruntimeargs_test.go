package sdlc

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestRuntimeArgsSuggestionEnrollmentAndOverride(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		flags       []string
	}{
		{"inherit", "--dangerously-skip-permissions --effort high", nil},
		{"replace", "--effort low", []string{"--runtime-args=--effort low"}},
		{"clear", "", []string{"--runtime-args="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newApp(t)
			testkit.WriteFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "agents.yaml"), `version: 1
agents:
  - id: custom-claude
    via: runtime
    runtime: claude
    model: test-model
    agent: custom-native
    rubric: Local changes
    tools: auto
    runtimeArgs: '--dangerously-skip-permissions --effort high'
`)
			args := append([]string{"sdlc", "agents", "add", "custom-claude", "--role", "all"}, tc.flags...)
			code, out, errs := run(a, "", args...)
			if code != app.ExitOK {
				t.Fatalf("add: %d %s %s", code, out, errs)
			}
			roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, agent := range roster.Agents {
				if agent.ID == "custom-claude" {
					found = true
					if agent.RuntimeArgs != tc.value || agent.RuntimeAgent != "custom-native" {
						t.Fatalf("enrolled settings: %+v", agent)
					}
				}
			}
			if !found {
				t.Fatal("suggestion was not enrolled")
			}
			code, out, errs = run(a, "", "sdlc", "agents")
			if code != app.ExitOK || tc.value != "" && !strings.Contains(out, "Runtime args:") {
				t.Fatalf("list: %d %s %s", code, out, errs)
			}
		})
	}
}

func TestSDLCDispatchRuntimeArgsAndSessionIdentity(t *testing.T) {
	a := newApp(t)
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"claude": {Write: true}}}
	}
	testkit.WriteFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - id: custom-claude
    via: runtime
    runtime: claude
    model: test-model
    roles: [planner, implementer, assessor]
    rubric: Local changes
    tools: auto
    runtimeArgs: '--dangerously-skip-permissions --effort high'
`)
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "answer", Content: "The requested explanation.", SessionID: "args-session"}}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "explain the project", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "drive", id, "--until-done")
	if code != app.ExitOK || len(f.requests) != 1 {
		t.Fatalf("drive: %d %s %s invocations=%d", code, out, errs, len(f.requests))
	}
	req := f.requests[0]
	if req.Agent.RuntimeArgs != "--dangerously-skip-permissions --effort high" || req.Assignment.RuntimeArgsFingerprint == "" || req.Assignment.RuntimeArgsFingerprint != req.Agent.RuntimeArgsFingerprint() {
		t.Fatalf("runtimeArgs lost during routing: %+v", req)
	}
	saved, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Sessions[sessionKey(req.Assignment)] != "args-session" {
		t.Fatal("runtime arguments not reflected in saved session")
	}
	changed := req.Assignment
	changed.RuntimeArgsFingerprint = (enrollment.Agent{RuntimeArgs: "--effort low"}).RuntimeArgsFingerprint()
	if saved.Sessions[sessionKey(changed)] != "" {
		t.Fatal("changed runtime arguments reused existing session")
	}
}
