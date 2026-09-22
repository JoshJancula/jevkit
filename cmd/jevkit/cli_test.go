package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/breaker"
	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/keystore"
	"github.com/OWNER/jevkit/internal/usage"
)

const secretKey = "sk-test-SECRET-0123456789"

// fakeKeyring is an in-memory keychain.
type fakeKeyring struct {
	items       map[string]string
	unavailable bool
}

func (k *fakeKeyring) id(service, account string) string { return service + "/" + account }

func (k *fakeKeyring) Get(service, account string) (string, error) {
	if k.unavailable {
		return "", errors.New("no keychain")
	}
	v, ok := k.items[k.id(service, account)]
	if !ok {
		return "", keystore.ErrKeyringNotFound
	}
	return v, nil
}

func (k *fakeKeyring) Set(service, account, secret string) error {
	if k.unavailable {
		return errors.New("no keychain")
	}
	if k.items == nil {
		k.items = map[string]string{}
	}
	k.items[k.id(service, account)] = secret
	return nil
}

func (k *fakeKeyring) Delete(service, account string) error {
	delete(k.items, k.id(service, account))
	return nil
}

// fakeJev records calls and returns a canned result.
type fakeJev struct {
	calls int
	keys  []string
	err   error
}

func (f *fakeJev) factory(t *testing.T) func(jev.Config, func() (string, error)) Asker {
	return func(_ jev.Config, key func() (string, error)) Asker {
		k, err := key()
		if err != nil {
			t.Fatalf("key func: %v", err)
		}
		f.keys = append(f.keys, k)
		return f
	}
}

func (f *fakeJev) Ask(context.Context, jev.Request) (*jev.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &jev.Response{}, nil
}

// cliApp is newApp plus a fake keychain and jev client.
func cliApp(t *testing.T) (*App, *fakeKeyring, *fakeJev) {
	t.Helper()
	a := newApp(t)
	kr := &fakeKeyring{}
	fj := &fakeJev{}
	a.Keyring = kr
	a.NewJev = fj.factory(t)
	a.ReadSecret = func() ([]byte, bool, error) { return nil, false, nil }
	return a, kr, fj
}

func noSecret(t *testing.T, what string, outs ...string) {
	t.Helper()
	for _, o := range outs {
		if strings.Contains(o, secretKey) {
			t.Errorf("%s leaked the key:\n%s", what, o)
		}
	}
}

func TestKeySetRejectsArgvKey(t *testing.T) {
	for _, args := range [][]string{
		{"key", "set", secretKey},
		{"key", "set", "--command", "echo hi", secretKey},
		{"key", "set", "-k" + secretKey},
		{"key", "set", "--key=" + secretKey},
	} {
		a, kr, _ := cliApp(t)
		code, out, errs := run(a, "", args...)
		if code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
		noSecret(t, strings.Join(args[:2], " "), out, errs)
		if len(kr.items) != 0 {
			t.Errorf("%v stored something: %v", args, kr.items)
		}
		if _, err := os.Stat(a.ConfigDir); err == nil {
			t.Errorf("%v wrote to the config dir", args)
		}
	}
}

func TestKeySetFromStdin(t *testing.T) {
	a, kr, _ := cliApp(t)
	code, out, errs := run(a, secretKey+"\n", "key", "set")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	noSecret(t, "key set", out, errs)
	if got := kr.items["jevkit/TYPESAFE_API_KEY"]; got != secretKey {
		t.Errorf("keychain holds %q", got)
	}
	if !strings.Contains(out, "keychain") {
		t.Errorf("output does not name the keychain: %s", out)
	}
	if code, out, _ := run(a, "", "key", "status"); code != exitOK || !strings.Contains(out, "source: keychain") {
		t.Errorf("status: %d %s", code, out)
	} else {
		noSecret(t, "key status", out)
	}
}

func TestKeySetTerminalPromptIsNotStdin(t *testing.T) {
	a, kr, _ := cliApp(t)
	a.ReadSecret = func() ([]byte, bool, error) { return []byte(secretKey + "\n"), true, nil }
	code, out, errs := run(a, "ignored-stdin", "key", "set")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	if kr.items["jevkit/TYPESAFE_API_KEY"] != secretKey {
		t.Errorf("terminal key not stored: %v", kr.items)
	}
	noSecret(t, "key set", out, errs)
}

func TestKeySetEmptyStdinFails(t *testing.T) {
	a, kr, _ := cliApp(t)
	if code, _, _ := run(a, "\n", "key", "set"); code != exitFail {
		t.Errorf("empty key: exit %d", code)
	}
	if len(kr.items) != 0 {
		t.Errorf("stored an empty key: %v", kr.items)
	}
}

func TestKeySetFallsBackToFileWithoutKeychain(t *testing.T) {
	a, kr, _ := cliApp(t)
	kr.unavailable = true
	code, out, errs := run(a, secretKey+"\n", "key", "set")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	noSecret(t, "key set", out, errs)
	if !strings.Contains(errs, "plaintext") {
		t.Errorf("no plaintext warning: %q", errs)
	}
	if got := readFile(t, filepath.Join(a.ConfigDir, "jev-api-key")); got != secretKey {
		t.Errorf("file holds %q", got)
	}
}

func TestKeySetCommandStoresNoSecret(t *testing.T) {
	a, kr, _ := cliApp(t)
	code, out, errs := run(a, "", "key", "set", "--command", "pass show typesafe")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	if len(kr.items) != 0 {
		t.Errorf("--command touched the keychain: %v", kr.items)
	}
	_, out, _ = run(a, "", "key", "status")
	for _, want := range []string{"source: command", "command: pass show typesafe"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if code, _, _ := run(a, "", "key", "set", "--command", "  "); code != exitUsage {
		t.Errorf("blank --command: exit %d", code)
	}
}

func TestKeyStatusAndClear(t *testing.T) {
	a, kr, _ := cliApp(t)
	if code, out, _ := run(a, "", "key", "status"); code != exitFail || !strings.Contains(out, "source: none") {
		t.Errorf("unconfigured status: %d %q", code, out)
	}
	run(a, secretKey+"\n", "key", "set")
	if code, out, errs := run(a, "", "key", "clear"); code != exitOK {
		t.Fatalf("clear: %d %s%s", code, out, errs)
	}
	if len(kr.items) != 0 {
		t.Errorf("clear left keychain entries: %v", kr.items)
	}
	if code, out, _ := run(a, "", "key", "status"); code != exitFail || !strings.Contains(out, "source: none") {
		t.Errorf("status after clear: %d %q", code, out)
	}
}

func TestKeyTest(t *testing.T) {
	t.Run("pass", func(t *testing.T) {
		a, _, fj := cliApp(t)
		run(a, secretKey+"\n", "key", "set")
		code, out, errs := run(a, "", "key", "test")
		if code != exitOK || !strings.HasPrefix(out, "pass") {
			t.Fatalf("exit %d: %q %q", code, out, errs)
		}
		if fj.calls != 1 || fj.keys[0] != secretKey {
			t.Errorf("calls=%d keys=%v", fj.calls, len(fj.keys))
		}
		noSecret(t, "key test", out, errs)
	})
	t.Run("fail never echoes the key", func(t *testing.T) {
		a, _, fj := cliApp(t)
		run(a, secretKey+"\n", "key", "set")
		fj.err = &jev.Error{Code: jev.CodeTransport, Reason: "http-401", Config: true, Err: errors.New("bearer " + secretKey)}
		code, out, errs := run(a, "", "key", "test")
		if code != exitFail || !strings.Contains(errs, "fail (http-401") {
			t.Fatalf("exit %d: %q %q", code, out, errs)
		}
		noSecret(t, "key test failure", out, errs)
	})
	t.Run("no key makes no call", func(t *testing.T) {
		a, _, fj := cliApp(t)
		code, _, errs := run(a, "", "key", "test")
		if code != exitFail || fj.calls != 0 {
			t.Errorf("exit %d calls %d: %s", code, fj.calls, errs)
		}
	})
}

// Every command except `key test` must stay offline.
func TestOnlyKeyTestCallsJev(t *testing.T) {
	a, _, fj := cliApp(t)
	a.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("down") }
	run(a, secretKey+"\n", "key", "set")
	for _, args := range [][]string{
		{"key", "status"}, {"key", "clear"}, {"usage"}, {"usage", "--format", "json"},
		{"doctor"}, {"version"}, {"migrate-from-ralph"},
		{"install", "claude", "--dry-run"}, {"uninstall", "claude", "--dry-run"},
	} {
		run(a, "", args...)
	}
	if fj.calls != 0 || len(fj.keys) != 0 {
		t.Errorf("a non-test command reached the jev client: calls=%d", fj.calls)
	}
}

func TestDoctorReportsWithoutKey(t *testing.T) {
	a, _, _ := cliApp(t)
	a.Environ = append(a.Environ, "JEVKIT_API_KEY="+secretKey, "JEVKIT_ENDPOINT=https://jev.example.test:8443/v1")
	var dialed string
	a.Dial = func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = network + " " + addr
		c, _ := net.Pipe()
		return c, nil
	}
	a.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/opt/bin/claude", nil
		}
		return "", errors.New("not found")
	}
	code, out, errs := run(a, "", "doctor")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	noSecret(t, "doctor", out, errs)
	for _, want := range []string{
		"source:        env",
		"https://jev.example.test:8443/v1",
		"reachable (tcp connect to jev.example.test:8443; no request sent)",
		"state:         closed",
		"claude:        /opt/bin/claude",
		"hooks:       not installed",
		"mcp:         not installed",
		"codex:         not found",
		"redaction",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}
	if dialed != "tcp jev.example.test:8443" {
		t.Errorf("dialed %q", dialed)
	}
}

func TestDoctorUnreachableAndBreakerOpen(t *testing.T) {
	a, _, _ := cliApp(t)
	a.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("dial " + secretKey) }
	breaker.New(a.stateHome()).Open("http-401")
	code, out, errs := run(a, "", "doctor")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	noSecret(t, "doctor", out, errs)
	for _, want := range []string{"source:        none", "unreachable (tcp api.typesafe.ai:443)", "state:         open (http-401)"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}
}

func TestDoctorFixtureTransportIsOffline(t *testing.T) {
	a, _, _ := cliApp(t)
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	a.Dial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("dialed under fixture transport")
		return nil, errors.New("no")
	}
	_, out, _ := run(a, "", "doctor")
	if !strings.Contains(out, "not checked (fixture transport") {
		t.Errorf("doctor: %s", out)
	}
}

func seedUsage(t *testing.T, a *App) {
	t.Helper()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339)
	for _, r := range []usage.Record{
		{Timestamp: at, Model: "jev-latest", QuestionSetID: "qs", InputTokens: 1000, OutputTokens: 10, UsageSource: usage.SourceMeasured, Transport: usage.TransportHTTPS},
		{Timestamp: at, Model: "jev-latest", QuestionSetID: "qs", InputTokens: 500, OutputTokens: 5, UsageSource: usage.SourceMeasured, Transport: usage.TransportFixture},
	} {
		if err := usage.Append(a.stateHome(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsage(t *testing.T) {
	a, _, _ := cliApp(t)
	if code, out, _ := run(a, "", "usage"); code != exitOK || !strings.Contains(out, "no recorded calls") {
		t.Errorf("empty usage: %d %q", code, out)
	}
	seedUsage(t, a)

	code, out, errs := run(a, "", "usage")
	if code != exitOK || !strings.Contains(out, "calls: 1 ") || !strings.Contains(out, "Jev (TypeSafe AI) usage") {
		t.Errorf("text: %d %q %q", code, out, errs)
	}
	if _, out, _ := run(a, "", "usage", "--include-fixture"); !strings.Contains(out, "calls: 2 ") {
		t.Errorf("--include-fixture: %q", out)
	}

	for _, tc := range []struct {
		args  []string
		calls int
		in    int
	}{
		{[]string{"usage", "--format", "json"}, 1, 1000},
		{[]string{"usage", "--format=json", "--include-fixture"}, 2, 1500},
	} {
		code, out, errs := run(a, "", tc.args...)
		var sum usage.Summary
		if code != exitOK || json.Unmarshal([]byte(out), &sum) != nil {
			t.Fatalf("%v: %d %q %q", tc.args, code, out, errs)
		}
		if sum.Calls != tc.calls || sum.InputTokens != tc.in {
			t.Errorf("%v: calls=%d in=%d", tc.args, sum.Calls, sum.InputTokens)
		}
	}
	if code, _, errs := run(a, "", "usage", "--format", "xml"); code != exitUsage || !strings.Contains(errs, "xml") {
		t.Errorf("bad format: %d %q", code, errs)
	}
}

// ralphFixture lays out ralph's config dir and keychain entry.
func ralphFixture(t *testing.T, a *App, creds string, keyFile string, keychainKey string, kr *fakeKeyring) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ralph")
	a.Environ = append(a.Environ, "RALPH_CONFIG_HOME="+dir)
	if creds != "" {
		writeFile(t, filepath.Join(dir, "jev-credentials.json"), creds)
	}
	if keyFile != "" {
		writeFile(t, filepath.Join(dir, "jev-api-key"), keyFile)
	}
	if keychainKey != "" {
		_ = kr.Set("ralph.jev", "TYPESAFE_API_KEY", keychainKey)
	}
}

func TestMigrateFromRalphKeychainAndCommand(t *testing.T) {
	a, kr, _ := cliApp(t)
	ralphFixture(t, a, `{"schema_version":1,"command":"pass show typesafe","keychain":true,"file":false}`, "", secretKey, kr)
	code, out, errs := run(a, "", "migrate-from-ralph")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	noSecret(t, "migrate-from-ralph", out, errs)
	if kr.items["jevkit/TYPESAFE_API_KEY"] != secretKey {
		t.Errorf("key not copied to jevkit keychain: %v", kr.items)
	}
	if kr.items["ralph.jev/TYPESAFE_API_KEY"] != secretKey {
		t.Error("ralph's keychain entry was removed")
	}
	_, out, _ = run(a, "", "key", "status")
	if !strings.Contains(out, "command: pass show typesafe") {
		t.Errorf("command not copied:\n%s", out)
	}
	noSecret(t, "status", out)
}

func TestMigrateFromRalphKeyFile(t *testing.T) {
	a, kr, _ := cliApp(t)
	kr.unavailable = true
	ralphFixture(t, a, `{"schema_version":1,"command":"","keychain":false,"file":true}`, secretKey+"\n", "", kr)
	code, out, errs := run(a, "", "migrate-from-ralph")
	if code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	noSecret(t, "migrate-from-ralph", out, errs)
	if got := readFile(t, filepath.Join(a.ConfigDir, "jev-api-key")); got != secretKey {
		t.Errorf("file holds %q", got)
	}
	if !strings.Contains(out, "ralph's key file") {
		t.Errorf("origin not named: %s", out)
	}
}

func TestMigrateIsOneTimeUnlessForced(t *testing.T) {
	a, kr, _ := cliApp(t)
	ralphFixture(t, a, `{"command":"","keychain":true}`, "", secretKey, kr)
	if code, out, errs := run(a, "", "migrate-from-ralph"); code != exitOK {
		t.Fatalf("first: %d %s%s", code, out, errs)
	}
	kr.items["ralph.jev/TYPESAFE_API_KEY"] = "sk-rotated-000"
	code, out, errs := run(a, "", "migrate-from-ralph")
	if code != exitFail || !strings.Contains(errs, "--force") {
		t.Fatalf("second: %d %q %q", code, out, errs)
	}
	if kr.items["jevkit/TYPESAFE_API_KEY"] != secretKey {
		t.Error("second run overwrote the key without --force")
	}
	if code, _, errs := run(a, "", "migrate-from-ralph", "--force"); code != exitOK {
		t.Fatalf("forced: %d %s", code, errs)
	}
	if kr.items["jevkit/TYPESAFE_API_KEY"] != "sk-rotated-000" {
		t.Error("--force did not replace the key")
	}
}

func TestMigrateNothingToDo(t *testing.T) {
	a, _, _ := cliApp(t)
	ralphFixture(t, a, "", "", "", &fakeKeyring{})
	code, _, errs := run(a, "", "migrate-from-ralph")
	if code != exitFail || !strings.Contains(errs, "nothing to migrate") {
		t.Errorf("exit %d: %q", code, errs)
	}
	ralphFixture(t, a, "{not json", "", "", &fakeKeyring{})
	if code, _, _ := run(a, "", "migrate-from-ralph"); code != exitFail {
		t.Errorf("corrupt credentials: exit %d", code)
	}
}

func TestMigrateDefaultsToHomeConfigRalph(t *testing.T) {
	a, kr, _ := cliApp(t)
	home := filepath.Join(t.TempDir(), "h")
	a.Environ = []string{"HOME=" + home}
	writeFile(t, filepath.Join(home, ".config", "ralph", "jev-credentials.json"), `{"command":"echo k"}`)
	if code, _, errs := run(a, "", "migrate-from-ralph"); code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if len(kr.items) != 0 {
		t.Errorf("unexpected keychain writes: %v", kr.items)
	}
}

func TestCLIUsageErrors(t *testing.T) {
	a, _, _ := cliApp(t)
	for _, args := range [][]string{
		{}, {"bogus"}, {"key"}, {"key", "bogus"}, {"key", "status", "extra"},
		{"usage", "extra"}, {"doctor", "--nope"}, {"version", "x"},
		{"install"}, {"uninstall"}, {"install", "claude", "--scope", "galaxy"},
	} {
		if code, _, _ := run(a, "", args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	for _, args := range [][]string{{"--help"}, {"help"}, {"key", "set", "--help"}, {"usage", "-h"}} {
		if code, out, _ := run(a, "", args...); code != exitOK || out == "" {
			t.Errorf("%v: exit %d out %q", args, code, out)
		}
	}
}
