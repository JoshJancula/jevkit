package agents_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/agents"
	securityconfig "github.com/OWNER/jevkit/internal/security/config"
)

func TestRuntimeSpecificSecurityDenyBodies(t *testing.T) {
	cfg, err := securityconfig.Builtin(securityconfig.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		agent   agents.Agent
		payload map[string]any
		field   string
	}{
		{"claude", &agents.Claude{Security: cfg}, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf /"}, "cwd": "/tmp/proj"}, "permissionDecision"},
		{"codex", &agents.Codex{Security: cfg}, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf /"}, "cwd": "/tmp/proj"}, "permissionDecision"},
		{"cursor", &agents.Cursor{Security: cfg}, map[string]any{"hook_event_name": "preToolUse", "tool_name": "Shell", "tool_input": map[string]any{"command": "rm -rf /"}, "cwd": "/tmp/proj", "workspace_roots": []string{"/tmp/proj"}}, "permission"},
		{"antigravity", &agents.Antigravity{Security: cfg}, map[string]any{"toolCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "rm -rf /"}}, "workspacePaths": []string{"/tmp/proj"}}, "decision"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.payload)
			resp, err := tc.agent.HandlePreTool(context.Background(), agents.Request{Raw: raw, Event: agents.EventPreTool})
			if err != nil || !resp.Deny {
				t.Fatalf("response=%+v err=%v", resp, err)
			}
			var body map[string]any
			if err := json.Unmarshal(resp.Body, &body); err != nil {
				t.Fatal(err)
			}
			if tc.field == "permissionDecision" {
				hso, ok := body["hookSpecificOutput"].(map[string]any)
				if !ok || hso["permissionDecision"] != "deny" || hso["hookEventName"] != "PreToolUse" {
					t.Fatalf("wrong %s deny body: %s", tc.name, resp.Body)
				}
			} else if body[tc.field] != "deny" {
				t.Fatalf("wrong %s deny body: %s", tc.name, resp.Body)
			}
			if strings.Contains(string(resp.Body), "_runtime shell-wrapper") {
				t.Fatalf("denied command was rewritten: %s", resp.Body)
			}
		})
	}
}

func TestYoloReachesShellWrapperEnvironment(t *testing.T) {
	cfg, err := securityconfig.Builtin(securityconfig.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Yolo = true
	raw := json.RawMessage(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo hi"},"cwd":"/tmp/proj"}`)
	resp, err := (&agents.Codex{Security: cfg}).HandlePreTool(context.Background(), agents.Request{Raw: raw, Event: agents.EventPreTool})
	if err != nil || !strings.Contains(string(resp.Body), "JEVKIT_YOLO=1") || !strings.Contains(string(resp.Body), "JEVKIT_SECURITY_POLICY=") {
		t.Fatalf("wrapper did not receive policy environment: %s %v", resp.Body, err)
	}
}
