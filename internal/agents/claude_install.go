package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backup sidecar next to settings.json. Holds the pre-install byte-exact
// original, or absentSentinel when the file did not exist.
const claudeBackupSuffix = ".jevkit-original"

const absentSentinel = "JEVKIT_ABSENT\n"

// Install merges the Claude PostToolUse:Bash hook into
// .claude/settings.json (project) or <ConfigDir>/.claude/settings.json (user).
// It is idempotent: a second apply leaves a single managed entry. DryRun
// computes the merge without writing.
func (c *Claude) Install(opts InstallOptions) error {
	path, err := claudeSettingsPath(opts)
	if err != nil {
		return err
	}
	binary := opts.Binary
	if binary == "" {
		binary = "jevkit"
	}
	command := claudeHookCommand(binary)

	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	out, err := mergeClaudeSettings(existing, command)
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
	if err := ensureClaudeBackup(path, existing); err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

// Uninstall removes jevkit-managed Claude hooks and restores the settings
// file to the byte-exact original captured on first install.
func (c *Claude) Uninstall(opts InstallOptions) error {
	path, err := claudeSettingsPath(opts)
	if err != nil {
		return err
	}
	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	after, err := plannedClaudeRestore(path, existing)
	if err != nil {
		return err
	}
	reportPreview(opts, path, existing, after)
	if opts.DryRun {
		return nil
	}
	return restoreClaudeBackup(path)
}

func claudeSettingsPath(opts InstallOptions) (string, error) {
	scope := strings.ToLower(strings.TrimSpace(opts.Scope))
	if scope == "" {
		if opts.WorkDir != "" {
			scope = "project"
		} else {
			scope = "user"
		}
	}
	switch scope {
	case "project":
		if opts.WorkDir == "" {
			return "", errors.New("claude install: project scope requires WorkDir")
		}
		return filepath.Join(opts.WorkDir, ".claude", "settings.json"), nil
	case "user":
		if opts.ConfigDir == "" {
			return "", errors.New("claude install: user scope requires ConfigDir (home)")
		}
		return filepath.Join(opts.ConfigDir, ".claude", "settings.json"), nil
	default:
		return "", fmt.Errorf("claude install: unknown scope %q", opts.Scope)
	}
}

func claudeHookCommand(binary string) string {
	return binary + " " + ClaudeHookMarker
}

func mergeClaudeSettings(existing []byte, command string) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("claude settings: not a JSON object: %w", err)
		}
		if doc == nil {
			return nil, errors.New("claude settings: not a JSON object")
		}
	}

	hooksObj, _ := doc["hooks"].(map[string]any)
	if hooksObj == nil {
		hooksObj = map[string]any{}
	}

	groups := asSlice(hooksObj["PostToolUse"])
	groups = stripManagedClaudeHooks(groups)
	groups = appendClaudeBashHook(groups, command)
	hooksObj["PostToolUse"] = groups
	preGroups := stripManagedClaudeHooks(asSlice(hooksObj["PreToolUse"]))
	preCommand := strings.Replace(command, ClaudeHookMarker, ClaudePreHookMarker, 1)
	hooksObj["PreToolUse"] = appendClaudeBashHook(preGroups, preCommand)
	doc["hooks"] = hooksObj

	return marshalSettings(doc)
}

func stripManagedClaudeHooks(groups []any) []any {
	out := make([]any, 0, len(groups))
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			out = append(out, g)
			continue
		}
		entries := asSlice(gm["hooks"])
		kept := make([]any, 0, len(entries))
		for _, e := range entries {
			em, ok := e.(map[string]any)
			if !ok {
				kept = append(kept, e)
				continue
			}
			cmd, _ := em["command"].(string)
			if isManagedClaudeCommand(cmd) {
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			// Drop empty matcher groups left behind after stripping.
			continue
		}
		cp := copyMap(gm)
		cp["hooks"] = kept
		out = append(out, cp)
	}
	return out
}

func appendClaudeBashHook(groups []any, command string) []any {
	entry := map[string]any{
		"type":    "command",
		"command": command,
	}
	for i, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		matcher, _ := gm["matcher"].(string)
		if matcher != "Bash" {
			continue
		}
		entries := asSlice(gm["hooks"])
		for _, e := range entries {
			em, _ := e.(map[string]any)
			if em != nil {
				if cmd, _ := em["command"].(string); cmd == command {
					return groups // already present
				}
			}
		}
		cp := copyMap(gm)
		cp["hooks"] = append(entries, entry)
		groups[i] = cp
		return groups
	}
	return append(groups, map[string]any{
		"matcher": "Bash",
		"hooks":   []any{entry},
	})
}

func isManagedClaudeCommand(command string) bool {
	return strings.Contains(command, ClaudeHookMarker) || strings.Contains(command, ClaudePreHookMarker) || strings.Contains(command, legacyClaudeHookMarker)
}

func ensureClaudeBackup(settingsPath string, existing []byte) error {
	bak := settingsPath + claudeBackupSuffix
	if _, err := os.Stat(bak); err == nil {
		return nil // first-install snapshot already taken
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var body []byte
	if len(existing) == 0 {
		if _, err := os.Stat(settingsPath); errors.Is(err, os.ErrNotExist) {
			body = []byte(absentSentinel)
		} else if err != nil {
			return err
		} else {
			body = existing
		}
	} else {
		body = existing
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(bak, body)
}

func plannedClaudeRestore(settingsPath string, existing []byte) ([]byte, error) {
	bak := settingsPath + claudeBackupSuffix
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if len(bytes.TrimSpace(existing)) == 0 {
				return nil, nil
			}
			doc := map[string]any{}
			dec := json.NewDecoder(bytes.NewReader(existing))
			dec.UseNumber()
			if err := dec.Decode(&doc); err != nil {
				return nil, err
			}
			hooksObj, _ := doc["hooks"].(map[string]any)
			if hooksObj == nil {
				return existing, nil
			}
			groups := stripManagedClaudeHooks(asSlice(hooksObj["PostToolUse"]))
			if len(groups) == 0 {
				delete(hooksObj, "PostToolUse")
			} else {
				hooksObj["PostToolUse"] = groups
			}
			preGroups := stripManagedClaudeHooks(asSlice(hooksObj["PreToolUse"]))
			if len(preGroups) == 0 {
				delete(hooksObj, "PreToolUse")
			} else {
				hooksObj["PreToolUse"] = preGroups
			}
			if len(hooksObj) == 0 {
				delete(doc, "hooks")
			} else {
				doc["hooks"] = hooksObj
			}
			return marshalSettings(doc)
		}
		return nil, err
	}
	if string(data) == absentSentinel {
		return nil, nil
	}
	return data, nil
}

func restoreClaudeBackup(settingsPath string) error {
	bak := settingsPath + claudeBackupSuffix
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No snapshot: strip managed entries in place as a best effort.
			return stripClaudeManagedInPlace(settingsPath)
		}
		return err
	}
	if string(data) == absentSentinel {
		_ = os.Remove(settingsPath)
		return os.Remove(bak)
	}
	if err := writeFileAtomic(settingsPath, data); err != nil {
		return err
	}
	return os.Remove(bak)
}

func stripClaudeManagedInPlace(settingsPath string) error {
	existing, err := readFileOptional(settingsPath)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(existing)) == 0 {
		return nil
	}
	doc := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(existing))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	hooksObj, _ := doc["hooks"].(map[string]any)
	if hooksObj == nil {
		return nil
	}
	groups := stripManagedClaudeHooks(asSlice(hooksObj["PostToolUse"]))
	if len(groups) == 0 {
		delete(hooksObj, "PostToolUse")
	} else {
		hooksObj["PostToolUse"] = groups
	}
	preGroups := stripManagedClaudeHooks(asSlice(hooksObj["PreToolUse"]))
	if len(preGroups) == 0 {
		delete(hooksObj, "PreToolUse")
	} else {
		hooksObj["PreToolUse"] = preGroups
	}
	if len(hooksObj) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooksObj
	}
	out, err := marshalSettings(doc)
	if err != nil {
		return err
	}
	return writeFileAtomic(settingsPath, out)
}

func readFileOptional(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func writeFileAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".jevkit-claude-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	_ = tmp.Chmod(mode)
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func marshalSettings(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func asSlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case nil:
		return nil
	default:
		return nil
	}
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
