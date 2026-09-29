package util

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

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/compact"
	"github.com/JoshJancula/jevkit/internal/registry"
	"github.com/JoshJancula/jevkit/internal/security"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
	"github.com/JoshJancula/jevkit/internal/security/review"
)

// shellWrapperCmd is invoked only after a runtime's PreToolUse hook rewrites a
// shell call. Capturing here makes the compacted text the runtime's actual
// tool result; no post-tool output mutation is required.
func (a *App) shellWrapperCmd() *cobra.Command {
	var workspace, runtime, command, session string
	c := &cobra.Command{
		Use:    "shell-wrapper",
		Hidden: true,
		Short:  "private shell output wrapper",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(workspace) == "" || strings.TrimSpace(command) == "" {
				return app.Usagef("shell wrapper requires --workspace and --command")
			}
			return a.runShellWrapperSession(workspace, runtime, command, session)
		},
	}
	c.Flags().StringVar(&workspace, "workspace", "", "private runtime workspace")
	c.Flags().StringVar(&runtime, "runtime", "", "private runtime name")
	c.Flags().StringVar(&command, "command", "", "private original shell command")
	c.Flags().StringVar(&session, "session", "", "private runtime session key")
	return c
}

func (a *App) runShellWrapper(workspace, runtime, command string) error {
	return a.runShellWrapperSession(workspace, runtime, command, "")
}

func (a *App) runShellWrapperSession(workspace, runtime, command, session string) error {
	opts := a.SecurityLoadOptions()
	// The wrapper's explicit workspace is authoritative even when the
	// process's current directory differs from the invoking project.
	opts.WorkDir = workspace
	cfg, loadErr := securityconfig.LoadForHook(opts)
	if loadErr != nil {
		a.Errf("jevkit: security policy load: %v (builtin policy applied)\n", loadErr)
	}
	if decision := security.CheckLocal(cfg, security.Request{Command: command, Cwd: workspace, Workspace: workspace, Runtime: runtime}); decision.Deny {
		return app.Failf("security check denied command: %s", decision.Reason)
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
			return app.Failf("run shell command: %v", err)
		}
		exitStatus = exitErr.ExitCode()
	}

	raw := joinShellStreams(stdout.String(), stderr.String())
	if cfg != nil && cfg.Injection.Mode != "off" && raw != "" {
		if session == "" {
			session = review.SessionKey(runtime, "", "", workspace)
		}
		pointer, storeErr := a.storeShellResult(runtime, raw)
		if storeErr == nil {
			cfg.Asker = a.SecurityAsker(context.Background())
			reg, _ := registry.Load()
			decider := &registry.Decider{Registry: reg, StateDir: a.StateHome(), Getenv: a.Getenv}
			v := security.CheckInjection(context.Background(), cfg, security.InjectionRequest{Body: raw, Runtime: runtime, Tool: "shell", ToolInput: command, Workspace: workspace, SessionKey: session, RawPointer: pointer, SDLCRunID: a.Getenv("JEVKIT_SDLC_RUN_ID")}, decider)
			if v.Halt {
				rec, e := review.Create(a.StateHome(), review.Record{Runtime: runtime, SessionKey: session, Workspace: workspace, Tool: "shell", ToolInput: command, Score: v.Score, Confidence: v.Confidence, Reason: v.Reason, HeuristicHits: v.Hits, Excerpt: v.Excerpt, RawPointer: pointer, ContentSHA256: v.Hash, SDLCRunID: a.Getenv("JEVKIT_SDLC_RUN_ID")})
				if e == nil {
					_ = registry.AppendDecision(a.StateHome(), registry.Decision{Timestamp: rec.Created.Format("2006-01-02T15:04:05Z07:00"), Decision: "halt", QuestionSetID: security.InjectionQuestionSetID, Surface: "security", Runtime: runtime, Confidence: v.Confidence, Reason: v.Reason, ReviewID: rec.ID})
					a.Outf("jevkit: tool output held for prompt-injection review %s. Run: jevkit security review %s\n", rec.ID, rec.ID)
					if exitStatus != 0 {
						return &app.ExitError{Code: exitStatus}
					}
					return nil
				}
			}
		}
	}
	body := raw
	compacted := false
	if app.Truthy(a.Getenv("JEVKIT_COMPACT")) || app.Truthy(a.Getenv("JEVKIT_COMPACT_GENERIC")) {
		// The pointer exists before the classifier is allowed to rely on it.
		pointer, storeErr := a.storeShellResult(runtime, raw)
		if storeErr == nil {
			if app.Truthy(a.Getenv("JEVKIT_COMPACT")) {
				asker, policy := a.CompactionClient(context.Background())
				if asker != nil {
					jr, result := compact.JevCompact(command, raw, "", exitStatus, asker, compact.JevOptions{
						Enabled: true, Shadow: app.Truthy(a.Getenv("JEVKIT_COMPACT_SHADOW")),
						StateDir: a.StateHome(), RawPointer: pointer, AuthoritativeExit: true, Policy: policy, Runtime: runtime,
					})
					if result.Compacted && !app.Truthy(a.Getenv("JEVKIT_COMPACT_SHADOW")) {
						body, compacted = jr.Body, true
					}
				}
			}
			if !compacted && !app.Truthy(a.Getenv("JEVKIT_COMPACT_SHADOW")) {
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
		return &app.ExitError{Code: exitStatus}
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
	if app.Truthy(generic) {
		r := compact.Compact(command, output, "", exitStatus, compact.Options{})
		if r.Compacted {
			return r.Stdout, true
		}
	}
	return output, false
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
