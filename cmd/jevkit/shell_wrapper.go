package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/compact"
	"github.com/OWNER/jevkit/internal/security"
	securityconfig "github.com/OWNER/jevkit/internal/security/config"
)

// shellWrapperCmd is invoked only after a runtime's PreToolUse hook rewrites a
// shell call. Capturing here makes the compacted text the runtime's actual
// tool result; no post-tool output mutation is required.
func (a *App) shellWrapperCmd() *cobra.Command {
	var workspace, runtime, command string
	c := &cobra.Command{
		Use:    "shell-wrapper",
		Hidden: true,
		Short:  "private shell output wrapper",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(workspace) == "" || strings.TrimSpace(command) == "" {
				return usagef("shell wrapper requires --workspace and --command")
			}
			return a.runShellWrapper(workspace, runtime, command)
		},
	}
	c.Flags().StringVar(&workspace, "workspace", "", "private runtime workspace")
	c.Flags().StringVar(&runtime, "runtime", "", "private runtime name")
	c.Flags().StringVar(&command, "command", "", "private original shell command")
	return c
}

func (a *App) runShellWrapper(workspace, runtime, command string) error {
	opts := a.securityLoadOptions()
	// The wrapper's explicit workspace is authoritative even when the
	// process's current directory differs from the invoking project.
	opts.WorkDir = workspace
	cfg, loadErr := securityconfig.LoadForHook(opts)
	if loadErr != nil {
		a.errf("jevkit: security policy load: %v (builtin policy applied)\n", loadErr)
	}
	if decision := security.CheckLocal(cfg, security.Request{Command: command, Cwd: workspace, Workspace: workspace, Runtime: runtime}); decision.Deny {
		return failf("security check denied command: %s", decision.Reason)
	}
	// bash matches the shell contract used by the supported coding runtimes.
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = workspace
	cmd.Stdin = a.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exitStatus := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return failf("run shell command: %v", err)
		}
		exitStatus = exitErr.ExitCode()
	}

	raw := joinShellStreams(stdout.String(), stderr.String())
	body := raw
	compacted := false
	if truthy(a.getenv("JEVKIT_COMPACT")) || truthy(a.getenv("JEVKIT_COMPACT_GENERIC")) {
		// The pointer exists before the classifier is allowed to rely on it.
		pointer, storeErr := a.storeShellResult(runtime, raw)
		if storeErr == nil {
			if truthy(a.getenv("JEVKIT_COMPACT")) {
				asker, policy := a.compactionClient(context.Background())
				if asker != nil {
					jr, result := compact.JevCompact(command, raw, "", exitStatus, asker, compact.JevOptions{
						Enabled: true, Shadow: truthy(a.getenv("JEVKIT_COMPACT_SHADOW")),
						StateDir: a.stateHome(), RawPointer: pointer, AuthoritativeExit: true, Policy: policy, Runtime: runtime,
					})
					if result.Compacted && !truthy(a.getenv("JEVKIT_COMPACT_SHADOW")) {
						body, compacted = jr.Body, true
					}
				}
			}
			if !compacted && !truthy(a.getenv("JEVKIT_COMPACT_SHADOW")) {
				body, compacted = compactShellResult(command, raw, exitStatus, "1")
			}
			if compacted {
				body = strings.TrimRight(body, "\n") + "\n[jevkit: shell output compacted; original: " + pointer + "]\n"
			}
		}
	}
	if body == "" {
		body = raw
	}
	_, _ = fmt.Fprint(a.Stdout, body)
	if exitStatus != 0 {
		return &exitError{code: exitStatus}
	}
	return nil
}

func joinShellStreams(stdout, stderr string) string {
	if stdout != "" && stderr != "" {
		return strings.TrimRight(stdout, "\n") + "\n" + stderr
	}
	return stdout + stderr
}

func compactShellResult(command, output string, exitStatus int, generic string) (string, bool) {
	if truthy(generic) {
		r := compact.Compact(command, output, "", exitStatus, compact.Options{})
		if r.Compacted {
			return r.Stdout, true
		}
	}
	return output, false
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (a *App) storeShellResult(runtime, body string) (string, error) {
	if strings.TrimSpace(a.StateDir) == "" {
		return "", errors.New("state directory unavailable")
	}
	if runtime == "" {
		runtime = "shell"
	}
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	dir := filepath.Join(a.StateDir, "tool-results", runtime)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hex.EncodeToString(idBytes)+".log")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
