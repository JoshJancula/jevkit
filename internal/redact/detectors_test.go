package redact

import (
	"strings"
	"testing"
)

const (
	sha40   = "3f786850e387550fdab836ed7e6dc881de23001b"
	tokMix  = "aZ3kQ9xP2mL7vB4nC8dF1gH5jR6tY0wSeU9iOpAqWlXcVbNm"
	shortHi = "aZ3kQ9xP2mL7vB4nC8dF1gH5j"
)

func applyText(t *testing.T, o Options, in string) string {
	t.Helper()
	res, err := mustNew(t, o).Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	return res.Text
}

func TestDetectors(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		in   string
		want string // "" means unchanged
	}{
		{"entropy redacts random token", Options{}, "x " + tokMix, "x [REDACTED]"},
		{"entropy keeps git sha", Options{}, "commit " + sha40, ""},
		{"entropy keeps short token", Options{}, "id " + shortHi, ""},
		{"entropy keeps prose word", Options{}, strings.Repeat("abcdefgh", 8), ""},
		{"entropy min length lowered", Options{MinTokenLen: 16}, shortHi, "[REDACTED]"},
		{"entropy threshold lowered", Options{EntropyThreshold: 3.0}, "sha " + sha40, "sha [REDACTED]"},
		{"entropy allowlist false positive", Options{Allow: map[string][]string{RuleHighEntropy: {`^aZ3k`}}}, tokMix, ""},
		{"email redacted", Options{}, "a@b.io", "[REDACTED]"},
		{"email allowlist false positive", Options{Allow: map[string][]string{RuleEmail: {`@b\.io$`}}}, "a@b.io", ""},
		{"ipv4 redacted", Options{}, "at 10.1.2.3", "at [REDACTED]"},
		{"ipv4 allowlist false positive", Options{Allow: map[string][]string{RuleIPv4: {`^127\.`}}}, "127.0.0.1", ""},
		{"env dump redacted", Options{}, "FOO=bar", "FOO=[REDACTED]"},
		{"env dump allowlist false positive", Options{Allow: map[string][]string{RuleEnvDump: {`^FOO=bar$`}}}, "FOO=bar", ""},
		{"user path rewritten", Options{}, "/Users/zed/x", "~/x"},
		{"user path allowlist", Options{Allow: map[string][]string{RuleUserPath: {`/Users/shared`}}}, "/Users/shared/x", ""},
		{"custom rule with label", Options{Custom: []CustomRule{{ID: "corp.ticket", Pattern: `TKT-[0-9]{4}`}}, Placeholder: LabelPlaceholder}, "see TKT-1234", "see [REDACTED:corp.ticket]"},
		{"custom rule replacement", Options{Custom: []CustomRule{{ID: "corp.ticket", Pattern: `TKT-[0-9]{4}`, Replacement: "<ticket>"}}}, "see TKT-1234", "see <ticket>"},
		{"custom rule ignores soft allowlist", Options{Custom: []CustomRule{{ID: "corp.ticket", Pattern: `TKT-[0-9]{4}`}}, Allow: map[string][]string{RuleEmail: {`.*`}}}, "TKT-1234", "[REDACTED]"},
		{"literal secret with placeholder", Options{Secrets: []string{"hunter2xyz"}, Placeholder: LabelPlaceholder}, "pw hunter2xyz", "pw [REDACTED:builtin.known-secret]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			if got := applyText(t, tt.opts, tt.in); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestStrictVsStandard(t *testing.T) {
	// A hex digest survives standard mode and is redacted in strict mode.
	if got := applyText(t, Options{}, "sha "+sha40); got != "sha "+sha40 {
		t.Fatalf("standard changed sha: %q", got)
	}
	if got := applyText(t, Options{Strict: true}, "sha "+sha40); got != "sha [REDACTED]" {
		t.Fatalf("strict kept sha: %q", got)
	}
	// A shorter token is only caught in strict mode.
	if got := applyText(t, Options{}, shortHi); got != shortHi {
		t.Fatalf("standard changed short token: %q", got)
	}
	if got := applyText(t, Options{Strict: true}, shortHi); got != "[REDACTED]" {
		t.Fatalf("strict kept short token: %q", got)
	}
	// Strict never raises a looser user tuning.
	if got := applyText(t, Options{Strict: true, EntropyThreshold: 5.5, MinTokenLen: 200}, "sha "+sha40); got != "sha [REDACTED]" {
		t.Fatalf("strict honoured loosened tuning: %q", got)
	}
	// Strict cannot be combined with anything that loosens SOFT rules.
	for name, o := range map[string]Options{
		"disable":   {Strict: true, DisableSoft: []string{RuleEmail}},
		"allowlist": {Strict: true, Allow: map[string][]string{RuleEmail: {`.*`}}},
	} {
		if _, err := New(o); err == nil {
			t.Errorf("strict + %s accepted", name)
		}
	}
}

func TestEnvValueRedactionSeededEnvironment(t *testing.T) {
	env := []string{
		"HOME=/home/zed",
		"API_KEY=k-1234567890abcd",
		"GITHUB_TOKEN=tok_abcdefghijkl",
		"DB_SECRET=s3cr3t-value-xyz",
		"ADMIN_PASSWORD=pa55w0rd-long",
		"AWS_CREDENTIALS=cred-abcdefgh12",
		"EDITOR=vimvimvimvim",
		"SHORT_TOKEN=abc",
	}
	r := mustNew(t, OptionsFromEnv(env))
	for _, v := range []string{"k-1234567890abcd", "tok_abcdefghijkl", "s3cr3t-value-xyz", "pa55w0rd-long", "cred-abcdefgh12"} {
		res, err := r.Apply("out: " + v + " end")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Text, v) {
			t.Errorf("env value %q survived: %q", v, res.Text)
		}
	}
	res, err := r.Apply("editor vimvimvimvim abc")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "editor vimvimvimvim abc" {
		t.Errorf("non-secret env value changed: %q", res.Text)
	}
}
