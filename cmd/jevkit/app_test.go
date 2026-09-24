package main

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// noNetwork fails the test if anything uses the default HTTP transport.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("unexpected network call to %s", r.URL)
	return nil, errors.New("network is forbidden in this test")
}

func newApp(t *testing.T) *App {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = noNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = old })
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return &App{
		Stdin:     strings.NewReader(""),
		Environ:   []string{"HOME=" + home},
		WorkDir:   work,
		HomeDir:   home,
		ConfigDir: filepath.Join(root, "cfg"),
		StateDir:  filepath.Join(root, "state"),
		Version:   "test",
		Binary:    "jevkit",
	}
}

// run executes one command with fresh buffers.
func run(a *App, stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	a.Stdout, a.Stderr, a.Stdin = &out, &errb, strings.NewReader(stdin)
	code = a.Run(args)
	return code, out.String(), errb.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustRun(t *testing.T, a *App, stdin string, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	code, out, errs := run(a, stdin, args...)
	if code != want {
		t.Fatalf("jevkit %v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, code, want, out, errs)
	}
	return out, errs
}

func TestInitCreatesPrivateFileThatPassesCheck(t *testing.T) {
	a := newApp(t)
	out, _ := mustRun(t, a, "", 0, "redact", "init")
	if !strings.Contains(out, a.userPath()) {
		t.Errorf("init did not report the path: %q", out)
	}
	fi, err := os.Stat(a.userPath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o, want 0600", fi.Mode().Perm())
	}
	if body := readFile(t, a.userPath()); strings.Count(body, "#") < 40 {
		t.Errorf("starter is not heavily commented:\n%s", body)
	}
	out, _ = mustRun(t, a, "", 0, "redact", "check")
	if !strings.Contains(out, "ok:") {
		t.Errorf("check output = %q", out)
	}

	// Refuses to clobber, unless forced.
	writeFile(t, a.userPath(), "version: 1\n")
	mustRun(t, a, "", 1, "redact", "init")
	if got := readFile(t, a.userPath()); got != "version: 1\n" {
		t.Errorf("init without --force changed the file: %q", got)
	}
	mustRun(t, a, "", 0, "redact", "init", "--force")
	if got := readFile(t, a.userPath()); got == "version: 1\n" {
		t.Error("--force did not rewrite the file")
	}
}

func TestInitProject(t *testing.T) {
	a := newApp(t)
	mustRun(t, a, "", 0, "redact", "init", "--project")
	path := filepath.Join(a.WorkDir, ".jevkit", "redact.yaml")
	if a.projectPath() != path {
		t.Fatalf("projectPath = %s", a.projectPath())
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o, want 0600", fi.Mode().Perm())
	}
	if _, err := os.Stat(a.userPath()); err == nil {
		t.Error("--project also wrote the user file")
	}
	mustRun(t, a, "", 0, "redact", "check")
}

func TestAddPreservesCommentsAndValidates(t *testing.T) {
	a := newApp(t)
	mustRun(t, a, "", 0, "redact", "init")
	before := readFile(t, a.userPath())

	mustRun(t, a, "", 0, "redact", "add", "--literal", "acme-prod-db-password")
	mustRun(t, a, "", 0, "redact", "add", "--env", "STRIPE_*")
	mustRun(t, a, "", 0, "redact", "add", "--never-send", "terraform output*")
	out, _ := mustRun(t, a, "", 0, "redact", "add", "--pattern", `corp-[a-z0-9-]+\.internal`)
	if !strings.Contains(out, "custom.rule-1") {
		t.Errorf("first rule id: %q", out)
	}
	out, _ = mustRun(t, a, "", 0, "redact", "add", "--pattern", `zone-[0-9]{4}`, "--flags", "i", "--replacement", "[ZONE]")
	if !strings.Contains(out, "custom.rule-2") {
		t.Errorf("second rule id: %q", out)
	}
	mustRun(t, a, "", 0, "redact", "add", "--pattern", `build-[0-9]{6}`, "--id", "custom.build")

	after := readFile(t, a.userPath())
	for _, line := range strings.Split(before, "\n") {
		if strings.HasPrefix(line, "#") && !strings.Contains(after, line) {
			t.Errorf("comment lost: %q\n---\n%s", line, after)
		}
	}
	for _, want := range []string{"acme-prod-db-password", "STRIPE_*", "terraform output*", "custom.rule-1", "custom.build", "replacement: '[ZONE]'"} {
		if !strings.Contains(after, want) {
			t.Errorf("result lacks %q:\n%s", want, after)
		}
	}
	if strings.Contains(out, "acme-prod") {
		t.Error("add echoed a literal")
	}

	// The edited file is live: the rule fires through `redact test`.
	stdout, _ := mustRun(t, a, "host corp-build.internal and acme-prod-db-password\n", 0, "redact", "test")
	if strings.Contains(stdout, "corp-build") || strings.Contains(stdout, "acme-prod") {
		t.Errorf("added entries did not redact: %q", stdout)
	}
	mustRun(t, a, "", 0, "redact", "check")

	// Adding an existing entry changes nothing.
	snapshot := readFile(t, a.userPath())
	out, _ = mustRun(t, a, "", 0, "redact", "add", "--env", "STRIPE_*")
	if !strings.Contains(out, "already present") || readFile(t, a.userPath()) != snapshot {
		t.Errorf("duplicate add: %q", out)
	}

	// No temp files are left behind.
	entries, err := os.ReadDir(a.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "redact.yaml" {
			t.Errorf("stray file %s", e.Name())
		}
	}
}

func TestAddRejectsInvalidInputWithoutModifyingFile(t *testing.T) {
	a := newApp(t)
	mustRun(t, a, "", 0, "redact", "init")
	mustRun(t, a, "", 0, "redact", "add", "--pattern", `keep-[0-9]+x`, "--id", "custom.keep")
	snapshot := readFile(t, a.userPath())

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bad regex", []string{"--pattern", "("}, "rules[1].pattern"},
		{"empty-matching regex", []string{"--pattern", "x*"}, "empty string"},
		{"short literal", []string{"--literal", "abc"}, "literals[0]"},
		{"bad env name", []string{"--env", "not a name"}, "env_values[0]"},
		{"bad never_send", []string{"--never-send", "***"}, "never_send[0]"},
		{"colliding id", []string{"--pattern", "other-[0-9]+", "--id", "custom.keep"}, "duplicate rule id"},
		{"builtin id", []string{"--pattern", "other-[0-9]+", "--id", "builtin.email"}, "builtin"},
		{"bad flags", []string{"--pattern", "abcd", "--flags", "x"}, "flags"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := mustRun(t, a, "", 1, append([]string{"redact", "add"}, tc.args...)...)
			if !strings.Contains(errs, tc.want) || !strings.Contains(errs, "left unchanged") {
				t.Errorf("stderr = %q, want %q", errs, tc.want)
			}
			if got := readFile(t, a.userPath()); got != snapshot {
				t.Errorf("file modified:\n%s", got)
			}
			if strings.Contains(errs, ".tmp") {
				t.Errorf("error leaks the temp name: %q", errs)
			}
		})
	}

	// Usage errors.
	mustRun(t, a, "", 2, "redact", "add")
	mustRun(t, a, "", 2, "redact", "add", "--literal", "abcdefgh", "--env", "FOO")
	mustRun(t, a, "", 2, "redact", "add", "--literal", "abcdefgh", "--id", "x")
	if got := readFile(t, a.userPath()); got != snapshot {
		t.Error("usage error modified the file")
	}
}

func TestAddCreatesMissingFilesAndProjectLayer(t *testing.T) {
	a := newApp(t)
	mustRun(t, a, "", 0, "redact", "add", "--literal", "super-secret-value")
	if got := readFile(t, a.userPath()); !strings.Contains(got, "version: 1") || !strings.Contains(got, "super-secret-value") {
		t.Errorf("created file:\n%s", got)
	}
	mustRun(t, a, "", 0, "redact", "add", "--project", "--never-send", "make deploy*")
	if got := readFile(t, a.projectPath()); !strings.Contains(got, "make deploy*") {
		t.Errorf("project file:\n%s", got)
	}
	// An untrusted project file cannot gain a key it may not have; a rule is fine.
	mustRun(t, a, "", 0, "redact", "add", "--project", "--pattern", "proj-[0-9]{5}")
}

func TestAddRejectsWhenAnotherLayerIsBroken(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.projectPath(), "version: 1\nallowlist:\n  - literal: x\n")
	_, errs := mustRun(t, a, "", 1, "redact", "add", "--literal", "abcdefgh")
	if !strings.Contains(errs, "allowlist") {
		t.Errorf("stderr = %q", errs)
	}
	if _, err := os.Stat(a.userPath()); err == nil {
		t.Error("a rejected add created the user file")
	}
}

func TestTestRedactsFromStdinAndFileWithoutNetwork(t *testing.T) {
	a := newApp(t)
	in := "hello\nmail bob@example.org\nbye\n"

	out, errs := mustRun(t, a, in, 0, "redact", "test")
	if out != "hello\nmail [REDACTED]\nbye\n" {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(errs, "builtin.email") || !strings.Contains(errs, "HITS") {
		t.Errorf("hit table = %q", errs)
	}

	out, _ = mustRun(t, a, in, 0, "redact", "test", "--diff", "-")
	want := "--- stdin\n+++ redacted\n@@ -1,3 +1,3 @@\n hello\n-mail bob@example.org\n+mail [REDACTED]\n bye\n"
	if out != want {
		t.Errorf("diff = %q\nwant %q", out, want)
	}

	path := filepath.Join(t.TempDir(), "in.txt")
	writeFile(t, path, in)
	out, _ = mustRun(t, a, "", 0, "redact", "test", path, "--diff")
	if !strings.HasPrefix(out, "--- "+path+"\n+++ redacted\n") {
		t.Errorf("file diff header: %q", out)
	}

	out, errs = mustRun(t, a, "nothing here\n", 0, "redact", "test", "--diff")
	if out != "" || !strings.Contains(errs, "no rules matched") {
		t.Errorf("clean input: out=%q err=%q", out, errs)
	}
	mustRun(t, a, "", 1, "redact", "test", filepath.Join(t.TempDir(), "missing"))
	mustRun(t, a, "", 2, "redact", "test", "a", "b")
}

func TestTestUsesConfigAndFailsClosed(t *testing.T) {
	a := newApp(t)
	a.Environ = append(a.Environ, "MY_API_TOKEN=tok_abcdefghijklmnop")
	writeFile(t, a.userPath(), "version: 1\nrules:\n  - id: custom.host\n    pattern: 'corp-[a-z]+\\.internal'\n    replacement: '[HOST]'\n")
	out, errs := mustRun(t, a, "x corp-build.internal tok_abcdefghijklmnop\n", 0, "redact", "test")
	if out != "x [HOST] [REDACTED]\n" {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(errs, "custom.host") {
		t.Errorf("hit table = %q", errs)
	}

	writeFile(t, a.userPath(), "version: 1\nrules:\n  - id: custom.bad\n    pattern: '('\n")
	out, errs = mustRun(t, a, "bob@example.org\n", 1, "redact", "test")
	if out != "" || !strings.Contains(errs, "rules[0].pattern") {
		t.Errorf("broken config must print nothing: out=%q err=%q", out, errs)
	}
}

func TestListShowsClassSourceAndState(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.userPath(), "version: 1\ndisable: [builtin.email]\nrules:\n  - id: custom.user-rule\n    pattern: 'uu-[0-9]{4}'\n")
	writeFile(t, a.projectPath(), "version: 1\nrules:\n  - id: custom.proj-rule\n    pattern: 'pp-[0-9]{4}'\n")
	out, _ := mustRun(t, a, "", 0, "redact", "list")
	want := map[string][]string{
		"builtin.openai-key":   {"HARD", "builtin", "enabled"},
		"builtin.email":        {"SOFT", "builtin", "disabled"},
		"builtin.ipv4":         {"SOFT", "builtin", "enabled"},
		"builtin.high-entropy": {"SOFT", "builtin", "enabled"},
		"custom.user-rule":     {"HARD", "user", "enabled"},
		"custom.proj-rule":     {"HARD", "project", "enabled"},
	}
	for id, cols := range want {
		var got []string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "│ "+id+" ") {
				cells := strings.Split(l, "│")
				for _, cell := range cells[1 : len(cells)-1] {
					got = append(got, strings.TrimSpace(cell))
				}
			}
		}
		if len(got) == 0 {
			t.Errorf("no row for %s in:\n%s", id, out)
			continue
		}
		if strings.Join(got[1:], " ") != strings.Join(cols, " ") {
			t.Errorf("%s: row %q, want %v", id, got, cols)
		}
	}
	if !strings.Contains(out, "mode: standard") {
		t.Errorf("no mode line:\n%s", out)
	}
}

func TestExplain(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.userPath(), "version: 1\nrules:\n  - id: custom.host\n    pattern: 'corp-[a-z]+'\n    flags: i\n")

	out, _ := mustRun(t, a, "", 0, "redact", "explain", "builtin.high-entropy")
	for _, want := range []string{"SOFT", "entropy_threshold", "allowlist", "disable"} {
		if !strings.Contains(out, want) {
			t.Errorf("soft explain lacks %q:\n%s", want, out)
		}
	}
	out, _ = mustRun(t, a, "", 0, "redact", "explain", "openai-key")
	if !strings.Contains(out, "HARD") || !strings.Contains(out, "cannot be disabled") || !strings.Contains(out, "sk-") {
		t.Errorf("hard explain:\n%s", out)
	}
	out, _ = mustRun(t, a, "", 0, "redact", "explain", "custom.host")
	if !strings.Contains(out, "corp-[a-z]+") || !strings.Contains(out, a.userPath()) || !strings.Contains(out, "HARD") {
		t.Errorf("custom explain:\n%s", out)
	}
	_, errs := mustRun(t, a, "", 1, "redact", "explain", "nope")
	if !strings.Contains(errs, "unknown rule") {
		t.Errorf("stderr = %q", errs)
	}
	mustRun(t, a, "", 2, "redact", "explain")
}

func TestCheckCatchesBrokenRule(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.userPath(), "version: 1\nrules:\n  - id: custom.ok\n    pattern: 'fine-[0-9]{4}'\n  - id: custom.broken\n    pattern: 'unclosed('\n")
	out, errs := mustRun(t, a, "", 1, "redact", "check")
	if !strings.Contains(errs, "rules[1].pattern") || !strings.Contains(errs, a.userPath()) {
		t.Errorf("check must name file and key: out=%q err=%q", out, errs)
	}
}

func TestCheckLintFindings(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.userPath(), `version: 1
literals:
  - corp-secret-host
  - corp-secret-host
env_values: [STRIPE_*, STRIPE_*]
never_send: ["terraform output*", "terraform output*", "cat .env*"]
rules:
  - id: custom.first
    pattern: 'internal-host'
  - id: custom.later
    pattern: 'internal-host-two'
  - id: custom.twin-a
    pattern: 'twin-[0-9]{5}'
  - id: custom.twin-b
    pattern: 'twin-[0-9]{5}'
  - id: custom.by-literal
    pattern: 'corp-secret-host-2'
  - id: custom.multiline
    pattern: 'foo\nbar'
  - id: custom.broad
    pattern: '\w+'
disable: [builtin.ipv4]
allowlist:
  - rule: builtin.ipv4
    literal: "10.0.0.1"
`)
	out, _ := mustRun(t, a, "", 1, "redact", "check")
	for _, want := range []string{
		"lint [duplicate]: literal (hidden)",
		"lint [duplicate]: env_values entry \"STRIPE_*\"",
		"lint [duplicate]: never_send pattern \"terraform output*\"",
		"lint [duplicate]: never_send pattern \"cat .env*\" in the user file is already listed in the built-in list",
		"lint [duplicate]: rule custom.twin-b has the same pattern as custom.twin-a",
		"lint [shadowed]: rule custom.later never fires: its text is always redacted first by rule custom.first",
		"lint [shadowed]: rule custom.by-literal never fires: its text is always redacted first by a literal",
		"lint [never-matches]: rule custom.multiline can never match",
		"lint [never-matches]: allowlist[0] targets builtin.ipv4, which is disabled",
		"lint [over-broad]: rule custom.broad matches ordinary text",
		"FAIL:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "corp-secret-host\"") {
		t.Errorf("lint printed a literal:\n%s", out)
	}
	if strings.Contains(out, "custom.first never") || strings.Contains(out, "custom.twin-a never") {
		t.Errorf("false positive on an unshadowed rule:\n%s", out)
	}
}

func TestCheckRunsEmbeddedTests(t *testing.T) {
	a := newApp(t)
	passing := `version: 1
rules:
  - id: custom.host
    pattern: 'corp-[a-z]+\.internal'
    replacement: '[HOST]'
tests:
  - name: host hidden
    input: "curl https://corp-build.internal/x"
    must_not_contain: ["corp-build"]
    must_contain: ["[HOST]", "curl"]
  - input: "no secrets here"
    must_contain: ["no secrets here"]
`
	writeFile(t, a.userPath(), passing)
	out, _ := mustRun(t, a, "", 0, "redact", "check")
	if !strings.Contains(out, "2 embedded test(s) passed") {
		t.Errorf("out = %q", out)
	}

	// A rule that no longer matches leaks the value: the test must fail, and
	// the output must name the case but not echo the needle.
	writeFile(t, a.userPath(), strings.Replace(passing, `corp-[a-z]+\.internal`, `corp-[0-9]+\.internal`, 1))
	out, _ = mustRun(t, a, "", 1, "redact", "check")
	for _, want := range []string{"test FAIL: tests[0] host hidden (user)", "must_not_contain[0]", "must_contain[0]", "FAIL: 2 problem(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "corp-build") {
		t.Errorf("failure output echoes the needle:\n%s", out)
	}

	// Cases in a project file run too.
	writeFile(t, a.userPath(), "version: 1\n")
	writeFile(t, a.projectPath(), "version: 1\ntests:\n  - name: email\n    input: \"a bob@example.org\"\n    must_not_contain: [\"bob@\"]\n")
	out, _ = mustRun(t, a, "", 0, "redact", "check")
	if !strings.Contains(out, "1 embedded test(s) passed") {
		t.Errorf("out = %q", out)
	}
	writeFile(t, a.projectPath(), "version: 1\ntests:\n  - input: \"x\"\n    unknown: 1\n")
	mustRun(t, a, "", 1, "redact", "check")
}

func TestDoctorReportsRedactionConfig(t *testing.T) {
	a := newApp(t)
	out, _ := mustRun(t, a, "", 0, "doctor")
	for _, want := range []string{"config:        ok", "mode:          standard", "active layers: built-in\n", "(not present)"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}

	writeFile(t, a.userPath(), "version: 1\nrules:\n  - id: custom.a\n    pattern: 'aaaa-[0-9]+'\n")
	writeFile(t, a.projectPath(), "version: 1\nmode: strict\n")
	out, _ = mustRun(t, a, "", 0, "doctor")
	for _, want := range []string{"active layers: built-in, user, project", "mode:          strict", "1 custom", "(present)"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}

	writeFile(t, a.userPath(), "version: 1\nrules:\n  - id: custom.a\n    pattern: '('\n")
	out, _ = mustRun(t, a, "", 1, "doctor")
	for _, want := range []string{"INVALID", "rules[0].pattern", "mode:          unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}

	// A too-permissive user file is a config error, too.
	if runtime.GOOS != "windows" {
		writeFile(t, a.userPath(), "version: 1\n")
		if err := os.Chmod(a.userPath(), 0o644); err != nil {
			t.Fatal(err)
		}
		out, _ = mustRun(t, a, "", 1, "doctor")
		if !strings.Contains(out, "too permissive") {
			t.Errorf("doctor: %s", out)
		}
	}
}

func TestDispatch(t *testing.T) {
	a := newApp(t)
	out, _ := mustRun(t, a, "", 0, "version")
	if out != "jevkit test\n" {
		t.Errorf("version = %q", out)
	}
	mustRun(t, a, "", 2)
	mustRun(t, a, "", 2, "bogus")
	mustRun(t, a, "", 2, "redact")
	mustRun(t, a, "", 2, "redact", "bogus")
	mustRun(t, a, "", 0, "redact", "--help")
	mustRun(t, a, "", 0, "redact", "test", "-h")
}

func TestUnifiedDiff(t *testing.T) {
	if got := unifiedDiff("a", "b", "x\ny\n", "x\ny\n"); got != "" {
		t.Errorf("equal texts: %q", got)
	}
	from := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19\n20\n"
	to := strings.Replace(strings.Replace(from, "\n2\n", "\nTWO\n", 1), "\n18\n", "\nEIGHTEEN\n", 1)
	got := unifiedDiff("a", "b", from, to)
	want := "--- a\n+++ b\n@@ -1,5 +1,5 @@\n 1\n-2\n+TWO\n 3\n 4\n 5\n@@ -15,6 +15,6 @@\n 15\n 16\n 17\n-18\n+EIGHTEEN\n 19\n 20\n"
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	// Adjacent changes merge into one hunk, deletions before additions.
	got = unifiedDiff("a", "b", "a\nb\nc\n", "A\nB\nc\n")
	if got != "--- a\n+++ b\n@@ -1,3 +1,3 @@\n-a\n-b\n+A\n+B\n c\n" {
		t.Errorf("run diff: %q", got)
	}
}
