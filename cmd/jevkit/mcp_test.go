package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/jev"
)

// syncBuf is a bytes.Buffer safe for the server goroutine and the test.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// runMCP serves session over a pipe, keeps stdin open until the last reply
// (id 3) arrives, as a real client would, then closes it and waits for exit.
func runMCP(t *testing.T, a *App, session string) (code int, out, errs string) {
	t.Helper()
	inR, inW := io.Pipe()
	stdout, stderr := &syncBuf{}, &syncBuf{}
	a.Stdout, a.Stderr, a.Stdin = stdout, stderr, inR
	done := make(chan int, 1)
	go func() { done <- a.Run([]string{"mcp", "start"}) }()
	if _, err := io.WriteString(inW, session); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stdout.String(), `"id":3`) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	_ = inW.Close()
	select {
	case code = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mcp start did not exit after stdin closed")
	}
	return code, stdout.String(), stderr.String()
}

const mcpSession = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"jev_classify_request","arguments":{"state":"route with ` + secretKey + `","options":["implement","investigate"]}}}
`

func replies(t *testing.T, out string) map[int]map[string]any {
	t.Helper()
	got := map[int]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout is not JSON-RPC: %q", line)
		}
		if id, ok := m["id"].(float64); ok {
			got[int(id)] = m
		}
	}
	return got
}

func TestMCPStartServesToolsOverStdio(t *testing.T) {
	a, _, _ := cliApp(t)
	a.NewJev = nil // real client, fixture transport: no key, no network
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture", "JEVKIT_FIXTURE_DIR=../../testdata/jev")
	code, out, errs := runMCP(t, a, mcpSession)
	if code != exitOK {
		t.Fatalf("exit %d\nstderr: %s", code, errs)
	}
	noSecret(t, "mcp start", out, errs)
	r := replies(t, out)
	tools := r[2]["result"].(map[string]any)
	if _, ok := tools["nextCursor"]; ok || len(tools["tools"].([]any)) != 4 {
		t.Errorf("tools/list = %v", tools)
	}
	sc := r[3]["result"].(map[string]any)["structuredContent"].(map[string]any)
	if sc["questionSetId"] != "graph.router-confidence" || sc["decision"].(map[string]any)["decision"] != "act" {
		t.Errorf("structuredContent = %v", sc)
	}
	if !strings.Contains(errs, "tool=jev_classify_request") {
		t.Errorf("server logs did not go to stderr: %q", errs)
	}
}

func TestMCPStartWithoutKeyIsSoftUnavailable(t *testing.T) {
	a, _, fj := cliApp(t)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	code, out, errs := runMCP(t, a, mcpSession)
	if code != exitOK {
		t.Fatalf("exit %d\nstderr: %s", code, errs)
	}
	sc := replies(t, out)[3]["result"].(map[string]any)["structuredContent"].(map[string]any)
	if sc["available"] != false || sc["reason"] != "no-key" {
		t.Errorf("structuredContent = %v", sc)
	}
	if fj.calls != 0 {
		t.Errorf("Jev was called %d times without a key", fj.calls)
	}
}

func TestMCPStartRejectsArguments(t *testing.T) {
	a, _, _ := cliApp(t)
	if code, _, _ := run(a, "", "mcp", "start", "extra"); code != exitUsage {
		t.Errorf("exit %d, want %d", code, exitUsage)
	}
}

func TestMCPConfigPrintsNoSecret(t *testing.T) {
	a, _, _ := cliApp(t)
	a.Environ = append(a.Environ, "JEVKIT_API_KEY="+secretKey)
	code, out, errs := run(a, "", "mcp", "config")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	noSecret(t, "mcp config", out, errs)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc["mcpServers"] == nil {
		t.Errorf("stdout is not a client config: %q (%v)", out, err)
	}
}

func TestMCPConfigNoKeyWarnsButSucceeds(t *testing.T) {
	a, _, _ := cliApp(t)
	code, out, errs := run(a, "", "mcp", "config")
	if code != exitOK || !strings.Contains(errs, "no API key") || strings.Contains(out, "warning") {
		t.Errorf("exit %d\nout: %s\nerr: %s", code, out, errs)
	}
}

func TestMCPConfigMergeIsIdempotent(t *testing.T) {
	a, _, _ := cliApp(t)
	a.Environ = append(a.Environ, "JEVKIT_API_KEY="+secretKey)
	p := filepath.Join(t.TempDir(), ".mcp.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers":{"other":{"command":"o"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run(a, "", "mcp", "config", "--merge", p)
	if code != exitOK || !strings.Contains(out, "updated") {
		t.Fatalf("exit %d\nout: %s\nerr: %s", code, out, errs)
	}
	first, _ := os.ReadFile(p)
	code, out, _ = run(a, "", "mcp", "config", "--merge", p)
	second, _ := os.ReadFile(p)
	if code != exitOK || !strings.Contains(out, "up to date") || string(first) != string(second) {
		t.Errorf("second merge not a no-op: %q", out)
	}
	if !strings.Contains(string(second), `"other"`) {
		t.Error("existing server was lost")
	}
	noSecret(t, "merged file", string(second))
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want preserved 0600", fi.Mode().Perm())
	}
}

func TestMCPConfigMergeBadFileFails(t *testing.T) {
	a, _, _ := cliApp(t)
	p := filepath.Join(t.TempDir(), ".mcp.json")
	_ = os.WriteFile(p, []byte("nope"), 0o600)
	if code, _, _ := run(a, "", "mcp", "config", "--merge", p); code != exitFail {
		t.Errorf("exit %d, want %d", code, exitFail)
	}
}

func TestMCPStatusReportsKeySource(t *testing.T) {
	a, _, _ := cliApp(t)
	code, out, _ := run(a, "", "mcp", "status")
	if code != exitOK || !strings.Contains(out, "key source:     none") || !strings.Contains(out, "WARN no API key") {
		t.Errorf("no key: exit %d\n%s", code, out)
	}
	a, _, _ = cliApp(t)
	a.Environ = append(a.Environ, "JEVKIT_API_KEY="+secretKey)
	code, out, errs := run(a, "", "mcp", "status")
	if code != exitOK || !strings.Contains(out, "key source:     env") || !strings.Contains(out, "preflight:      ok") {
		t.Errorf("env key: exit %d\n%s", code, out)
	}
	noSecret(t, "mcp status", out, errs)
}
