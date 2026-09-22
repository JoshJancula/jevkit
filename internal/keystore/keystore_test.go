package keystore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const secret = "sk-test-SECRET-value-123"

type fakeKeyring struct {
	m        map[string]string
	down     bool // simulate an unavailable keychain
	sawArgs  []string
	getCalls int
}

func (f *fakeKeyring) k(s, a string) string { return s + "/" + a }

func (f *fakeKeyring) Get(s, a string) (string, error) {
	f.getCalls++
	if f.down {
		return "", errors.New("no dbus")
	}
	v, ok := f.m[f.k(s, a)]
	if !ok {
		return "", ErrKeyringNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Set(s, a, v string) error {
	if f.down {
		return errors.New("no dbus")
	}
	if f.m == nil {
		f.m = map[string]string{}
	}
	f.m[f.k(s, a)] = v
	f.sawArgs = append(f.sawArgs, s, a)
	return nil
}

func (f *fakeKeyring) Delete(s, a string) error {
	delete(f.m, f.k(s, a))
	return nil
}

type env map[string]string

func (e env) get(k string) string { return e[k] }

func newStore(t *testing.T, e env) (*Store, *fakeKeyring, *bytes.Buffer) {
	t.Helper()
	if e == nil {
		e = env{}
	}
	kr := &fakeKeyring{}
	warn := &bytes.Buffer{}
	return &Store{
		Getenv:    e.get,
		Workspace: t.TempDir(),
		ConfigDir: filepath.Join(t.TempDir(), "cfg"),
		Keyring:   kr,
		Timeout:   2 * time.Second,
		Warn:      warn,
	}, kr, warn
}

func writeEnvFile(t *testing.T, s *Store, content string) {
	t.Helper()
	if err := os.WriteFile(s.EnvFilePath(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resolve(t *testing.T, s *Store) (string, Source, error) {
	t.Helper()
	return s.Resolve(context.Background())
}

func wantKey(t *testing.T, s *Store, key string, src Source) {
	t.Helper()
	got, gotSrc, err := resolve(t, s)
	if err != nil || got != key || gotSrc != src {
		t.Fatalf("Resolve = (%q, %s, %v); want (%q, %s, nil)", got, gotSrc, err, key, src)
	}
	if s2 := s.Source(context.Background()); s2 != src {
		t.Fatalf("Source = %s; want %s", s2, src)
	}
}

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix shell / file modes")
	}
}

func TestBackendsInIsolation(t *testing.T) {
	skipWindows(t)
	t.Run("env JEVKIT_API_KEY", func(t *testing.T) {
		s, _, _ := newStore(t, env{"JEVKIT_API_KEY": secret})
		wantKey(t, s, secret, SourceEnv)
	})
	t.Run("env TYPESAFE_API_KEY", func(t *testing.T) {
		s, _, _ := newStore(t, env{"TYPESAFE_API_KEY": secret})
		wantKey(t, s, secret, SourceEnv)
	})
	t.Run("JEVKIT_API_KEY wins over TYPESAFE_API_KEY", func(t *testing.T) {
		s, _, _ := newStore(t, env{"JEVKIT_API_KEY": "a", "TYPESAFE_API_KEY": "b"})
		wantKey(t, s, "a", SourceEnv)
	})
	t.Run("env-file", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		writeEnvFile(t, s, "TYPESAFE_API_KEY="+secret+"\n")
		wantKey(t, s, secret, SourceEnvFile)
	})
	t.Run("command", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		if err := s.SetCommand("echo " + secret); err != nil {
			t.Fatal(err)
		}
		wantKey(t, s, secret, SourceCommand)
	})
	t.Run("keychain", func(t *testing.T) {
		s, kr, _ := newStore(t, nil)
		if err := s.SetKeychain(secret); err != nil {
			t.Fatal(err)
		}
		if kr.sawArgs[0] != "jevkit" || kr.sawArgs[1] != "TYPESAFE_API_KEY" {
			t.Fatalf("keychain entry = %v", kr.sawArgs)
		}
		wantKey(t, s, secret, SourceKeychain)
	})
	t.Run("file", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		if err := s.SetFile(secret); err != nil {
			t.Fatal(err)
		}
		wantKey(t, s, secret, SourceFile)
	})
	t.Run("none", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		k, src, err := resolve(t, s)
		if k != "" || src != SourceNone || !errors.Is(err, ErrNoKey) {
			t.Fatalf("got (%q, %s, %v)", k, src, err)
		}
	})
}

func TestPrecedence(t *testing.T) {
	skipWindows(t)
	full := func(t *testing.T, e env) *Store {
		s, _, _ := newStore(t, e)
		writeEnvFile(t, s, "TYPESAFE_API_KEY=from-envfile\n")
		mustNil(t, s.SetCommand("echo from-command"))
		mustNil(t, s.SetKeychain("from-keychain"))
		mustNil(t, s.SetFile("from-file"))
		return s
	}
	t.Run("env beats everything", func(t *testing.T) {
		wantKey(t, full(t, env{"TYPESAFE_API_KEY": "from-env"}), "from-env", SourceEnv)
	})
	t.Run("env-file beats command keychain file", func(t *testing.T) {
		wantKey(t, full(t, nil), "from-envfile", SourceEnvFile)
	})
	t.Run("command beats keychain and file", func(t *testing.T) {
		s := full(t, env{"JEVKIT_ENV_FILE": "0"})
		wantKey(t, s, "from-command", SourceCommand)
	})
	t.Run("keychain beats file", func(t *testing.T) {
		s := full(t, env{"JEVKIT_ENV_FILE": "0"})
		mustNil(t, s.SetCommand("echo x"))
		c := s.readCreds()
		c.Command = ""
		mustNil(t, s.writeCreds(c))
		wantKey(t, s, "from-keychain", SourceKeychain)
	})
	t.Run("unavailable keychain is skipped for file", func(t *testing.T) {
		s, kr, _ := newStore(t, nil)
		mustNil(t, s.SetKeychain("kc"))
		mustNil(t, s.SetFile("from-file"))
		kr.down = true
		wantKey(t, s, "from-file", SourceFile)
	})
}

func TestEnvFileDisabled(t *testing.T) {
	s, _, _ := newStore(t, env{"JEVKIT_ENV_FILE": "0"})
	writeEnvFile(t, s, "TYPESAFE_API_KEY="+secret+"\n")
	if _, src, _ := resolve(t, s); src != SourceNone {
		t.Fatalf("source = %s; want none", src)
	}
}

func TestEnvFileParsing(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"plain", "TYPESAFE_API_KEY=abc\n", "abc"},
		{"jevkit name", "JEVKIT_API_KEY=abc\n", "abc"},
		{"jevkit preferred", "TYPESAFE_API_KEY=t\nJEVKIT_API_KEY=j\n", "j"},
		{"export prefix", "export TYPESAFE_API_KEY=abc\n", "abc"},
		{"export tab", "export\tTYPESAFE_API_KEY=abc\n", "abc"},
		{"double quotes", `TYPESAFE_API_KEY="abc def"` + "\n", "abc def"},
		{"single quotes", "TYPESAFE_API_KEY='abc'\n", "abc"},
		{"leading space", "   TYPESAFE_API_KEY=abc\n", "abc"},
		{"crlf", "TYPESAFE_API_KEY=abc\r\n", "abc"},
		{"no trailing newline", "TYPESAFE_API_KEY=abc", "abc"},
		{"unquoted trailing space", "TYPESAFE_API_KEY=abc   \n", "abc"},
		{"comments and blanks", "# c\n\n# TYPESAFE_API_KEY=no\nTYPESAFE_API_KEY=abc\n", "abc"},
		{"first assignment wins", "TYPESAFE_API_KEY=one\nTYPESAFE_API_KEY=two\n", "one"},
		{"literal dollar", "TYPESAFE_API_KEY=a$HOME$(id)b\n", "a$HOME$(id)b"},
		{"equals in value", "TYPESAFE_API_KEY=a=b==\n", "a=b=="},
		{"other secret ignored", "OTHER_SECRET=zzz\nTYPESAFE_API_KEY=abc\nDB_PASS=yyy\n", "abc"},
		{"mismatched quotes kept", `TYPESAFE_API_KEY="abc` + "\n", `"abc`},
		{"empty value", "TYPESAFE_API_KEY=\n", ""},
		{"empty quoted", `TYPESAFE_API_KEY=""` + "\n", ""},
		{"prefix name ignored", "TYPESAFE_API_KEY_OLD=x\n", ""},
		{"exported var name ignored", "exportTYPESAFE_API_KEY=x\n", ""},
		{"no key line", "FOO=bar\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newStore(t, nil)
			writeEnvFile(t, s, tc.content)
			got, src, _ := resolve(t, s)
			if tc.want == "" {
				if src != SourceNone {
					t.Fatalf("source = %s (%q); want none", src, got)
				}
				return
			}
			if got != tc.want || src != SourceEnvFile {
				t.Fatalf("got (%q, %s); want (%q, env-file)", got, src, tc.want)
			}
		})
	}
}

func TestEnvFileNeverExecuted(t *testing.T) {
	skipWindows(t)
	s, _, _ := newStore(t, nil)
	marker := filepath.Join(t.TempDir(), "pwned")
	writeEnvFile(t, s, "TYPESAFE_API_KEY=$(touch "+marker+")\nX=`touch "+marker+"`\n")
	got, _, _ := resolve(t, s)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("command substitution in .env was executed")
	}
	if !strings.Contains(got, "$(touch") {
		t.Fatalf("value should be literal, got %q", got)
	}
}

func TestEnvFilePathDiscipline(t *testing.T) {
	s, _, _ := newStore(t, nil)
	// .env.local in the workspace and .env in the parent are both ignored.
	if err := os.WriteFile(filepath.Join(s.Workspace, ".env.local"), []byte("TYPESAFE_API_KEY=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.Workspace), ".env"), []byte("TYPESAFE_API_KEY=parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(filepath.Dir(s.Workspace), ".env")) }) //nolint:errcheck
	if _, src, _ := resolve(t, s); src != SourceNone {
		t.Fatalf("source = %s; want none", src)
	}
}

func TestCommandBackend(t *testing.T) {
	skipWindows(t)
	t.Run("strips one trailing newline", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		mustNil(t, s.SetCommand(`printf 'abc\n\n'`))
		k, _, err := resolve(t, s)
		if err != nil || k != "abc\n" {
			t.Fatalf("got (%q, %v)", k, err)
		}
	})
	t.Run("nonzero exit does not fall through", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		mustNil(t, s.SetCommand("echo "+secret+"; exit 3"))
		mustNil(t, s.SetFile("from-file"))
		k, src, err := resolve(t, s)
		if err == nil || k != "" || src != SourceCommand {
			t.Fatalf("got (%q, %s, %v)", k, src, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error leaks command output")
		}
	})
	t.Run("empty output fails", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		mustNil(t, s.SetCommand("true"))
		if _, src, err := resolve(t, s); err == nil || src != SourceCommand {
			t.Fatalf("got (%s, %v)", src, err)
		}
	})
	t.Run("timeout kills the command", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		s.Timeout = 200 * time.Millisecond
		mustNil(t, s.SetCommand("sleep 30; echo late"))
		start := time.Now()
		_, src, err := resolve(t, s)
		if err == nil || src != SourceCommand || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("got (%s, %v)", src, err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("took %s", d)
		}
	})
	t.Run("timeout env override", func(t *testing.T) {
		s, _, _ := newStore(t, env{"JEVKIT_KEY_TIMEOUT_MS": "150"})
		s.Timeout = 0
		mustNil(t, s.SetCommand("sleep 30"))
		start := time.Now()
		if _, _, err := resolve(t, s); err == nil {
			t.Fatal("want timeout")
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("override ignored")
		}
	})
	t.Run("caller context cancel", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		mustNil(t, s.SetCommand("sleep 30"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := s.Resolve(ctx); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("Source does not run the command", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		marker := filepath.Join(t.TempDir(), "ran")
		mustNil(t, s.SetCommand("touch "+marker+"; echo k"))
		if got := s.Source(context.Background()); got != SourceCommand {
			t.Fatalf("source = %s", got)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("Source executed the command")
		}
	})
	t.Run("empty command rejected", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		if s.SetCommand("  ") == nil {
			t.Fatal("want error")
		}
	})
}

func TestKeychainFailureDoesNotFallThrough(t *testing.T) {
	s, kr, _ := newStore(t, nil)
	mustNil(t, s.SetKeychain("kc"))
	mustNil(t, s.SetFile("from-file"))
	delete(kr.m, "jevkit/TYPESAFE_API_KEY") // configured but entry gone
	k, src, err := resolve(t, s)
	if err == nil || k != "" || src != SourceKeychain {
		t.Fatalf("got (%q, %s, %v)", k, src, err)
	}
}

func TestFilePerms(t *testing.T) {
	skipWindows(t)
	s, _, warn := newStore(t, nil)
	mustNil(t, s.SetFile(secret))
	mustNil(t, s.SetCommand("echo x"))
	for _, p := range []string{s.keyFilePath(), s.credentialsPath()} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o; want 600", p, fi.Mode().Perm())
		}
	}
	if !strings.Contains(warn.String(), "plaintext") || strings.Contains(warn.String(), secret) {
		t.Fatalf("warning = %q", warn.String())
	}
	// Rewriting keeps 0600 even if the old file was loosened.
	mustNil(t, os.Chmod(s.keyFilePath(), 0o644))
	mustNil(t, s.SetFile(secret))
	fi, _ := os.Stat(s.keyFilePath())
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("rewritten mode = %o", fi.Mode().Perm())
	}
}

func TestCredentialsHoldNoSecret(t *testing.T) {
	s, _, _ := newStore(t, nil)
	mustNil(t, s.SetKeychain(secret))
	mustNil(t, s.SetCommand("pass show jev"))
	data, err := os.ReadFile(s.credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("credentials json contains the key")
	}
}

func TestNeverUnderWorkspace(t *testing.T) {
	s, _, _ := newStore(t, nil)
	s.ConfigDir = filepath.Join(s.Workspace, ".jevkit", "state")
	if s.SetFile(secret) == nil {
		t.Fatal("SetFile under workspace should fail")
	}
	if s.SetCommand("echo x") == nil {
		t.Fatal("SetCommand under workspace should fail")
	}
	if s.SetKeychain(secret) == nil {
		t.Fatal("SetKeychain under workspace should fail")
	}
	if _, err := os.Stat(s.ConfigDir); err == nil {
		t.Fatal("config dir was created under the workspace")
	}
}

func TestStatusNeverPrintsKey(t *testing.T) {
	skipWindows(t)
	ctx := context.Background()
	check := func(t *testing.T, s *Store, wantSrc Source) {
		t.Helper()
		out := s.Status(ctx)
		if strings.Contains(out, secret) {
			t.Fatalf("status leaks key: %q", out)
		}
		if !strings.Contains(out, "source: "+string(wantSrc)) {
			t.Fatalf("status = %q", out)
		}
	}
	t.Run("env", func(t *testing.T) {
		s, _, _ := newStore(t, env{"TYPESAFE_API_KEY": secret})
		check(t, s, SourceEnv)
	})
	t.Run("env-file", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		writeEnvFile(t, s, "TYPESAFE_API_KEY="+secret+"\n")
		check(t, s, SourceEnvFile)
	})
	t.Run("command", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		mustNil(t, s.SetCommand("cat /nonexistent-credential-path"))
		check(t, s, SourceCommand)
	})
	t.Run("keychain", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		mustNil(t, s.SetKeychain(secret))
		check(t, s, SourceKeychain)
	})
	t.Run("file", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		mustNil(t, s.SetFile(secret))
		check(t, s, SourceFile)
	})
	t.Run("none", func(t *testing.T) {
		s, _, _ := newStore(t, nil)
		check(t, s, SourceNone)
	})
}

func TestClear(t *testing.T) {
	s, kr, _ := newStore(t, nil)
	mustNil(t, s.SetKeychain("kc"))
	mustNil(t, s.SetFile("f"))
	mustNil(t, s.SetCommand("echo c"))
	mustNil(t, s.Clear())
	if len(kr.m) != 0 {
		t.Fatal("keychain entry survived Clear")
	}
	for _, p := range []string{s.keyFilePath(), s.credentialsPath()} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s survived Clear", p)
		}
	}
	if got := s.Source(context.Background()); got != SourceNone {
		t.Fatalf("source after clear = %s", got)
	}
	mustNil(t, s.Clear()) // idempotent
}

func TestSetRejectsEmptyKey(t *testing.T) {
	s, _, _ := newStore(t, nil)
	if s.SetFile("") == nil || s.SetKeychain("") == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := ReadKey(strings.NewReader("\n")); err == nil {
		t.Fatal("ReadKey accepted empty stdin")
	}
	k, err := ReadKey(strings.NewReader("abc\n"))
	if err != nil || k != "abc" {
		t.Fatalf("ReadKey = %q, %v", k, err)
	}
}

func TestKeychainUnavailableAtSet(t *testing.T) {
	s, kr, _ := newStore(t, nil)
	kr.down = true
	err := s.SetKeychain(secret)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(s.credentialsPath()); statErr == nil {
		t.Fatal("keychain selected despite failed Set")
	}
}

func TestKeyNotInArgv(t *testing.T) {
	skipWindows(t)
	// The command backend receives only the stored command in argv; the key it
	// prints is never passed anywhere. Prove the child sees no key in argv.
	s, _, _ := newStore(t, nil)
	out := filepath.Join(t.TempDir(), "argv")
	mustNil(t, s.SetCommand(`printf '%s ' "$0" "$@" > `+out+`; echo `+secret))
	if _, _, err := resolve(t, s); err != nil {
		t.Fatal(err)
	}
	argv, _ := os.ReadFile(out)
	if strings.Contains(string(argv), secret) {
		t.Fatalf("key in argv: %q", argv)
	}
}

func TestConfigDir(t *testing.T) {
	get := func(m env) func(string) string { return m.get }
	if got := ConfigDir(get(env{"JEVKIT_CONFIG_HOME": "/a", "XDG_CONFIG_HOME": "/x"})); got != "/a" {
		t.Fatalf("got %s", got)
	}
	if got := ConfigDir(get(env{"XDG_CONFIG_HOME": "/x"})); got != filepath.Join("/x", "jevkit") {
		t.Fatalf("got %s", got)
	}
	if got := ConfigDir(get(env{})); filepath.Base(got) != "jevkit" {
		t.Fatalf("got %s", got)
	}
}

func TestCorruptCredentialsTreatedAsUnconfigured(t *testing.T) {
	s, _, _ := newStore(t, nil)
	mustNil(t, os.MkdirAll(s.ConfigDir, 0o700))
	mustNil(t, os.WriteFile(s.credentialsPath(), []byte("{not json"), 0o600))
	if _, src, _ := resolve(t, s); src != SourceNone {
		t.Fatalf("source = %s", src)
	}
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
