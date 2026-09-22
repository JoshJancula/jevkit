package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
)

const (
	entropyTok = "aZ3kQ9xP2mL7vB4nC8dF1gH5jR6tY0wSeU9iOpAqWlXcVbNm"
	openaiKey  = "sk-abcdefghijklmnopqrstuvwxyz"
)

func write(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func load(t *testing.T, user, project string, env ...string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	return Load(LoadOptions{UserPath: user, ProjectPath: project, SaltPath: filepath.Join(dir, "salt"), Environ: env})
}

func apply(t *testing.T, c *Config, text string) string {
	t.Helper()
	r, err := c.Redactor()
	if err != nil {
		t.Fatalf("Redactor: %v", err)
	}
	res, err := r.Apply(text)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res.Text
}

// wantConfigErr asserts a code-3 reject naming file and key.
func wantConfigErr(t *testing.T, err error, file, key string) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want error for %s key %q, got nil", file, key)
	}
	if jev.CodeOf(err) != jev.CodeRejected {
		t.Fatalf("code = %d, want %d (rejected): %v", jev.CodeOf(err), jev.CodeRejected, err)
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("no *config.Error in %v", err)
	}
	if ce.File != file || ce.Key != key {
		t.Fatalf("error names file %q key %q, want %q key %q (%v)", ce.File, ce.Key, file, key, err)
	}
	if !strings.Contains(err.Error(), file) {
		t.Fatalf("message does not name the file: %v", err)
	}
	return ce
}

func TestSchemaEmbedded(t *testing.T) {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(SchemaJSON(), &s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s.Properties["version"]), "1") {
		t.Fatalf("schema does not pin version 1: %s", s.Properties["version"])
	}
	if _, err := compiledSchema(); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFilesUseBuiltins(t *testing.T) {
	dir := t.TempDir()
	c, err := load(t, filepath.Join(dir, "nope.yaml"), filepath.Join(dir, "nope2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sources) != 0 {
		t.Fatalf("sources = %v", c.Sources)
	}
	out := apply(t, c, "mail bob@example.com "+openaiKey+" "+entropyTok)
	if strings.Contains(out, "bob@example.com") || strings.Contains(out, openaiKey) || strings.Contains(out, entropyTok) {
		t.Fatalf("built-ins did not redact: %s", out)
	}
	if m, ok := c.NeverSend.Match("cat .env.local"); !ok || m != "cat .env*" {
		t.Fatalf("built-in never_send missing: %q %v", m, ok)
	}
}

func TestLayeringAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	user := write(t, dir, "redact.yaml", `
version: 1
rules:
  - {id: corp.ticket, pattern: 'TICKET-[0-9]{4}'}
literals: [user-literal-one]
env_values: [INTERNAL_*]
allowlist:
  - {literal: allowed@example.com}
  - {rule: builtin.high-entropy, literal: `+entropyTok+`}
disable: [builtin.ipv4]
tuning: {path_handling: keep}
never_send: ["cat secrets*"]
`, 0o600)
	proj := write(t, dir, "ws/.jevkit/redact.yaml", `
version: 1
rules:
  - {id: proj.host, pattern: 'db-[a-z]+\.internal', replacement: '<host>'}
literals: [allowed@example.com, project-literal]
env_values: [PROJ_SECRETISH]
never_send: ["*/vault/*"]
`, 0o644)
	env := []string{"HOME=/home/tester", "INTERNAL_URL=corp-internal-hostname", "PROJ_SECRETISH=proj-env-value"}
	c, err := load(t, user, proj, env...)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sources) != 2 || c.Sources[0] != user || c.Sources[1] != proj {
		t.Fatalf("sources = %v", c.Sources)
	}

	text := strings.Join([]string{
		"t TICKET-1234",
		"h db-main.internal",
		"u user-literal-one p project-literal",
		"e corp-internal-hostname proj-env-value",
		"allow other@example.com and allowed@example.com",
		"tok " + entropyTok,
		"ip 10.1.2.3",
		"path /home/tester/src",
	}, "\n")
	out := apply(t, c, text)
	lines := strings.Split(out, "\n")
	if len(lines) != 8 {
		t.Fatalf("line count %d", len(lines))
	}
	for i, c := range []struct{ line, mustNot, must string }{
		{lines[0], "TICKET-1234", redact.Marker},
		{lines[1], "db-main.internal", "<host>"},
		{lines[2], "user-literal-one", ""},
		{lines[2], "project-literal", ""},
		{lines[3], "corp-internal-hostname", ""},
		{lines[3], "proj-env-value", ""},
		{lines[4], "other@example.com", ""},
		// Precedence: the project literal is a HARD addition, so it beats the
		// user's SOFT allowlist entry for the same value.
		{lines[4], "allowed@example.com", ""},
		// The user allowlist and tuning apply to SOFT rules.
		{lines[5], "", entropyTok},
		{lines[6], "", "10.1.2.3"},
		{lines[7], "", "/home/tester/src"},
	} {
		if c.mustNot != "" && strings.Contains(c.line, c.mustNot) {
			t.Errorf("case %d: %q survived in %q", i, c.mustNot, c.line)
		}
		if c.must != "" && !strings.Contains(c.line, c.must) {
			t.Errorf("case %d: %q missing from %q", i, c.must, c.line)
		}
	}
	for _, s := range []string{"cat .env", "cat secrets/x", "ls /repo/vault/key"} {
		if _, ok := c.NeverSend.Match(s); !ok {
			t.Errorf("never_send did not match %q", s)
		}
	}
}

func TestUserTuning(t *testing.T) {
	dir := t.TempDir()
	base, err := load(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out := apply(t, base, entropyTok); out != redact.Marker {
		t.Fatalf("default should redact, got %q", out)
	}
	user := write(t, dir, "a.yaml", "version: 1\ntuning: {entropy_threshold: 6}\n", 0o600)
	c, err := load(t, user, "")
	if err != nil {
		t.Fatal(err)
	}
	if out := apply(t, c, entropyTok); out != entropyTok {
		t.Fatalf("raised threshold should keep token, got %q", out)
	}
	user = write(t, dir, "b.yaml", "version: 1\ntuning: {min_token_length: 60}\n", 0o600)
	c, err = load(t, user, "")
	if err != nil {
		t.Fatal(err)
	}
	if out := apply(t, c, entropyTok); out != entropyTok {
		t.Fatalf("longer minimum should keep token, got %q", out)
	}
	user = write(t, dir, "c.yaml", "version: 1\ndisable: [builtin.email]\n", 0o600)
	c, err = load(t, user, "")
	if err != nil {
		t.Fatal(err)
	}
	if out := apply(t, c, "bob@example.com"); out != "bob@example.com" {
		t.Fatalf("disabled email rule still fired: %q", out)
	}
}

func TestProjectCannotLoosenAnything(t *testing.T) {
	for _, tc := range []struct{ name, body, key string }{
		{"disable", "disable: [builtin.email]", "disable"},
		{"allowlist", "allowlist: [{literal: bob@example.com}]", "allowlist"},
		{"tuning threshold", "tuning: {entropy_threshold: 2}", "tuning"},
		{"tuning min length", "tuning: {min_token_length: 256}", "tuning"},
		{"placeholder", "placeholder: label", "placeholder"},
		{"unknown", "frobnicate: 1", "frobnicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			proj := write(t, dir, ".jevkit/redact.yaml", "version: 1\n"+tc.body+"\n", 0o644)
			_, err := load(t, "", proj)
			_ = wantConfigErr(t, err, proj, tc.key)
		})
	}
}

func TestProjectCannotOverrideRule(t *testing.T) {
	dir := t.TempDir()
	user := write(t, dir, "u.yaml", "version: 1\nrules: [{id: corp.x, pattern: 'abc123'}]\n", 0o600)
	proj := write(t, dir, "p.yaml", "version: 1\nrules: [{id: corp.x, pattern: 'zzz'}]\n", 0o644)
	_, err := load(t, user, proj)
	_ = wantConfigErr(t, err, proj, "rules[0].id")

	proj = write(t, dir, "p2.yaml", "version: 1\nrules: [{id: builtin.email, pattern: 'zzzz'}]\n", 0o644)
	_, err = load(t, "", proj)
	_ = wantConfigErr(t, err, proj, "rules[0].id")
}

func TestHardRulesIgnoreAllowlistAndDisable(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, body, key string }{
		{"disable hard", "disable: [builtin.openai-key]", "disable[0]"},
		{"disable known secret", "disable: [builtin.known-secret]", "disable[0]"},
		{"disable pem", "disable: [builtin.private-key-block]", "disable[0]"},
		{"allowlist hard", "allowlist: [{rule: builtin.credential-assignment, regex: '.*'}]", "allowlist[0].rule"},
		{"disable unknown", "disable: [builtin.nope]", "disable[0]"},
		{"allowlist home", "allowlist: [{rule: builtin.home-path, literal: /home/x}]", "allowlist[0].rule"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := write(t, dir, "u.yaml", "version: 1\n"+tc.body+"\n", 0o600)
			_, err := load(t, user, "")
			_ = wantConfigErr(t, err, user, tc.key)
		})
	}

	// A wildcard allowlist and disabling every SOFT rule still leave the HARD
	// rules in force.
	user := write(t, dir, "wild.yaml", `
version: 1
allowlist:
  - {regex: '.*'}
disable: [builtin.email, builtin.ipv4, builtin.user-path, builtin.env-dump, builtin.high-entropy]
`, 0o600)
	c, err := load(t, user, "", "TYPESAFE_API_KEY=live-key-value-123")
	if err != nil {
		t.Fatal(err)
	}
	in := strings.Join([]string{
		"k " + openaiKey,
		"live-key-value-123",
		"password=hunter2hunter2",
		"Authorization: Bearer abcdefghijklmnop",
		"AKIAABCDEFGHIJKLMNOP",
		"-----BEGIN PRIVATE KEY-----",
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASC",
		"-----END PRIVATE KEY-----",
		"bob@example.com",
	}, "\n")
	out := apply(t, c, in)
	for _, leak := range []string{openaiKey, "live-key-value-123", "hunter2hunter2", "abcdefghijklmnop", "AKIAABCDEFGHIJKLMNOP", "MIIEvQIBADANBg"} {
		if strings.Contains(out, leak) {
			t.Errorf("HARD secret %q survived: %s", leak, out)
		}
	}
	if !strings.Contains(out, "bob@example.com") {
		t.Errorf("SOFT email should have been left alone: %s", out)
	}
}

func TestFailClosed(t *testing.T) {
	long := strings.Repeat("a", redact.MaxPatternLen+1)
	for _, tc := range []struct{ name, body, key string }{
		{"invalid regex", "version: 1\nrules: [{id: a.b, pattern: '(unclosed'}]", "rules[0].pattern"},
		{"lookahead is not RE2", "version: 1\nrules: [{id: a.b, pattern: '(?=x)y'}]", "rules[0].pattern"},
		{"oversize pattern", "version: 1\nrules: [{id: a.b, pattern: '" + long + "'}]", "rules[0].pattern"},
		{"empty match", "version: 1\nrules: [{id: a.b, pattern: 'x*'}]", "rules[0].pattern"},
		{"bad flags", "version: 1\nrules: [{id: a.b, pattern: 'x', flags: 'g'}]", "rules[0].flags"},
		{"bad id", "version: 1\nrules: [{id: 'Bad ID', pattern: 'xyz'}]", "rules[0].id"},
		{"newline replacement", "version: 1\nrules: [{id: a.b, pattern: 'xyz', replacement: \"a\\nb\"}]", "rules[0].replacement"},
		{"unknown top key", "version: 1\nsecrets: [x]", "secrets"},
		{"unknown nested key", "version: 1\nrules: [{id: a.b, pattern: xyz, extra: 1}]", "rules[0]"},
		{"unknown tuning key", "version: 1\ntuning: {nope: 1}", "tuning"},
		{"wrong version", "version: 2", "version"},
		{"missing version", "literals: [abcdefgh]", ""},
		{"string version", "version: '1'", "version"},
		{"short literal", "version: 1\nliterals: [ab]", "literals[0]"},
		{"bad env name", "version: 1\nenv_values: ['A B']", "env_values[0]"},
		{"threshold too low", "version: 1\ntuning: {entropy_threshold: 0.5}", "tuning.entropy_threshold"},
		{"bad placeholder", "version: 1\nplaceholder: fancy", "placeholder"},
		{"allowlist needs regex or literal", "version: 1\nallowlist: [{rule: builtin.email}]", "allowlist[0]"},
		{"allowlist bad regex", "version: 1\nallowlist: [{regex: '['}]", "allowlist[0].regex"},
		{"never_send matches all", "version: 1\nnever_send: ['*']", "never_send[0]"},
		{"invalid yaml", "version: 1\nrules: [", ""},
		{"not a mapping", "- a\n- b", ""},
		{"duplicate key", "version: 1\nversion: 1", ""},
		{"empty file", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, layer := range []struct {
				name string
				mode os.FileMode
			}{{"user.yaml", 0o600}, {"proj/.jevkit/redact.yaml", 0o644}} {
				if !strings.HasPrefix(layer.name, "user") && userOnlyKey(tc.key) {
					continue
				}
				p := write(t, dir, layer.name, tc.body+"\n", layer.mode)
				var err error
				if strings.HasPrefix(layer.name, "user") {
					_, err = load(t, p, "")
				} else {
					_, err = load(t, "", p)
				}
				if tc.key == "" {
					if err == nil || jev.CodeOf(err) != jev.CodeRejected {
						t.Fatalf("%s: want code-3 reject, got %v", layer.name, err)
					}
					var ce *Error
					if !errors.As(err, &ce) || ce.File != p {
						t.Fatalf("%s: error does not name the file: %v", layer.name, err)
					}
					continue
				}
				_ = wantConfigErr(t, err, p, tc.key)
			}
		})
	}
}

// userOnlyKey reports keys a project file cannot use at all (its error is the
// "not permitted" one, covered by TestProjectCannotLoosenAnything).
func userOnlyKey(key string) bool {
	for _, p := range []string{"tuning", "allowlist", "placeholder", "disable"} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func TestOversizeFile(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "big.yaml", "version: 1\n# "+strings.Repeat("x", maxFileBytes)+"\n", 0o600)
	_, err := load(t, p, "")
	_ = wantConfigErr(t, err, p, "")
}

func TestUserFilePermissions(t *testing.T) {
	if os.Getuid() == 0 {
		t.Log("running as root; mode checks still apply")
	}
	dir := t.TempDir()
	for _, mode := range []os.FileMode{0o644, 0o640, 0o660, 0o666, 0o602, 0o700} {
		p := write(t, dir, "u.yaml", "version: 1\n", mode)
		_, err := load(t, p, "")
		ce := wantConfigErr(t, err, p, "")
		if !strings.Contains(ce.Msg, "0600") || !strings.Contains(ce.Msg, "Refusing") {
			t.Errorf("mode %04o: message not clear: %s", mode, ce.Msg)
		}
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		p := write(t, dir, "ok.yaml", "version: 1\n", mode)
		if _, err := load(t, p, ""); err != nil {
			t.Errorf("mode %04o: %v", mode, err)
		}
	}
	// A cloned project file's mode is whatever git gave it; it is not refused.
	p := write(t, dir, "proj.yaml", "version: 1\n", 0o666)
	if _, err := load(t, "", p); err != nil {
		t.Errorf("project file with mode 0666: %v", err)
	}
}

func TestNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	_, err := load(t, dir, "")
	_ = wantConfigErr(t, err, dir, "")
}

func TestStablePlaceholders(t *testing.T) {
	dir := t.TempDir()
	user := write(t, dir, "u.yaml", "version: 1\nplaceholder: stable\nliterals: [super-secret-a, super-secret-b]\n", 0o600)
	load2 := func(saltFile string) *Config {
		t.Helper()
		c, err := Load(LoadOptions{UserPath: user, SaltPath: saltFile})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	saltA := write(t, dir, "saltA", strings.Repeat("11", 32)+"\n", 0o600)
	saltB := write(t, dir, "saltB", strings.Repeat("22", 32)+"\n", 0o600)
	text := "x super-secret-a y\nsuper-secret-a and super-secret-b\n" + openaiKey

	a1 := apply(t, load2(saltA), text)
	a2 := apply(t, load2(saltA), text)
	b1 := apply(t, load2(saltB), text)
	if a1 != a2 {
		t.Fatalf("not deterministic per salt:\n%s\n%s", a1, a2)
	}
	if a1 == b1 {
		t.Fatalf("different salts gave identical output: %s", a1)
	}
	tok := regexpTok(t, a1)
	if len(tok) < 3 || tok[0] != tok[1] {
		t.Fatalf("identical secrets must share a token: %v in %s", tok, a1)
	}
	if tok[0] == tok[2] {
		t.Fatalf("different secrets shared a token: %v", tok)
	}
	if !strings.Contains(a1, "[REDACTED:builtin.known-secret:") || !strings.Contains(a1, "[REDACTED:builtin.openai-key:") {
		t.Fatalf("stable format wrong: %s", a1)
	}
	for _, leak := range []string{"super-secret-a", "super-secret-b", openaiKey} {
		if strings.Contains(a1, leak) {
			t.Fatalf("%q leaked: %s", leak, a1)
		}
	}
}

func regexpTok(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	for {
		i := strings.Index(s, "[REDACTED:")
		if i < 0 {
			return out
		}
		j := strings.Index(s[i:], "]")
		out = append(out, s[i:i+j+1])
		s = s[i+j+1:]
	}
}

func TestLabelPlaceholder(t *testing.T) {
	dir := t.TempDir()
	user := write(t, dir, "u.yaml", "version: 1\nplaceholder: label\n", 0o600)
	c, err := load(t, user, "")
	if err != nil {
		t.Fatal(err)
	}
	out := apply(t, c, "key "+openaiKey+"\nbob@example.com")
	if out != "key [REDACTED:builtin.openai-key]\n[REDACTED:builtin.email]" {
		t.Fatalf("got %q", out)
	}
}

func TestSaltFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state", "salt")
	s1, err := LoadSalt(p)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("salt mode %04o, want 0600", fi.Mode().Perm())
	}
	s2, err := LoadSalt(p)
	if err != nil || string(s1) != string(s2) || len(s1) != saltBytes {
		t.Fatalf("salt not stable: %v", err)
	}
	other, err := LoadSalt(filepath.Join(dir, "other"))
	if err != nil || string(other) == string(s1) {
		t.Fatalf("salts should differ per install: %v", err)
	}

	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadSalt(p)
	_ = wantConfigErr(t, err, p, "")

	bad := write(t, dir, "bad", "not-hex\n", 0o600)
	_, err = LoadSalt(bad)
	_ = wantConfigErr(t, err, bad, "")

	_, err = LoadSalt("")
	if jev.CodeOf(err) != jev.CodeRejected {
		t.Fatalf("empty salt path should reject: %v", err)
	}
}

func TestStableWithoutSaltFailsClosed(t *testing.T) {
	dir := t.TempDir()
	user := write(t, dir, "u.yaml", "version: 1\nplaceholder: stable\n", 0o600)
	_, err := Load(LoadOptions{UserPath: user})
	if jev.CodeOf(err) != jev.CodeRejected {
		t.Fatalf("want code-3 reject, got %v", err)
	}
}

func TestEnvValuesGlob(t *testing.T) {
	dir := t.TempDir()
	user := write(t, dir, "u.yaml", "version: 1\nenv_values: ['*_DSN', EXACT_NAME]\n", 0o600)
	c, err := load(t, user, "", "DB_DSN=postgres-dsn-value", "EXACT_NAME=exact-value-1", "OTHER=untouched-value", "TINY_DSN=x")
	if err != nil {
		t.Fatal(err)
	}
	out := apply(t, c, "postgres-dsn-value exact-value-1 untouched-value x")
	if strings.Contains(out, "postgres-dsn-value") || strings.Contains(out, "exact-value-1") {
		t.Fatalf("env values survived: %s", out)
	}
	if !strings.Contains(out, "untouched-value") {
		t.Fatalf("unrelated env value redacted: %s", out)
	}
}

func TestNeverSendMatch(t *testing.T) {
	n, err := NewNeverSend([]string{"cat .env*", "*/secrets/*", "kubectl get secret*", "id_rsa"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"cat .env", "cat .env.production", "cd app && cat .env", "true; kubectl get secrets -o yaml",
		"/srv/app/secrets/db.json", "app/secrets/db.json", "kubectl get secret foo", "id_rsa",
	} {
		if _, ok := n.Match(s); !ok {
			t.Errorf("should match %q", s)
		}
	}
	for _, s := range []string{"cat README.md", "ls secrets", "kubectl get pods", "echo cat .env", "id_rsa.pub"} {
		if p, ok := n.Match(s); ok && s != "echo cat .env" {
			t.Errorf("should not match %q (pattern %q)", s, p)
		}
	}
	var none *NeverSend
	if _, ok := none.Match("cat .env"); ok {
		t.Error("nil matcher matched")
	}
	if _, err := NewNeverSend([]string{""}); err == nil {
		t.Error("empty pattern accepted")
	}
}

func TestModeStrict(t *testing.T) {
	const sha = "sha 3f786850e387550fdab836ed7e6dc881de23001b"
	dir := t.TempDir()
	strict := write(t, dir, "u/redact.yaml", "version: 1\nmode: strict\n", 0o600)
	standard := write(t, dir, "u2/redact.yaml", "version: 1\nmode: standard\n", 0o600)
	projStrict := write(t, dir, "p/.jevkit/redact.yaml", "version: 1\nmode: strict\n", 0o644)
	projStd := write(t, dir, "p2/.jevkit/redact.yaml", "version: 1\nmode: standard\n", 0o644)

	c, err := load(t, standard, "")
	if err != nil || apply(t, c, sha) != sha {
		t.Fatalf("standard changed sha: %v", err)
	}
	for name, paths := range map[string][2]string{"user": {strict, ""}, "project": {"", projStrict}, "both": {standard, projStrict}} {
		c, err := load(t, paths[0], paths[1])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := apply(t, c, sha); !strings.HasSuffix(got, "[REDACTED]") {
			t.Errorf("%s strict kept sha: %q", name, got)
		}
	}
	// A project file cannot loosen to standard, even over a strict user file.
	_, err = load(t, strict, projStd)
	_ = wantConfigErr(t, err, projStd, "mode")
	// Strict conflicts with anything that loosens SOFT rules; project strict
	// over a user disable fails closed naming the project file.
	dis := write(t, dir, "u3/redact.yaml", "version: 1\ndisable: [builtin.email]\n", 0o600)
	_, err = load(t, dis, projStrict)
	_ = wantConfigErr(t, err, projStrict, "mode")
	keep := write(t, dir, "u4/redact.yaml", "version: 1\nmode: strict\ntuning: {path_handling: keep}\n", 0o600)
	_, err = load(t, keep, "")
	_ = wantConfigErr(t, err, keep, "mode")
	bad := write(t, dir, "u5/redact.yaml", "version: 1\nmode: lax\n", 0o600)
	_, err = load(t, bad, "")
	_ = wantConfigErr(t, err, bad, "mode")
}

func TestGateNeverSendShortCircuits(t *testing.T) {
	c, err := load(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	send := func(_ context.Context, s string) (string, error) {
		calls++
		return "ok:" + s, nil
	}
	fail := func(context.Context, string) (string, error) {
		t.Fatal("jev client called for a never_send subject")
		return "", nil
	}
	for _, subj := range []string{"cat .env", "cat .env.local", "cd x && cat .env", "/repo/secrets/a.txt", "kubectl get secret db -o yaml"} {
		out, pat, sent, err := c.Gate(context.Background(), subj, "password=hunter2", fail)
		if err != nil || sent || out != "" || pat == "" {
			t.Errorf("%q: out=%q pat=%q sent=%v err=%v", subj, out, pat, sent, err)
		}
	}
	out, pat, sent, err := c.Gate(context.Background(), "ls -la", "password=hunter2 user@example.com", send)
	if err != nil || !sent || pat != "" || calls != 1 {
		t.Fatalf("normal subject: out=%q pat=%q sent=%v calls=%d err=%v", out, pat, sent, calls, err)
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, "user@example.com") {
		t.Errorf("unredacted text reached send: %q", out)
	}
}

func TestReviewAndConfirmSettings(t *testing.T) {
	dir := t.TempDir()
	c, err := load(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Review || c.Confirm || c.ReviewMax != DefaultReviewMax || c.ReviewTTL != DefaultReviewTTL {
		t.Errorf("defaults: %+v", c)
	}
	u := write(t, dir, "u/redact.yaml", "version: 1\nreview: true\nconfirm: true\nreview_max: 5\nreview_ttl: 90m\n", 0o600)
	if c, err = load(t, u, ""); err != nil {
		t.Fatal(err)
	}
	if !c.Review || !c.Confirm || c.ReviewMax != 5 || c.ReviewTTL != 90*time.Minute {
		t.Errorf("file settings: %+v", c)
	}
	// The environment overrides the file, both ways.
	if c, err = load(t, u, "", "JEVKIT_REDACT_REVIEW=0", "JEVKIT_REDACT_CONFIRM=false"); err != nil || c.Review || c.Confirm {
		t.Errorf("env off: %+v err=%v", c, err)
	}
	if c, err = load(t, "", "", "JEVKIT_REDACT_REVIEW=1"); err != nil || !c.Review {
		t.Errorf("env on: %+v err=%v", c, err)
	}
	if c, err = load(t, "", "", "JEVKIT_REDACT_REVIEW=maybe"); err != nil || c.Review {
		t.Errorf("unrecognised env value must leave review off: %+v err=%v", c, err)
	}
	for key, body := range map[string]string{
		"review_ttl": "version: 1\nreview_ttl: soon\n",
		"review_max": "version: 1\nreview_max: 0\n",
		"review":     "version: 1\nreview: yes-please\n",
	} {
		f := write(t, dir, "bad-"+key+"/redact.yaml", body, 0o600)
		_, err := load(t, f, "")
		_ = wantConfigErr(t, err, f, key)
	}
	long := write(t, dir, "long/redact.yaml", "version: 1\nreview_ttl: 9999h\n", 0o600)
	_, err = load(t, long, "")
	_ = wantConfigErr(t, err, long, "review_ttl")
	// An untrusted project file cannot switch storage on or confirm off.
	for _, k := range []string{"review: true", "confirm: false", "review_max: 5", "review_ttl: 1h"} {
		p := write(t, dir, "p-"+k+"/.jevkit/redact.yaml", "version: 1\n"+k+"\n", 0o644)
		_, err := load(t, "", p)
		_ = wantConfigErr(t, err, p, strings.Split(k, ":")[0])
	}
}
