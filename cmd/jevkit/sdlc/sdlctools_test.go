package sdlc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestSDLCDispatchCarriesToolPolicyAndSessionFingerprint(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	testkit.WriteFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - id: coder
    roles: [planner, implementer, assessor]
    rubric: Local repository work
    via: runtime
    runtime: codex
    model: test-model
    tools: {web: false, delegate: false}
`)
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "answer", Content: "The requested explanation.", SessionID: "policy-session"}}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "explain the project", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "drive", id, "--until-done")
	if code != app.ExitOK {
		t.Fatalf("drive: %d %s %s", code, out, errs)
	}
	if len(f.requests) != 1 {
		t.Fatalf("got %d invocations", len(f.requests))
	}
	req := f.requests[0]
	if req.Agent.Tools == nil || req.Assignment.ToolPolicyFingerprint == "" || req.Assignment.ToolPolicyFingerprint != req.Agent.Tools.Fingerprint() {
		t.Fatalf("policy missing from assignment: %+v", req)
	}
	saved, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Sessions[sessionKey(req.Assignment)] != "policy-session" {
		t.Fatalf("session saved under wrong tool policy: %+v", saved.Sessions)
	}
}

func TestAgentSuggestionCopiesToolsIntoRoster(t *testing.T) {
	a := newApp(t)
	dir := filepath.Join(a.WorkDir, ".jevkit", "sdlc")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := `version: 1
agents:
  - id: local-coder
    via: runtime
    runtime: codex
    model: test-model
    rubric: Local changes
    tools: {web: false, delegate: false}
`
	if err := os.WriteFile(filepath.Join(dir, "agents.yaml"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run(a, "", "sdlc", "agents", "add", "local-coder", "--role", "all")
	if code != app.ExitOK {
		t.Fatalf("add: %d %s %s", code, out, errs)
	}
	roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
	if err != nil {
		t.Fatal(err)
	}
	var found *enrollment.ToolPolicy
	for _, agent := range roster.Agents {
		if agent.ID == "local-coder" {
			found = agent.Tools
		}
	}
	if found == nil || found.Web == nil || *found.Web || found.Delegate == nil || *found.Delegate || found.Shell != nil {
		t.Fatalf("copied controls: %+v", found)
	}
	code, out, errs = run(a, "", "sdlc", "agents")
	if code != app.ExitOK || !strings.Contains(out, "web=false") || !strings.Contains(out, "delegate=false") {
		t.Fatalf("list: %d %s %s", code, out, errs)
	}
}

func TestNamedAgentShowsNativeToolOwnership(t *testing.T) {
	a := newApp(t)
	code, out, errs := run(a, "", "sdlc", "agents", "add", "reviewer", "--runtime", "claude", "--model", "test-model", "--agent", "security-reviewer", "--rubric", "Review", "--role", "assessor")
	if code != app.ExitOK {
		t.Fatalf("add: %d %s %s", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "agents")
	if code != app.ExitOK || !strings.Contains(out, "Tools: native runtime configuration") {
		t.Fatalf("list: %d %s %s", code, out, errs)
	}
}

func TestAutoToolSuggestionSurvivesEnrollment(t *testing.T) {
	a := newApp(t)
	testkit.WriteFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "agents.yaml"), `version: 1
agents:
  - id: inherited-cursor
    via: runtime
    runtime: cursor
    model: test-model
    rubric: Local changes
    tools: auto
`)
	code, out, errs := run(a, "", "sdlc", "agents", "add", "inherited-cursor", "--role", "all")
	if code != app.ExitOK {
		t.Fatalf("add: %d %s %s", code, out, errs)
	}
	roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
	if err != nil {
		t.Fatal(err)
	}
	var found *enrollment.ToolPolicy
	for _, agent := range roster.Agents {
		if agent.ID == "inherited-cursor" {
			found = agent.Tools
		}
	}
	if found == nil || !found.Auto || found.Fingerprint() != "" {
		t.Fatalf("explicit inheritance lost during enrollment: %+v", found)
	}
	code, out, errs = run(a, "", "sdlc", "agents")
	if code != app.ExitOK || !strings.Contains(out, "Tools: auto (runtime configuration)") {
		t.Fatalf("list: %d %s %s", code, out, errs)
	}
}
