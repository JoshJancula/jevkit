package security

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/internal/security/config"
)

const (
	DefaultCheckTimeout   = 5 * time.Minute
	DefaultMaxOutputBytes = 256 * 1024
)

// ArgvRequest runs one planner-proposed check command. Argv is executed
// without a shell unless UseShell is explicitly set. Stdin is closed, the
// environment is limited, output is bounded, and the process group is cleaned
// up on exit or cancellation.
type ArgvRequest struct {
	Argv           []string
	WorkingDir     string // relative to Workspace unless absolute and allowed
	Timeout        time.Duration
	Workspace      string
	AllowRead      []string
	MaxOutputBytes int
	// UseShell is an explicit opt-in for shell syntax. ExtraEnv and InheritEnv
	// are explicit opt-ins for additional environment privileges.
	UseShell   bool
	ExtraEnv   []string
	InheritEnv bool
	// AuthorizedDigest must equal ChecksDigest; --auto alone is not permission.
	ChecksDigest     string
	AuthorizedDigest string
	Security         *config.Config
}

// ArgvResult is the bounded outcome of a planner-proposed command.
type ArgvResult struct {
	ExitCode    int
	TimedOut    bool
	Stdout      []byte
	Stderr      []byte
	Truncated   bool
	Duration    time.Duration
	CommandLine string
}

// RequireCommandAuthorization refuses execution when the operator has not
// authorized this exact checks.json digest for the run.
func RequireCommandAuthorization(checksDigest, authorizedDigest string) error {
	checksDigest = strings.TrimSpace(checksDigest)
	authorizedDigest = strings.TrimSpace(authorizedDigest)
	if checksDigest == "" || authorizedDigest == "" || checksDigest != authorizedDigest {
		return fmt.Errorf("security: planner-proposed commands are not authorized for this run")
	}
	return nil
}

// RunArgv executes a planner-proposed command only after authorization and
// local security checks. It never invents a command or uses a shell by default.
func RunArgv(ctx context.Context, req ArgvRequest) (ArgvResult, error) {
	if err := RequireCommandAuthorization(req.ChecksDigest, req.AuthorizedDigest); err != nil {
		return ArgvResult{}, err
	}
	if len(req.Argv) == 0 || strings.TrimSpace(req.Argv[0]) == "" {
		return ArgvResult{}, fmt.Errorf("security: empty argv")
	}
	for _, arg := range req.Argv {
		if strings.TrimSpace(arg) == "" {
			return ArgvResult{}, fmt.Errorf("security: argv contains an empty entry")
		}
	}
	if req.Workspace == "" {
		return ArgvResult{}, fmt.Errorf("security: workspace is required")
	}
	workspace, err := filepath.Abs(req.Workspace)
	if err != nil {
		return ArgvResult{}, fmt.Errorf("security: resolve workspace: %w", err)
	}
	dir := workspace
	if wd := strings.TrimSpace(req.WorkingDir); wd != "" {
		if filepath.IsAbs(wd) || strings.Contains(wd, "..") {
			return ArgvResult{}, fmt.Errorf("security: workingDir must be a relative path without ..")
		}
		dir = filepath.Join(workspace, filepath.Clean(wd))
	}
	commandLine := strings.Join(req.Argv, " ")
	if req.UseShell {
		commandLine = req.Argv[0]
		if len(req.Argv) > 1 {
			commandLine = strings.Join(req.Argv, " ")
		}
	}
	cfg := req.Security
	if cfg == nil {
		cfg, err = config.Builtin(config.LoadOptions{WorkDir: workspace})
		if err != nil {
			return ArgvResult{}, err
		}
		cfg.AllowRead = append([]string(nil), req.AllowRead...)
	}
	decision := CheckLocal(cfg, Request{Command: commandLine, Cwd: dir, Workspace: workspace})
	if decision.Deny {
		return ArgvResult{}, fmt.Errorf("security: denied: %s", decision.Reason)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	maxOut := req.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxOutputBytes
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if req.UseShell {
		shell := "/bin/sh"
		if _, err := os.Stat(shell); err != nil {
			shell = "sh"
		}
		cmd = exec.CommandContext(runCtx, shell, "-c", commandLine)
	} else {
		cmd = exec.CommandContext(runCtx, req.Argv[0], req.Argv[1:]...)
	}
	prepareArgvCommand(cmd)
	cmd.Dir = dir
	cmd.Env = limitedEnv(req.InheritEnv, req.ExtraEnv)
	cmd.Stdin = nil // closed stdin: no interactive or piped input
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxOut, maxOut
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	started := time.Now()
	err = runArgvCommand(cmd)
	result := ArgvResult{
		CommandLine: commandLine,
		Stdout:      stdout.Bytes(),
		Stderr:      stderr.Bytes(),
		Truncated:   stdout.truncated || stderr.truncated,
		Duration:    time.Since(started),
	}
	if runCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, fmt.Errorf("security: command timed out after %s", timeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, err
	}
	return result, nil
}

func limitedEnv(inherit bool, extra []string) []string {
	var env []string
	if inherit {
		env = append(env, os.Environ()...)
	} else {
		keep := []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TEMP", "TMP", "TERM", "TZ"}
		for _, key := range keep {
			if v, ok := os.LookupEnv(key); ok {
				env = append(env, key+"="+v)
			}
		}
	}
	env = append(env, extra...)
	return env
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte {
	return append([]byte(nil), b.buf.Bytes()...)
}

// Discard unused io import guard — limitedBuffer is an io.Writer.
var _ io.Writer = (*limitedBuffer)(nil)
