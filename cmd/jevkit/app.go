package main

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

	"github.com/OWNER/jevkit/internal/breaker"
	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/keystore"
	"github.com/OWNER/jevkit/internal/redact/audit"
	"github.com/OWNER/jevkit/internal/redact/config"
)

// Exit codes.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
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
}

func (a *App) outf(format string, args ...any) { _, _ = fmt.Fprintf(a.Stdout, format, args...) }
func (a *App) errf(format string, args ...any) { _, _ = fmt.Fprintf(a.Stderr, format, args...) }

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) auditPath() string  { return filepath.Join(a.StateDir, audit.FileName) }
func (a *App) reviewPath() string { return filepath.Join(a.StateDir, audit.ReviewFileName) }

func (a *App) userPath() string {
	if a.ConfigDir == "" {
		return ""
	}
	return filepath.Join(a.ConfigDir, "redact.yaml")
}

func (a *App) projectPath() string { return filepath.Join(a.WorkDir, ".jevkit", "redact.yaml") }

func (a *App) loadOptions() config.LoadOptions {
	o := config.LoadOptions{UserPath: a.userPath(), ProjectPath: a.projectPath(), Environ: a.Environ}
	if a.ConfigDir != "" {
		o.SaltPath = filepath.Join(a.ConfigDir, "redact.salt")
	}
	return o
}

// layerName labels a config file as the user or project layer.
func (a *App) layerName(file string) string {
	switch file {
	case a.userPath():
		return "user"
	case a.projectPath():
		return "project"
	}
	return file
}

// target returns the layer file a write command edits.
func (a *App) target(project bool) (string, error) {
	if project {
		return a.projectPath(), nil
	}
	if a.userPath() == "" {
		return "", errors.New("no user config directory; set JEVKIT_CONFIG_DIR or use --project")
	}
	return a.userPath(), nil
}

// newFlagSet makes a FlagSet that reports to stderr and never exits.
func (a *App) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	return fs
}

// parseFlags parses flags interleaved with positional arguments. done is true
// when the caller should return code immediately (help or a flag error).
func parseFlags(fs *flag.FlagSet, args []string) (pos []string, code int, done bool) {
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, exitOK, true
			}
			return nil, exitUsage, true
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, exitOK, false
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// writeAtomic writes data to path through a same-directory temp file and a
// rename, so a reader never sees a partial file.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := stage(path, data, perm)
	if err != nil {
		return err
	}
	return commit(tmp, path)
}

// stage writes data to a fresh temp file beside path and returns its name.
func stage(path string, data []byte, perm os.FileMode) (string, error) {
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

func commit(tmp, path string) error {
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
