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

const codexBackupSuffix = ".jevkit-original"

// Install merges Codex hooks into .codex/hooks.json (project) or
// <ConfigDir>/.codex/hooks.json (user). It is idempotent: a second apply
// leaves a single managed set. DryRun computes the merge without writing.
func (c *Codex) Install(opts InstallOptions) error {
	path, err := codexHooksPath(opts)
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
	out, err := mergeCodexHooks(existing, binary)
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
	if err := ensureCodexBackup(path, existing); err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

// Uninstall removes jevkit-managed Codex hooks and restores the hooks file
// to the byte-exact original captured on first install.
func (c *Codex) Uninstall(opts InstallOptions) error {
	path, err := codexHooksPath(opts)
	if err != nil {
		return err
	}
	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	after, err := plannedCodexRestore(path, existing)
	if err != nil {
		return err
	}
	reportPreview(opts, path, existing, after)
	if opts.DryRun {
		return nil
	}
	return restoreCodexBackup(path)
}

func codexHooksPath(opts InstallOptions) (string, error) {
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
			return "", errors.New("codex install: project scope requires WorkDir")
		}
		return filepath.Join(opts.WorkDir, ".codex", "hooks.json"), nil
	case "user":
		if opts.ConfigDir == "" {
			return "", errors.New("codex install: user scope requires ConfigDir (home)")
		}
		return filepath.Join(opts.ConfigDir, ".codex", "hooks.json"), nil
	default:
		return "", fmt.Errorf("codex install: unknown scope %q", opts.Scope)
	}
}

func codexHookCommand(binary, marker string) string {
	return binary + " " + marker
}

func mergeCodexHooks(existing []byte, binary string) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("codex hooks: not a JSON object: %w", err)
		}
		if doc == nil {
			return nil, errors.New("codex hooks: not a JSON object")
		}
	}

	hooksObj, _ := doc["hooks"].(map[string]any)
	if hooksObj == nil {
		hooksObj = map[string]any{}
	}

	preCmd := codexHookCommand(binary, CodexPreToolMarker)
	postCmd := codexHookCommand(binary, CodexPostToolMarker)

	hooksObj["PreToolUse"] = upsertCodexMatcherHooks(
		stripManagedCodexHooks(asSlice(hooksObj["PreToolUse"])),
		[]string{codexBashMatcher, codexCommandMatcher}, preCmd,
	)
	hooksObj["PostToolUse"] = upsertCodexMatcherHooks(
		stripManagedCodexHooks(asSlice(hooksObj["PostToolUse"])),
		[]string{codexBashMatcher, codexCommandMatcher},
		postCmd,
	)

	doc["hooks"] = hooksObj
	return marshalSettings(doc)
}

func stripManagedCodexHooks(groups []any) []any {
	out := make([]any, 0, len(groups))
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			out = append(out, g)
			continue
		}
		entries := asSlice(gm["hooks"])
		if entries == nil {
			out = append(out, g)
			continue
		}
		kept := make([]any, 0, len(entries))
		for _, e := range entries {
			em, ok := e.(map[string]any)
			if !ok {
				kept = append(kept, e)
				continue
			}
			cmd, _ := em["command"].(string)
			if isManagedCodexCommand(cmd) {
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			continue
		}
		cp := copyMap(gm)
		cp["hooks"] = kept
		out = append(out, cp)
	}
	return out
}

func upsertCodexMatcherHooks(groups []any, matchers []string, command string) []any {
	out := append([]any{}, groups...)
	entry := map[string]any{
		"type":    "command",
		"command": command,
		"timeout": codexHookTimeout,
	}
	for _, matcher := range matchers {
		if codexMatcherHasCommand(out, matcher, command) {
			continue
		}
		if idx := codexMatcherIndex(out, matcher); idx >= 0 {
			gm := out[idx].(map[string]any)
			cp := copyMap(gm)
			cp["hooks"] = append(asSlice(gm["hooks"]), copyMap(entry))
			out[idx] = cp
			continue
		}
		out = append(out, map[string]any{
			"matcher": matcher,
			"hooks":   []any{copyMap(entry)},
		})
	}
	return out
}

func codexMatcherIndex(groups []any, matcher string) int {
	for i, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		m, _ := gm["matcher"].(string)
		if m == matcher {
			return i
		}
	}
	return -1
}

func codexMatcherHasCommand(groups []any, matcher, command string) bool {
	idx := codexMatcherIndex(groups, matcher)
	if idx < 0 {
		return false
	}
	gm, _ := groups[idx].(map[string]any)
	for _, e := range asSlice(gm["hooks"]) {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := em["command"].(string)
		if cmd == command {
			return true
		}
	}
	return false
}

func isManagedCodexCommand(command string) bool {
	return strings.Contains(command, CodexHookMarker) || strings.Contains(command, legacyCodexHookMarker)
}

func ensureCodexBackup(hooksPath string, existing []byte) error {
	bak := hooksPath + codexBackupSuffix
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

func plannedCodexRestore(hooksPath string, existing []byte) ([]byte, error) {
	bak := hooksPath + codexBackupSuffix
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
			for _, key := range []string{"PreToolUse", "PostToolUse", "Stop"} {
				groups := stripManagedCodexHooks(asSlice(hooksObj[key]))
				if len(groups) == 0 {
					delete(hooksObj, key)
				} else {
					hooksObj[key] = groups
				}
			}
			if len(hooksObj) == 0 {
				delete(doc, "hooks")
			} else {
				doc["hooks"] = hooksObj
			}
			if len(doc) == 0 {
				return nil, nil
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

func restoreCodexBackup(hooksPath string) error {
	bak := hooksPath + codexBackupSuffix
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stripCodexManagedInPlace(hooksPath)
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

func stripCodexManagedInPlace(hooksPath string) error {
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
	for _, key := range []string{"PreToolUse", "PostToolUse", "Stop"} {
		groups := stripManagedCodexHooks(asSlice(hooksObj[key]))
		if len(groups) == 0 {
			delete(hooksObj, key)
		} else {
			hooksObj[key] = groups
		}
	}
	if len(hooksObj) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooksObj
	}
	if len(doc) == 0 {
		return os.Remove(hooksPath)
	}
	out, err := marshalSettings(doc)
	if err != nil {
		return err
	}
	return writeFileAtomic(hooksPath, out)
}
