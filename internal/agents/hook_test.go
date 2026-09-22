package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/agents"
	"github.com/OWNER/jevkit/internal/usage"
)

// fakeAgent is a controllable adapter for framework tests.
type fakeAgent struct {
	name         string
	caps         agents.Capabilities
	passthrough  map[agents.Event][]byte
	pre          func(context.Context, agents.Request) (agents.Response, error)
	post         func(context.Context, agents.Request) (agents.Response, error)
	stop         func(context.Context, agents.Request) (agents.Response, error)
	installErr   error
	uninstallErr error
}

func (f *fakeAgent) Name() string                          { return f.name }
func (f *fakeAgent) Capabilities() agents.Capabilities     { return f.caps }
func (f *fakeAgent) Install(agents.InstallOptions) error   { return f.installErr }
func (f *fakeAgent) Uninstall(agents.InstallOptions) error { return f.uninstallErr }

func (f *fakeAgent) Passthrough(event agents.Event) []byte {
	if b, ok := f.passthrough[event]; ok {
		return b
	}
	return []byte(`{"decision":"allow"}`)
}

func (f *fakeAgent) HandlePreTool(ctx context.Context, req agents.Request) (agents.Response, error) {
	if f.pre != nil {
		return f.pre(ctx, req)
	}
	return agents.Response{Body: f.Passthrough(req.Event)}, nil
}

func (f *fakeAgent) HandlePostTool(ctx context.Context, req agents.Request) (agents.Response, error) {
	if f.post != nil {
		return f.post(ctx, req)
	}
	return agents.Response{Body: f.Passthrough(req.Event)}, nil
}

func (f *fakeAgent) HandleStop(ctx context.Context, req agents.Request) (agents.Response, error) {
	if f.stop != nil {
		return f.stop(ctx, req)
	}
	return agents.Response{Body: f.Passthrough(req.Event)}, nil
}

func baseFake() *fakeAgent {
	return &fakeAgent{
		name: "fake",
		caps: agents.Capabilities{PreTool: true, PostTool: true, Stop: true},
		passthrough: map[agents.Event][]byte{
			agents.EventPreTool:  []byte(`{"decision":"allow"}`),
			agents.EventPostTool: []byte(`{}`),
			agents.EventStop:     []byte(`{}`),
		},
	}
}

func runHook(t *testing.T, agent agents.Agent, event agents.Event, stdin string, opts agents.Options) (stdout string, code int, recs []usage.HookInvocation) {
	t.Helper()
	var mu sync.Mutex
	opts.AppendHook = func(_ string, rec usage.HookInvocation) error {
		mu.Lock()
		defer mu.Unlock()
		recs = append(recs, rec)
		return nil
	}
	if opts.StateDir == "" {
		opts.StateDir = t.TempDir()
	}
	var out bytes.Buffer
	code = agents.Run(context.Background(), agent, event, strings.NewReader(stdin), &out, opts)
	return strings.TrimSpace(out.String()), code, recs
}

func TestGarbageStdinFailOpen(t *testing.T) {
	agent := baseFake()
	for _, stdin := range []string{"", "not-json", "{", "null", "[]"} {
		out, code, recs := runHook(t, agent, agents.EventPreTool, stdin, agents.Options{})
		if code != 0 {
			t.Fatalf("stdin %q: exit %d", stdin, code)
		}
		if out != `{"decision":"allow"}` {
			t.Fatalf("stdin %q: body %q", stdin, out)
		}
		if len(recs) != 1 || recs[0].Outcome != agents.OutcomeGarbage {
			t.Fatalf("stdin %q: telemetry %+v", stdin, recs)
		}
	}
}

func TestPanicFailOpen(t *testing.T) {
	agent := baseFake()
	agent.pre = func(context.Context, agents.Request) (agents.Response, error) {
		panic("adapter blew up")
	}
	out, code, recs := runHook(t, agent, agents.EventPreTool, `{"tool_name":"Bash"}`, agents.Options{})
	if code != 0 || out != `{"decision":"allow"}` {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if len(recs) != 1 || recs[0].Outcome != agents.OutcomePanic || recs[0].Tool != "Bash" {
		t.Fatalf("telemetry %+v", recs)
	}
}

func TestTimeoutFailOpen(t *testing.T) {
	agent := baseFake()
	agent.pre = func(ctx context.Context, _ agents.Request) (agents.Response, error) {
		select {
		case <-ctx.Done():
			return agents.Response{}, ctx.Err()
		case <-time.After(time.Second):
			return agents.Response{Body: []byte(`{"ok":true}`)}, nil
		}
	}
	out, code, recs := runHook(t, agent, agents.EventPreTool, `{"tool_name":"Shell"}`, agents.Options{
		Timeout: 20 * time.Millisecond,
	})
	if code != 0 || out != `{"decision":"allow"}` {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if len(recs) != 1 || recs[0].Outcome != agents.OutcomeTimeout {
		t.Fatalf("telemetry %+v", recs)
	}
}

func TestVersionMismatchFailOpen(t *testing.T) {
	agent := baseFake()
	agent.pre = func(context.Context, agents.Request) (agents.Response, error) {
		t.Fatal("adapter must not run on version mismatch")
		return agents.Response{}, nil
	}
	payload := `{"protocol_version":99,"tool_name":"Bash"}`
	out, code, recs := runHook(t, agent, agents.EventPreTool, payload, agents.Options{})
	if code != 0 || out != `{"decision":"allow"}` {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if len(recs) != 1 || recs[0].Outcome != agents.OutcomeVersionMismatch {
		t.Fatalf("telemetry %+v", recs)
	}
}

func TestVersionMatchDispatches(t *testing.T) {
	agent := baseFake()
	agent.pre = func(_ context.Context, req agents.Request) (agents.Response, error) {
		if req.ProtocolVersion != agents.ProtocolVersion {
			t.Fatalf("version %d", req.ProtocolVersion)
		}
		return agents.Response{Body: []byte(`{"permission":"allow","updated_input":{"command":"jevkit exec -- true"}}`)}, nil
	}
	payload := `{"protocol_version":1,"tool_name":"Shell","tool_input":{"command":"true"}}`
	out, code, recs := runHook(t, agent, agents.EventPreTool, payload, agents.Options{})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !json.Valid([]byte(out)) || !strings.Contains(out, "jevkit exec") {
		t.Fatalf("body %q", out)
	}
	if len(recs) != 1 || recs[0].Outcome != agents.OutcomeOK {
		t.Fatalf("telemetry %+v", recs)
	}
}

func TestAbsentVersionAccepted(t *testing.T) {
	agent := baseFake()
	called := false
	agent.pre = func(_ context.Context, req agents.Request) (agents.Response, error) {
		called = true
		if req.ProtocolVersion != agents.ProtocolVersion {
			t.Fatalf("version %d", req.ProtocolVersion)
		}
		return agents.Response{Body: []byte(`{"decision":"allow"}`)}, nil
	}
	_, code, _ := runHook(t, agent, agents.EventPreTool, `{"tool_name":"Bash"}`, agents.Options{})
	if code != 0 || !called {
		t.Fatalf("code=%d called=%v", code, called)
	}
}

func TestDeliberateDeny(t *testing.T) {
	agent := baseFake()
	agent.pre = func(context.Context, agents.Request) (agents.Response, error) {
		return agents.Response{
			Body:     []byte(`{"decision":"deny","reason":"blocked"}`),
			Deny:     true,
			ExitCode: 2,
		}, nil
	}
	out, code, recs := runHook(t, agent, agents.EventPreTool, `{"tool_name":"Read"}`, agents.Options{})
	if code != 2 || !strings.Contains(out, `"deny"`) {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if len(recs) != 1 || recs[0].Outcome != agents.OutcomeDeny {
		t.Fatalf("telemetry %+v", recs)
	}
}

func TestUnknownAgentFailOpen(t *testing.T) {
	out, code, recs := runHook(t, nil, agents.EventPreTool, `{"tool_name":"Bash"}`, agents.Options{})
	if code != 0 || out != `{}` {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if len(recs) != 1 || recs[0].Outcome != agents.OutcomeUnknownAgent {
		t.Fatalf("telemetry %+v", recs)
	}
}

func TestPhase0FixturesFailOpenAndDispatch(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "hooks")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("testdata/hooks: %v", err)
	}
	agent := baseFake()
	agent.pre = func(_ context.Context, req agents.Request) (agents.Response, error) {
		return agents.Response{Body: append([]byte(nil), req.Raw...)}, nil
	}
	agent.post = agent.pre
	agent.stop = agent.pre

	for _, agentDir := range entries {
		if !agentDir.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(root, agentDir.Name(), "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			name := filepath.Base(file)
			if strings.Contains(name, "response") {
				continue
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			event := agents.EventPreTool
			switch {
			case strings.Contains(name, "posttool"), strings.Contains(name, "aftershell"), strings.Contains(name, "tool-execute-after"):
				event = agents.EventPostTool
			case strings.Contains(name, "stop"):
				event = agents.EventStop
			}
			t.Run(agentDir.Name()+"/"+name, func(t *testing.T) {
				out, code, recs := runHook(t, agent, event, string(raw), agents.Options{})
				if code != 0 {
					t.Fatalf("exit %d", code)
				}
				if !json.Valid([]byte(out)) {
					t.Fatalf("invalid JSON %q", out)
				}
				if len(recs) != 1 || recs[0].Outcome != agents.OutcomeOK {
					t.Fatalf("telemetry %+v", recs)
				}
			})
		}
	}
}

func TestRegistry(t *testing.T) {
	a := baseFake()
	a.name = "registry-test-agent"
	agents.Register(a)
	got := agents.Lookup("registry-test-agent")
	if got == nil || got.Name() != a.name {
		t.Fatalf("lookup %+v", got)
	}
}

func TestAppendHookPersists(t *testing.T) {
	dir := t.TempDir()
	agent := baseFake()
	var out bytes.Buffer
	code := agents.Run(context.Background(), agent, agents.EventPreTool,
		strings.NewReader(`{"tool_name":"Bash"}`), &out, agents.Options{StateDir: dir})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	path := usage.HookPath(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec usage.HookInvocation
	if err := json.Unmarshal(bytes.TrimSpace(data), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Agent != "fake" || rec.Outcome != agents.OutcomeOK || rec.Event != string(agents.EventPreTool) {
		t.Fatalf("%+v", rec)
	}
}
