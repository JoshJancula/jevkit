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

const antigravityBackupSuffix = ".jevkit-original"

// Install merges Antigravity hooks into .agents/hooks.json (project) or
// <ConfigDir>/.agents/hooks.json (user). It is idempotent: a second apply
// leaves a single managed group. DryRun computes the merge without writing.
func (a *Antigravity) Install(opts InstallOptions) error {
	path, err := antigravityHooksPath(opts)
	if err != nil {
		return err
	}
	binary := opts.Binary
	if binary == "" {
		binary = a.binary()
	}

	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	out, err := mergeAntigravityHooks(existing, binary)
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
	if err := ensureAntigravityBackup(path, existing); err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

// Uninstall removes jevkit-managed Antigravity hooks and restores the hooks
// file to the byte-exact original captured on first install.
func (a *Antigravity) Uninstall(opts InstallOptions) error {
	path, err := antigravityHooksPath(opts)
	if err != nil {
		return err
	}
	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	after, err := plannedAntigravityRestore(path, existing)
	if err != nil {
		return err
	}
	reportPreview(opts, path, existing, after)
	if opts.DryRun {
		return nil
	}
	return restoreAntigravityBackup(path)
}

func antigravityHooksPath(opts InstallOptions) (string, error) {
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
			return "", errors.New("antigravity install: project scope requires WorkDir")
		}
		return filepath.Join(opts.WorkDir, ".agents", "hooks.json"), nil
	case "user":
		if opts.ConfigDir == "" {
			return "", errors.New("antigravity install: user scope requires ConfigDir (home)")
		}
		return filepath.Join(opts.ConfigDir, ".agents", "hooks.json"), nil
	default:
		return "", fmt.Errorf("antigravity install: unknown scope %q", opts.Scope)
	}
}

func antigravityHookCommand(binary string) string {
	return binary + " " + AntigravityPostToolMarker
}

func mergeAntigravityHooks(existing []byte, binary string) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("antigravity hooks: not a JSON object: %w", err)
		}
		if doc == nil {
			return nil, errors.New("antigravity hooks: not a JSON object")
		}
	}

	// Drop prior managed group / stray managed commands, then upsert.
	delete(doc, AntigravityHooksGroup)
	stripManagedAntigravityFromDoc(doc)

	cmd := antigravityHookCommand(binary)
	doc[AntigravityHooksGroup] = map[string]any{
		"PostToolUse": []any{
			map[string]any{
				"matcher": antigravityRunCommandMatcher,
				"hooks": []any{
					map[string]any{
						"command": cmd,
						"timeout": antigravityPreToolTimeout,
					},
				},
			},
		},
	}
	return marshalSettings(doc)
}

func stripManagedAntigravityFromDoc(doc map[string]any) {
	for key, val := range doc {
		group, ok := val.(map[string]any)
		if !ok {
			continue
		}
		changed := false
		for event, ev := range group {
			entries := asSlice(ev)
			stripped := stripManagedAntigravityHookGroups(entries)
			if len(stripped) == 0 {
				delete(group, event)
				changed = true
				continue
			}
			if len(stripped) != len(entries) {
				group[event] = stripped
				changed = true
			}
		}
		if changed && len(group) == 0 {
			delete(doc, key)
		}
	}
}

func stripManagedAntigravityHookGroups(groups []any) []any {
	out := make([]any, 0, len(groups))
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			out = append(out, g)
			continue
		}
		// Flat entry with command (Stop-style) or nested hooks.
		if cmd, _ := gm["command"].(string); cmd != "" {
			if isManagedAntigravityCommand(cmd) {
				continue
			}
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
			if isManagedAntigravityCommand(cmd) {
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

func isManagedAntigravityCommand(command string) bool {
	return strings.Contains(command, AntigravityHookMarker) || strings.Contains(command, legacyAntigravityHookMarker)
}

func ensureAntigravityBackup(hooksPath string, existing []byte) error {
	bak := hooksPath + antigravityBackupSuffix
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

func plannedAntigravityRestore(hooksPath string, existing []byte) ([]byte, error) {
	bak := hooksPath + antigravityBackupSuffix
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
			delete(doc, AntigravityHooksGroup)
			stripManagedAntigravityFromDoc(doc)
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

func restoreAntigravityBackup(hooksPath string) error {
	bak := hooksPath + antigravityBackupSuffix
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stripAntigravityManagedInPlace(hooksPath)
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

func stripAntigravityManagedInPlace(hooksPath string) error {
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
	delete(doc, AntigravityHooksGroup)
	stripManagedAntigravityFromDoc(doc)
	if len(doc) == 0 {
		return os.Remove(hooksPath)
	}
	out, err := marshalSettings(doc)
	if err != nil {
		return err
	}
	return writeFileAtomic(hooksPath, out)
}
