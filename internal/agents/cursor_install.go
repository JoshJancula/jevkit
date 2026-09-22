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

const cursorBackupSuffix = ".jevkit-original"

// Install merges Cursor hooks into .cursor/hooks.json (project) or
// <ConfigDir>/.cursor/hooks.json (user). It is idempotent: a second apply
// leaves a single managed set. DryRun computes the merge without writing.
func (c *Cursor) Install(opts InstallOptions) error {
	path, err := cursorHooksPath(opts)
	if err != nil {
		return err
	}
	binary := opts.Binary
	if binary == "" {
		binary = c.binary()
	}

	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	out, err := mergeCursorHooks(existing, binary)
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
	if err := ensureCursorBackup(path, existing); err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

// Uninstall removes jevkit-managed Cursor hooks and restores the hooks file
// to the byte-exact original captured on first install.
func (c *Cursor) Uninstall(opts InstallOptions) error {
	path, err := cursorHooksPath(opts)
	if err != nil {
		return err
	}
	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	after, err := plannedCursorRestore(path, existing)
	if err != nil {
		return err
	}
	reportPreview(opts, path, existing, after)
	if opts.DryRun {
		return nil
	}
	return restoreCursorBackup(path)
}

func cursorHooksPath(opts InstallOptions) (string, error) {
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
			return "", errors.New("cursor install: project scope requires WorkDir")
		}
		return filepath.Join(opts.WorkDir, ".cursor", "hooks.json"), nil
	case "user":
		if opts.ConfigDir == "" {
			return "", errors.New("cursor install: user scope requires ConfigDir (home)")
		}
		return filepath.Join(opts.ConfigDir, ".cursor", "hooks.json"), nil
	default:
		return "", fmt.Errorf("cursor install: unknown scope %q", opts.Scope)
	}
}

func cursorHookCommand(binary, marker string) string {
	return binary + " " + marker
}

func mergeCursorHooks(existing []byte, binary string) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("cursor hooks: not a JSON object: %w", err)
		}
		if doc == nil {
			return nil, errors.New("cursor hooks: not a JSON object")
		}
	}

	// Cursor hooks.json is version 1 with camelCase event names.
	if _, ok := doc["version"]; !ok {
		doc["version"] = 1
	}

	hooksObj, _ := doc["hooks"].(map[string]any)
	if hooksObj == nil {
		hooksObj = map[string]any{}
	}

	postCmd := cursorHookCommand(binary, CursorPostToolMarker)

	// Legacy pre-tool wrapper entries are stripped but never replaced: jevkit
	// observes results directly and no longer rewrites host commands.
	hooksObj["preToolUse"] = stripManagedCursorHooks(asSlice(hooksObj["preToolUse"]))
	hooksObj["postToolUse"] = upsertCursorHookEntries(
		stripManagedCursorHooks(asSlice(hooksObj["postToolUse"])),
		[]map[string]any{
			{"command": postCmd, "matcher": cursorShellMatcher},
			{"command": postCmd, "matcher": cursorNativeMatcher},
			{"command": postCmd, "matcher": cursorMCPMatcher},
		},
	)
	hooksObj["afterShellExecution"] = upsertCursorHookEntries(
		stripManagedCursorHooks(asSlice(hooksObj["afterShellExecution"])),
		[]map[string]any{
			{"command": postCmd},
		},
	)

	doc["hooks"] = hooksObj
	return marshalSettings(doc)
}

func stripManagedCursorHooks(entries []any) []any {
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		cmd, _ := em["command"].(string)
		if isManagedCursorCommand(cmd) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func upsertCursorHookEntries(existing []any, managed []map[string]any) []any {
	out := append([]any{}, existing...)
	for _, entry := range managed {
		if cursorEntryPresent(out, entry) {
			continue
		}
		cp := make(map[string]any, len(entry))
		for k, v := range entry {
			cp[k] = v
		}
		out = append(out, cp)
	}
	return out
}

func cursorEntryPresent(entries []any, want map[string]any) bool {
	wantCmd, _ := want["command"].(string)
	wantMatcher, _ := want["matcher"].(string)
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := em["command"].(string)
		matcher, _ := em["matcher"].(string)
		if cmd == wantCmd && matcher == wantMatcher {
			return true
		}
	}
	return false
}

func isManagedCursorCommand(command string) bool {
	return strings.Contains(command, CursorHookMarker) || strings.Contains(command, legacyCursorHookMarker)
}

func ensureCursorBackup(hooksPath string, existing []byte) error {
	bak := hooksPath + cursorBackupSuffix
	if _, err := os.Stat(bak); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var body []byte
	if len(existing) == 0 {
		if _, err := os.Stat(hooksPath); errors.Is(err, os.ErrNotExist) {
			body = []byte(absentSentinel)
		} else if err != nil {
			return err
		} else {
			body = existing
		}
	} else {
		body = existing
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(bak, body)
}

func plannedCursorRestore(hooksPath string, existing []byte) ([]byte, error) {
	bak := hooksPath + cursorBackupSuffix
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
			for _, key := range []string{"preToolUse", "postToolUse", "afterShellExecution", "stop"} {
				entries := stripManagedCursorHooks(asSlice(hooksObj[key]))
				if len(entries) == 0 {
					delete(hooksObj, key)
				} else {
					hooksObj[key] = entries
				}
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

func restoreCursorBackup(hooksPath string) error {
	bak := hooksPath + cursorBackupSuffix
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stripCursorManagedInPlace(hooksPath)
		}
		return err
	}
	if string(data) == absentSentinel {
		_ = os.Remove(hooksPath)
		return os.Remove(bak)
	}
	if err := writeFileAtomic(hooksPath, data); err != nil {
		return err
	}
	return os.Remove(bak)
}

func stripCursorManagedInPlace(hooksPath string) error {
	existing, err := readFileOptional(hooksPath)
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
	for _, key := range []string{"preToolUse", "postToolUse", "afterShellExecution", "stop"} {
		entries := stripManagedCursorHooks(asSlice(hooksObj[key]))
		if len(entries) == 0 {
			delete(hooksObj, key)
		} else {
			hooksObj[key] = entries
		}
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
	return writeFileAtomic(hooksPath, out)
}
