package sdlc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestPromptCacheDecisionUsesJevAndDefaultsOff(t *testing.T) {
	a := newApp(t)
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	fake := &testkit.FakeJev{Resp: &jev.Response{Answers: map[string]jev.Answer{"cache": jev.NoulAnswer{Noul: 0.96}}, UsageReported: true, Usage: jev.Usage{InputTokens: 9}}}
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fake }
	id := "run-20260929T160856Z-cache123"
	run := ledger.Run{RunID: id, Adaptive: &adaptive.State{Stage: adaptive.Implementing, MaxAssignments: 3}}
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	req := worker.Request{Agent: enrollment.Agent{ID: "planner", Via: enrollment.Runtime, Runtime: "claude", Model: "sonnet"}, Assignment: adaptive.Assignment{InvocationID: "inv", Role: "planner"}}
	a.sdlcPromptCacheDecision(context.Background(), store, run, &req)
	if fake.Calls != 1 || req.PromptCache == nil || !*req.PromptCache {
		t.Fatalf("calls=%d cache=%v", fake.Calls, req.PromptCache)
	}
	if strings.Contains(fake.Req.State, "Task:") || !strings.Contains(fake.Req.State, "stablePrefixFingerprint") {
		t.Fatalf("unsafe cache state: %q", fake.Req.State)
	}
	decisions, err := store.ReadDecisions()
	if err != nil || len(decisions) != 1 || decisions[0].Choice != "enabled" {
		t.Fatalf("decisions=%+v err=%v", decisions, err)
	}
	fake.Err = context.DeadlineExceeded
	req.Assignment.InvocationID = "inv-2"
	a.sdlcPromptCacheDecision(context.Background(), store, run, &req)
	if req.PromptCache == nil || *req.PromptCache {
		t.Fatalf("cache enabled after Jev failure: %v", req.PromptCache)
	}
	a.Environ = append(a.Environ, "DISABLE_PROMPT_CACHING=1")
	before := fake.Calls
	fake.Err = nil
	req.Assignment.InvocationID = "inv-3"
	a.sdlcPromptCacheDecision(context.Background(), store, run, &req)
	if req.PromptCache == nil || *req.PromptCache || fake.Calls != before {
		t.Fatalf("user cache setting ignored: cache=%v calls=%d", req.PromptCache, fake.Calls)
	}
}

func TestCodexHookReadinessFindsMissingTrust(t *testing.T) {
	a := newApp(t)
	project, config := t.TempDir(), t.TempDir()
	a.Environ = append(a.Environ, "CODEX_HOME="+config)
	if err := os.MkdirAll(filepath.Join(project, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".codex", "hooks.json"), []byte(`{"hooks":{"PreToolUse":[{"command":"jevkit _runtime dispatch --protocol 1 codex pre-tool"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte("[hooks.state]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := a.codexHookReadiness(project); !strings.Contains(got, "trust review needed") {
		t.Fatal(got)
	}
}
