package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	jevmcp "github.com/OWNER/jevkit/internal/mcp"
)

const (
	mcpBackupSuffix = ".jevkit-original"
	codexMCPBegin   = "# BEGIN JEVKIT MCP"
	codexMCPEnd     = "# END JEVKIT MCP"
)

// InstallMCP merges the jevkit MCP server entry into the agent-specific
// client config. Idempotent; DryRun only reports a preview.
func InstallMCP(name string, opts InstallOptions) error {
	path, err := MCPConfigPath(name, opts)
	if err != nil {
		return err
	}
	binary := opts.Binary
	if binary == "" {
		binary = "jevkit"
	}
	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	out, err := mergeMCP(name, existing, binary)
	if err != nil {
		return err
	}
	reportPreview(opts, path, existing, out)
	if opts.DryRun {
		return nil
	}
	if bytes.Equal(existing, out) {
		return nil
	}
	if err := ensureMCPBackup(path, existing); err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

// UninstallMCP restores the MCP config from its first-install backup, or
// strips the jevkit entry when no backup exists.
func UninstallMCP(name string, opts InstallOptions) error {
	path, err := MCPConfigPath(name, opts)
	if err != nil {
		return err
	}
	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	var after []byte
	bak := path + mcpBackupSuffix
	if data, err := os.ReadFile(bak); err == nil {
		if string(data) == absentSentinel {
			after = nil
		} else {
			after = data
		}
	} else if errors.Is(err, os.ErrNotExist) {
		stripped, err := stripMCP(name, existing)
		if err != nil {
			return err
		}
		after = stripped
	} else {
		return err
	}
	reportPreview(opts, path, existing, after)
	if opts.DryRun {
		return nil
	}
	if after == nil {
		_ = os.Remove(path)
		_ = os.Remove(bak)
		return nil
	}
	if err := writeFileAtomic(path, after); err != nil {
		return err
	}
	_ = os.Remove(bak)
	return nil
}

func mergeMCP(name string, existing []byte, binary string) ([]byte, error) {
	switch name {
	case OpenCodeName:
		return mergeOpenCodeMCP(existing, binary)
	case CodexName:
		return mergeCodexMCP(existing, binary)
	default:
		out, _, err := jevmcp.MergeConfig(existing, jevmcp.DefaultServerName, binary)
		return out, err
	}
}

func stripMCP(name string, existing []byte) ([]byte, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return nil, nil
	}
	switch name {
	case OpenCodeName:
		return stripOpenCodeMCP(existing)
	case CodexName:
		return stripCodexMCP(existing), nil
	default:
		return stripJSONServersMCP(existing)
	}
}

func mergeOpenCodeMCP(existing []byte, binary string) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("opencode mcp: not a JSON object: %w", err)
		}
		if doc == nil {
			return nil, errors.New("opencode mcp: not a JSON object")
		}
	}
	mcpObj, _ := doc["mcp"].(map[string]any)
	if mcpObj == nil {
		mcpObj = map[string]any{}
	}
	entry := map[string]any{
		"type":    "local",
		"command": []any{binary, "mcp", "start"},
		"enabled": true,
	}
	if cur, ok := mcpObj[jevmcp.DefaultServerName]; ok {
		a, _ := json.Marshal(cur)
		b, _ := json.Marshal(entry)
		if bytes.Equal(a, b) {
			return existing, nil
		}
	}
	mcpObj[jevmcp.DefaultServerName] = entry
	doc["mcp"] = mcpObj
	return marshalSettings(doc)
}

func stripOpenCodeMCP(existing []byte) ([]byte, error) {
	doc := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(existing))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	mcpObj, _ := doc["mcp"].(map[string]any)
	if mcpObj == nil {
		return existing, nil
	}
	delete(mcpObj, jevmcp.DefaultServerName)
	if len(mcpObj) == 0 {
		delete(doc, "mcp")
	} else {
		doc["mcp"] = mcpObj
	}
	if len(doc) == 0 {
		return nil, nil
	}
	return marshalSettings(doc)
}

func stripJSONServersMCP(existing []byte) ([]byte, error) {
	doc := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(existing))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		return existing, nil
	}
	delete(servers, jevmcp.DefaultServerName)
	if len(servers) == 0 {
		delete(doc, "mcpServers")
	} else {
		doc["mcpServers"] = servers
	}
	if len(doc) == 0 {
		return nil, nil
	}
	return marshalSettings(doc)
}

func mergeCodexMCP(existing []byte, binary string) ([]byte, error) {
	stripped := stripCodexMCP(existing)
	block := codexMCPBlock(binary)
	if len(bytes.TrimSpace(stripped)) == 0 {
		return []byte(block), nil
	}
	body := strings.TrimRight(string(stripped), "\n") + "\n\n" + block
	return []byte(body), nil
}

func stripCodexMCP(existing []byte) []byte {
	s := string(existing)
	for {
		start := strings.Index(s, codexMCPBegin)
		if start < 0 {
			// Also strip a bare [mcp_servers.jevkit] table without markers.
			start = strings.Index(s, "[mcp_servers.jevkit]")
			if start < 0 {
				break
			}
			end := nextTOMLTable(s, start+1)
			s = s[:start] + s[end:]
			continue
		}
		end := strings.Index(s[start:], codexMCPEnd)
		if end < 0 {
			s = s[:start]
			break
		}
		end = start + end + len(codexMCPEnd)
		if end < len(s) && s[end] == '\n' {
			end++
		}
		s = s[:start] + s[end:]
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return []byte(trimmed + "\n")
}

func nextTOMLTable(s string, from int) int {
	lines := strings.SplitAfter(s[from:], "\n")
	off := from
	for _, line := range lines {
		off += len(line)
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && !strings.HasPrefix(trim, "[mcp_servers.jevkit") {
			return off - len(line)
		}
	}
	return len(s)
}

func codexMCPBlock(binary string) string {
	if binary == "" {
		binary = "jevkit"
	}
	return fmt.Sprintf(`%s
[mcp_servers.jevkit]
command = %q
args = ["mcp", "start"]
%s
`, codexMCPBegin, binary, codexMCPEnd)
}

func ensureMCPBackup(path string, existing []byte) error {
	bak := path + mcpBackupSuffix
	if _, err := os.Stat(bak); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var body []byte
	if len(existing) == 0 {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			body = []byte(absentSentinel)
		} else if err != nil {
			return err
		} else {
			body = existing
		}
	} else {
		body = existing
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(bak, body)
}
