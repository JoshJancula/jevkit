// Package keystore resolves the Typesafe API key from a fixed chain of
// sources, first hit wins, and reports which source won.
//
// Resolution order:
//
//  1. env      JEVKIT_API_KEY, then TYPESAFE_API_KEY
//  2. env-file <workspace>/.env, parsed (never sourced) for the key line only
//  3. command  a stored command whose stdout is the key
//  4. keychain the OS keychain (service "jevkit", account "TYPESAFE_API_KEY")
//  5. file     a 0600 plaintext file in the config dir (last resort)
//
// A configured backend that fails does not fall through to a later one, with
// one exception: an unavailable keychain is skipped silently. Backend
// selection lives in jev-credentials.json (0600) in the config dir. Keys are
// never written under the workspace.
package keystore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

// Source names the backend that supplied (or would supply) the key.
type Source string

// Sources, in resolution order.
const (
	SourceEnv      Source = "env"
	SourceEnvFile  Source = "env-file"
	SourceCommand  Source = "command"
	SourceKeychain Source = "keychain"
	SourceFile     Source = "file"
	SourceNone     Source = "none"
)

const (
	// KeychainService and KeychainAccount identify the keychain entry.
	KeychainService = "jevkit"
	KeychainAccount = "TYPESAFE_API_KEY"

	credentialsName = "jev-credentials.json"
	keyFileName     = "jev-api-key"
	schemaVersion   = 1

	defaultTimeout = 4 * time.Second
)

// Environment variable names that carry the key, in priority order.
var keyVars = []string{"JEVKIT_API_KEY", "TYPESAFE_API_KEY"}

// ErrNoKey means no source produced a key.
var ErrNoKey = errors.New("keystore: no API key configured")

// ErrKeyringNotFound is returned by a Keyring when the entry does not exist.
// Any other Keyring error means the keychain is unavailable and is skipped.
var ErrKeyringNotFound = errors.New("keystore: keychain entry not found")

// Keyring is the OS keychain surface the store needs.
type Keyring interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

// OSKeyring is the Keyring backed by zalando/go-keyring.
type OSKeyring struct{}

// Get implements Keyring.
func (OSKeyring) Get(service, account string) (string, error) {
	v, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrKeyringNotFound
	}
	return v, err
}

// Set implements Keyring.
func (OSKeyring) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}

// Delete implements Keyring.
func (OSKeyring) Delete(service, account string) error {
	err := keyring.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Store resolves and stores the API key. The zero value is not usable; use New.
type Store struct {
	// Getenv reads the environment; defaults to os.Getenv.
	Getenv func(string) string
	// Workspace is the workspace root whose .env is consulted.
	Workspace string
	// ConfigDir holds jev-credentials.json and the plaintext key file.
	ConfigDir string
	// Keyring is the keychain backend; defaults to OSKeyring.
	Keyring Keyring
	// Timeout bounds the stored command; JEVKIT_KEY_TIMEOUT_MS overrides the
	// 4s default when Timeout is zero.
	Timeout time.Duration
	// Warn receives the plaintext-storage warning; defaults to os.Stderr.
	Warn io.Writer
}

// New returns a Store for the given workspace root using the process
// environment, the default config dir and the OS keychain.
func New(workspace string) *Store {
	return &Store{
		Getenv:    os.Getenv,
		Workspace: workspace,
		ConfigDir: ConfigDir(os.Getenv),
		Keyring:   OSKeyring{},
		Warn:      os.Stderr,
	}
}

// ConfigDir returns $JEVKIT_CONFIG_HOME, else $XDG_CONFIG_HOME/jevkit, else
// the platform default user config dir plus "jevkit".
func ConfigDir(getenv func(string) string) string {
	if d := getenv("JEVKIT_CONFIG_HOME"); d != "" {
		return d
	}
	if d := getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "jevkit")
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "jevkit")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "jevkit")
	}
	return filepath.Join(os.TempDir(), "jevkit")
}

type credentials struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	Keychain      bool   `json:"keychain"`
	File          bool   `json:"file"`
}

func (s *Store) getenv(k string) string {
	if s.Getenv == nil {
		return os.Getenv(k)
	}
	return s.Getenv(k)
}

func (s *Store) keyring() Keyring {
	if s.Keyring == nil {
		return OSKeyring{}
	}
	return s.Keyring
}

func (s *Store) credentialsPath() string { return filepath.Join(s.ConfigDir, credentialsName) }
func (s *Store) keyFilePath() string     { return filepath.Join(s.ConfigDir, keyFileName) }

// EnvFilePath is the workspace dotenv path consulted by the env-file source.
func (s *Store) EnvFilePath() string { return filepath.Join(s.Workspace, ".env") }

// readCreds loads the backend selection. A missing or corrupt file yields the
// defaults, matching a store that was never configured.
func (s *Store) readCreds() credentials {
	c := credentials{SchemaVersion: schemaVersion}
	data, err := os.ReadFile(s.credentialsPath())
	if err != nil {
		return c
	}
	var raw credentials
	if json.Unmarshal(data, &raw) != nil {
		return c
	}
	c.Command, c.Keychain, c.File = raw.Command, raw.Keychain, raw.File
	return c
}

func (s *Store) writeCreds(c credentials) error {
	c.SchemaVersion = schemaVersion
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.writeSecure(s.credentialsPath(), data)
}

// writeSecure writes atomically with mode 0600, refusing to place secrets
// under the workspace tree.
func (s *Store) writeSecure(path string, data []byte) error {
	if s.underWorkspace() {
		return fmt.Errorf("keystore: refusing to store credentials under the workspace (%s); set JEVKIT_CONFIG_HOME", s.ConfigDir)
	}
	if err := os.MkdirAll(s.ConfigDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.ConfigDir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best effort; gone after rename
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close() //nolint:errcheck
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Store) underWorkspace() bool {
	if s.Workspace == "" {
		return false
	}
	ws, err1 := filepath.Abs(s.Workspace)
	cd, err2 := filepath.Abs(s.ConfigDir)
	if err1 != nil || err2 != nil {
		return false
	}
	ws, cd = resolveExisting(ws), resolveExisting(cd)
	rel, err := filepath.Rel(ws, cd)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExisting evaluates symlinks on the deepest existing ancestor of p, so
// a not-yet-created path compares correctly against a symlinked workspace.
func resolveExisting(p string) string {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// Resolve returns the key and the source that supplied it. When a configured
// backend fails, the source is still reported alongside the error so callers
// can say which backend to fix; with nothing configured the error is ErrNoKey.
func (s *Store) Resolve(ctx context.Context) (string, Source, error) {
	return s.resolve(ctx, true)
}

// Source reports which backend would supply the key without running the
// stored command. The key is never returned.
func (s *Store) Source(ctx context.Context) Source {
	_, src, _ := s.resolve(ctx, false)
	return src
}

func (s *Store) resolve(ctx context.Context, run bool) (string, Source, error) {
	for _, name := range keyVars {
		if v := s.getenv(name); v != "" {
			return v, SourceEnv, nil
		}
	}

	if s.getenv("JEVKIT_ENV_FILE") != "0" {
		if v, ok := parseEnvFile(s.EnvFilePath()); ok {
			return v, SourceEnvFile, nil
		}
	}

	c := s.readCreds()

	if c.Command != "" {
		if !run {
			return "", SourceCommand, nil
		}
		key, err := s.runCommand(ctx, c.Command)
		if err != nil {
			return "", SourceCommand, err
		}
		return key, SourceCommand, nil
	}

	if c.Keychain {
		key, err := s.keyring().Get(KeychainService, KeychainAccount)
		switch {
		case err == nil && key != "":
			return key, SourceKeychain, nil
		case errors.Is(err, ErrKeyringNotFound) || (err == nil && key == ""):
			return "", SourceKeychain, errors.New("keystore: keychain entry is missing or empty")
		}
		// Any other error: keychain unavailable, skip silently.
	}

	if c.File {
		data, err := os.ReadFile(s.keyFilePath())
		if err != nil {
			return "", SourceFile, fmt.Errorf("keystore: read key file: %w", err)
		}
		key := strings.TrimSuffix(string(data), "\n")
		if key == "" {
			return "", SourceFile, errors.New("keystore: key file is empty")
		}
		return key, SourceFile, nil
	}

	return "", SourceNone, ErrNoKey
}

func (s *Store) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	if ms, err := strconv.Atoi(s.getenv("JEVKIT_KEY_TIMEOUT_MS")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultTimeout
}

// runCommand runs the stored command under a context deadline. Its stdout,
// minus one trailing newline, is the key. Stderr is discarded so the command
// cannot leak into our logs, and errors never include its output.
func (s *Store) runCommand(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	// Keep Wait from blocking on grandchildren that inherited the pipe.
	cmd.WaitDelay = 500 * time.Millisecond

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("keystore: credential command timed out after %s", s.timeout())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("keystore: credential command exited with status %d", ee.ExitCode())
		}
		return "", errors.New("keystore: credential command failed to run")
	}
	key := strings.TrimSuffix(out.String(), "\n")
	if key == "" {
		return "", errors.New("keystore: credential command produced no output")
	}
	return key, nil
}

// parseEnvFile extracts the API key from a dotenv file. The file is parsed,
// never executed: only the exact key names are read, an optional "export "
// prefix is allowed, one pair of matching quotes is stripped, and the value
// is otherwise literal (no expansion).
func parseEnvFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	found := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		name, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if _, dup := found[name]; dup {
			continue
		}
		for _, k := range keyVars {
			if name == k {
				found[name] = unquote(strings.TrimSpace(val))
			}
		}
	}
	for _, k := range keyVars {
		if v := found[k]; v != "" {
			return v, true
		}
	}
	return "", false
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// ReadKey reads a key from r (typically stdin), dropping one trailing newline.
// Keys are taken from a reader so they never appear in argv or shell history.
func ReadKey(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	key := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if key == "" {
		return "", errors.New("keystore: empty key")
	}
	return key, nil
}

// SetCommand stores a command whose stdout is the key. No secret is stored.
func (s *Store) SetCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("keystore: empty command")
	}
	c := s.readCreds()
	c.Command = command
	return s.writeCreds(c)
}

// SetKeychain stores key in the OS keychain and selects that backend.
func (s *Store) SetKeychain(key string) error {
	if key == "" {
		return errors.New("keystore: empty key")
	}
	if s.underWorkspace() {
		return fmt.Errorf("keystore: refusing to store credentials under the workspace (%s)", s.ConfigDir)
	}
	if err := s.keyring().Set(KeychainService, KeychainAccount, key); err != nil {
		return errors.New("keystore: keychain unavailable or refused the key")
	}
	c := s.readCreds()
	c.Keychain = true
	return s.writeCreds(c)
}

// SetFile stores key in a 0600 plaintext file and selects that backend,
// warning that the key is stored in plaintext.
func (s *Store) SetFile(key string) error {
	if key == "" {
		return errors.New("keystore: empty key")
	}
	if err := s.writeSecure(s.keyFilePath(), []byte(key)); err != nil {
		return err
	}
	w := s.Warn
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintf(w, "warning: stored a plaintext API key at %s (mode 600). Prefer the keychain or a credential command when available.\n", s.keyFilePath())
	c := s.readCreds()
	c.File = true
	return s.writeCreds(c)
}

// Clear removes every stored backend: keychain entry, key file and selection.
func (s *Store) Clear() error {
	_ = s.keyring().Delete(KeychainService, KeychainAccount) // unavailable keychain is fine
	if err := os.Remove(s.keyFilePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(s.credentialsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Status describes where the key comes from. It never includes the key.
func (s *Store) Status(ctx context.Context) string {
	src := s.Source(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "source: %s\n", src)
	switch src {
	case SourceEnv:
		for _, k := range keyVars {
			if s.getenv(k) != "" {
				fmt.Fprintf(&b, "location: %s (environment)\n", k)
				break
			}
		}
	case SourceEnvFile:
		fmt.Fprintf(&b, "location: %s\n", s.EnvFilePath())
	case SourceCommand:
		fmt.Fprintf(&b, "command: %s\n", s.readCreds().Command)
	case SourceKeychain:
		fmt.Fprintf(&b, "location: service=%s account=%s\n", KeychainService, KeychainAccount)
	case SourceFile:
		fmt.Fprintf(&b, "location: %s\n", s.keyFilePath())
	case SourceNone:
	}
	return b.String()
}
