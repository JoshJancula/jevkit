package agents

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed opencodeplugin/jevkit-runtime-hooks.ts
var opencodePluginSource string

const opencodeBackupSuffix = ".jevkit-original"

// Install stages the OpenCode TypeScript plugin under
// .opencode/plugins/jevkit-runtime-hooks.ts (project) or
// <ConfigDir>/.opencode/plugins/jevkit-runtime-hooks.ts (user).
// It is idempotent: a second apply leaves a single managed file. DryRun
// computes the rendered plugin without writing.
func (o *OpenCode) Install(opts InstallOptions) error {
	path, err := opencodePluginPath(opts)
	if err != nil {
		return err
	}
	binary := opts.Binary
	if binary == "" {
		binary = o.binary()
	}
	out := renderOpenCodePlugin(binary)

	existing, err := readFileOptional(path)
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
	if err := ensureOpenCodeBackup(path, existing); err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

// Uninstall removes the staged OpenCode plugin and restores any pre-install
// file captured on first install (or deletes the file when it was absent).
func (o *OpenCode) Uninstall(opts InstallOptions) error {
	path, err := opencodePluginPath(opts)
	if err != nil {
		return err
	}
	existing, err := readFileOptional(path)
	if err != nil {
		return err
	}
	after, err := plannedOpenCodeRestore(path, existing)
	if err != nil {
		return err
	}
	reportPreview(opts, path, existing, after)
	if opts.DryRun {
		return nil
	}
	return restoreOpenCodeBackup(path)
}

func opencodePluginPath(opts InstallOptions) (string, error) {
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
			return "", errors.New("opencode install: project scope requires WorkDir")
		}
		return filepath.Join(opts.WorkDir, ".opencode", "plugins", OpenCodePluginFile), nil
	case "user":
		if opts.ConfigDir == "" {
			return "", errors.New("opencode install: user scope requires ConfigDir (home)")
		}
		return filepath.Join(opts.ConfigDir, ".opencode", "plugins", OpenCodePluginFile), nil
	default:
		return "", fmt.Errorf("opencode install: unknown scope %q", opts.Scope)
	}
}

// renderOpenCodePlugin injects the install-time binary into the embedded
// TypeScript source. The /*JEVKIT_BINARY*/ delimiters are the replace anchors.
func renderOpenCodePlugin(binary string) []byte {
	if strings.TrimSpace(binary) == "" {
		binary = "jevkit"
	}
	const open = "/*JEVKIT_BINARY*/"
	const close = "/*JEVKIT_BINARY*/"
	start := strings.Index(opencodePluginSource, open)
	if start < 0 {
		return []byte(opencodePluginSource)
	}
	start += len(open)
	end := strings.Index(opencodePluginSource[start:], close)
	if end < 0 {
		return []byte(opencodePluginSource)
	}
	end = start + end
	out := opencodePluginSource[:start] + strconv.Quote(binary) + opencodePluginSource[end:]
	return []byte(out)
}

func ensureOpenCodeBackup(pluginPath string, existing []byte) error {
	bak := pluginPath + opencodeBackupSuffix
	if _, err := os.Stat(bak); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var body []byte
	if len(existing) == 0 {
		if _, err := os.Stat(pluginPath); errors.Is(err, os.ErrNotExist) {
			body = []byte(absentSentinel)
		} else if err != nil {
			return err
		} else {
			body = existing
		}
	} else {
		body = existing
	}
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(bak, body)
}

func plannedOpenCodeRestore(pluginPath string, existing []byte) ([]byte, error) {
	bak := pluginPath + opencodeBackupSuffix
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if len(existing) == 0 {
				return nil, nil
			}
			if strings.Contains(string(existing), OpenCodePluginMarker) {
				return nil, nil
			}
			return existing, nil
		}
		return nil, err
	}
	if string(data) == absentSentinel {
		return nil, nil
	}
	return data, nil
}

func restoreOpenCodeBackup(pluginPath string) error {
	bak := pluginPath + opencodeBackupSuffix
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stripOpenCodeManagedInPlace(pluginPath)
		}
		return err
	}
	if string(data) == absentSentinel {
		_ = os.Remove(pluginPath)
		return os.Remove(bak)
	}
	if err := writeFileAtomic(pluginPath, data); err != nil {
		return err
	}
	return os.Remove(bak)
}

func stripOpenCodeManagedInPlace(pluginPath string) error {
	existing, err := readFileOptional(pluginPath)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	if !strings.Contains(string(existing), OpenCodePluginMarker) {
		return nil
	}
	return os.Remove(pluginPath)
}
