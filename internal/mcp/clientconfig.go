package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultServerName is the key under mcpServers that jevkit owns.
const DefaultServerName = "jevkit"

// ServerEntry is the client-config entry that launches the server. It has no
// env block: the server resolves the API key itself, so nothing secret is
// ever written to a client config.
func ServerEntry(command string) map[string]any {
	if command == "" {
		command = "jevkit"
	}
	return map[string]any{"command": command, "args": []any{"mcp", "start"}}
}

// ConfigDocument is a standalone client config holding only the entry.
func ConfigDocument(name, command string) ([]byte, error) {
	doc := map[string]any{"mcpServers": map[string]any{name: ServerEntry(command)}}
	return marshal(doc)
}

// MergeConfig sets mcpServers.<name> in an existing client config (nil or empty
// input starts a new one), leaving every other setting untouched. changed is
// false when the entry was already present and identical, so merging twice is
// a no-op.
func MergeConfig(existing []byte, name, command string) (out []byte, changed bool, err error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, false, fmt.Errorf("existing config is not a JSON object: %w", err)
		}
		if doc == nil {
			return nil, false, errors.New("existing config is not a JSON object")
		}
	}
	servers := map[string]any{}
	if v, ok := doc["mcpServers"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false, errors.New(`existing "mcpServers" is not an object`)
		}
		servers = m
	}
	want := ServerEntry(command)
	if cur, ok := servers[name]; ok {
		a, _ := json.Marshal(cur)
		b, _ := json.Marshal(want)
		if bytes.Equal(a, b) {
			return existing, false, nil
		}
	}
	servers[name] = want
	doc["mcpServers"] = servers
	out, err = marshal(doc)
	return out, true, err
}

// WriteConfigFile merges the entry into path, creating it (and its directory)
// if needed. The file is rewritten only when it changes, atomically, keeping
// its mode.
func WriteConfigFile(path, name, command string) (changed bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	out, changed, err := MergeConfig(existing, name, command)
	if err != nil || !changed {
		return false, err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".jevkit-mcp-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return false, err
	}
	_ = tmp.Chmod(mode) // best effort: unsupported on some platforms
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

func marshal(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
