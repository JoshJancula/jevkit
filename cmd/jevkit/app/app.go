package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/JoshJancula/jevkit/internal/breaker"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/keystore"
	"github.com/JoshJancula/jevkit/internal/redact/audit"
	"github.com/JoshJancula/jevkit/internal/redact/config"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitFail  = 1
	ExitUsage = 2
)

// App is the CLI with every external dependency injected, so commands run
// in-process under test.
type App struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Environ is the process environment (KEY=value).
	Environ []string
	// WorkDir holds the project layer at .jevkit/redact.yaml.
	WorkDir string
	// ConfigDir holds the user layer at redact.yaml.
	ConfigDir string
	// StateDir holds the audit log and the review store.
	StateDir string
	// SecurityPolicy selects a named local security policy for this invocation.
	SecurityPolicy string
	// Yolo lifts the workspace guard; killswitch and Jev scoring still apply.
	Yolo bool
	// Now is the clock; nil means time.Now.
	Now     func() time.Time
	Version string

	// Keystore resolves and stores the API key; nil builds one from
	// ConfigDir, WorkDir and Environ.
	Keystore *keystore.Store
	// Keyring is the OS keychain backend; nil means the real one.
	Keyring keystore.Keyring
	// NewJev builds the client `key test` uses; nil builds the real one.
	NewJev func(cfg jev.Config, key func() (string, error)) Asker
	// Breaker is the persisted circuit breaker; nil uses StateDir.
	Breaker *breaker.Breaker
	// Dial probes endpoint reachability for doctor; nil skips the probe.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// LookPath finds agent binaries for doctor/install; nil means not detected
	// (tests) or exec.LookPath at the call site when doctor needs real PATH.
	LookPath func(name string) (string, error)
	// HomeDir is the user home for agent user-scope installs; empty uses
	// os.UserHomeDir.
	HomeDir string
	// Binary is the jevkit path written into agent hooks; empty resolves to
	// the running executable or "jevkit".
	Binary string
	// ReadSecret reads a key with echo off. ok is false when stdin is not a
	// terminal; nil means the real terminal check on os.Stdin.
	ReadSecret func() (secret []byte, ok bool, err error)
	// NewRunID generates an sdlc run id; nil means a random one.
	NewRunID func() string
	// SdlcReach supplies fake runtime adapters in tests; nil probes CLI binaries.
	SdlcReach func() enrollment.Reach
	// SdlcExecutor replaces CLI invocation in tests and host integrations.
	SdlcExecutor worker.Executor
	// WorktreeCreator creates isolated Git worktrees for fan-out subtasks.
	// Nil uses worker.GitWorktreeCreator.
	WorktreeCreator worker.WorktreeCreator
	// SdlcSpecialistNeed replaces the specialist-need router in tests or hosts.
	// Nil uses Jev's registered specialist question set.
	SdlcSpecialistNeed func(context.Context, string, string, string) (bool, error)
	// Confirm prompts prompt and reports whether the user agreed; nil means
	// non-interactive (no TTY to confirm on), so a caller offering a
	// "gather"-confidence pick for confirmation must instead refuse.
	Confirm func(prompt string) (bool, error)
	// SuppressOutput silences Outf while a dashboard owns the terminal.
	SuppressOutput bool
}

func (a *App) Outf(format string, args ...any) {
	if a.SuppressOutput {
		return
	}
	_, _ = fmt.Fprintf(a.Stdout, format, args...)
}
func (a *App) Errf(format string, args ...any) { _, _ = fmt.Fprintf(a.Stderr, format, args...) }

func (a *App) Clock() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// auditPath and reviewPath resolve through stateHome()+"jevkit" (the same
// resolver breaker.New, usage.Path, and registry.DecisionsPath use) rather
// than a.StateDir directly, so a JEVKIT_STATE_DIR override that doesn't
// itself end in "jevkit" still lands these files next to every other
// jevkit-owned state file instead of one directory up.
func (a *App) AuditPath() string { return filepath.Join(a.StateHome(), "jevkit", audit.FileName) }
func (a *App) ReviewPath() string {
	return filepath.Join(a.StateHome(), "jevkit", audit.ReviewFileName)
}

func (a *App) UserPath() string {
	if a.ConfigDir == "" {
		return ""
	}
	return filepath.Join(a.ConfigDir, "redact.yaml")
}

func (a *App) ProjectPath() string { return filepath.Join(a.WorkDir, ".jevkit", "redact.yaml") }

func (a *App) LoadOptions() config.LoadOptions {
	o := config.LoadOptions{UserPath: a.UserPath(), ProjectPath: a.ProjectPath(), Environ: a.Environ}
	if a.ConfigDir != "" {
		o.SaltPath = filepath.Join(a.ConfigDir, "redact.salt")
	}
	return o
}

// layerName labels a config file as the user or project layer.
func (a *App) LayerName(file string) string {
	switch file {
	case a.UserPath():
		return "user"
	case a.ProjectPath():
		return "project"
	}
	return file
}

// target returns the layer file a write command edits.
func (a *App) Target(project bool) (string, error) {
	if project {
		return a.ProjectPath(), nil
	}
	if a.UserPath() == "" {
		return "", errors.New("no user config directory; set JEVKIT_CONFIG_DIR or use --project")
	}
	return a.UserPath(), nil
}

// newFlagSet makes a FlagSet that reports to stderr and never exits.
func (a *App) NewFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	return fs
}

// parseFlags parses flags interleaved with positional arguments. done is true
// when the caller should return code immediately (help or a flag error).
func ParseFlags(fs *flag.FlagSet, args []string) (pos []string, code int, done bool) {
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, ExitOK, true
			}
			return nil, ExitUsage, true
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, ExitOK, false
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// writeAtomic writes data to path through a same-directory temp file and a
// rename, so a reader never sees a partial file.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := Stage(path, data, perm)
	if err != nil {
		return err
	}
	return Commit(tmp, path)
}

// stage writes data to a fresh temp file beside path and returns its name.
func Stage(path string, data []byte, perm os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".redact-*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func Commit(tmp, path string) error {
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
