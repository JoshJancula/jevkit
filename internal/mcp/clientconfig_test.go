package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDocumentHasNoSecret(t *testing.T) {
	doc, err := ConfigDocument(DefaultServerName, "")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	e := got.MCPServers["jevkit"]
	if e["command"] != "jevkit" || len(e) != 2 {
		t.Errorf("entry = %v; want only command and args", e)
	}
	if _, ok := e["env"]; ok || strings.Contains(strings.ToLower(string(doc)), "key") {
		t.Errorf("config mentions a key/env: %s", doc)
	}
}

func TestMergeConfigPreservesAndIsIdempotent(t *testing.T) {
	in := []byte(`{"theme":"x","big":12345678901234567890,"mcpServers":{"other":{"command":"o"}}}`)
	out, changed, err := MergeConfig(in, "jevkit", "")
	if err != nil || !changed {
		t.Fatalf("first merge changed=%v err=%v", changed, err)
	}
	for _, want := range []string{`"other"`, `"theme": "x"`, "12345678901234567890", `"jevkit"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("merged config lost %s:\n%s", want, out)
		}
	}
	again, changed, err := MergeConfig(out, "jevkit", "")
	if err != nil || changed || string(again) != string(out) {
		t.Errorf("second merge changed=%v err=%v", changed, err)
	}
	// A stale entry is corrected, not duplicated.
	stale := []byte(`{"mcpServers":{"jevkit":{"command":"old","env":{"K":"v"}}}}`)
	fixed, changed, err := MergeConfig(stale, "jevkit", "")
	if err != nil || !changed || strings.Contains(string(fixed), "env") {
		t.Errorf("stale entry not replaced: %v %s", err, fixed)
	}
}

func TestMergeConfigRejectsBadShapes(t *testing.T) {
	for _, in := range []string{`[1]`, `null`, `nope`, `{"mcpServers":[]}`} {
		if _, _, err := MergeConfig([]byte(in), "jevkit", ""); err == nil {
			t.Errorf("%q: want error", in)
		}
	}
}

func TestWriteConfigFileIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", ".mcp.json")
	if changed, err := WriteConfigFile(p, "jevkit", ""); err != nil || !changed {
		t.Fatalf("create changed=%v err=%v", changed, err)
	}
	first, _ := os.ReadFile(p)
	if changed, err := WriteConfigFile(p, "jevkit", ""); err != nil || changed {
		t.Fatalf("second changed=%v err=%v", changed, err)
	}
	second, _ := os.ReadFile(p)
	if string(first) != string(second) {
		t.Error("file changed on second merge")
	}
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteConfigFile(p, "jevkit", ""); err == nil {
		t.Error("invalid existing file must not be overwritten")
	}
	if b, _ := os.ReadFile(p); string(b) != "not json" {
		t.Error("invalid file was clobbered")
	}
}
