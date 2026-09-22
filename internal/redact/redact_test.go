package redact

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
)

func mustNew(t testing.TB, o Options) *Redactor {
	t.Helper()
	r, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestApply(t *testing.T) {
	const home = "/tmp/jev-redact-home-fixture"
	const key = "typesafe-secret-xyz-999"
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEabc\ndefGHI\n-----END RSA PRIVATE KEY-----"

	tests := []struct {
		name      string
		in        string
		wantHave  []string
		wantNot   []string
		wantRules []string
	}{
		{"env-dump value", "FOO_BAR_XYZ=supersecretvalue99",
			[]string{"FOO_BAR_XYZ=[REDACTED]"}, []string{"supersecretvalue99"}, []string{RuleEnvDump}},
		{"bearer credential assignment", "bearer: my-bearer-secret-token99",
			[]string{"[REDACTED]"}, []string{"my-bearer-secret-token99"}, []string{RuleCredentialAsg}},
		{"sk- key", "key=sk-abcdefghijklmnopqrstuvwxyz",
			[]string{"key=[REDACTED]"}, []string{"sk-abcdefghijklmnopqrstuvwxyz"}, []string{RuleOpenAIKey}},
		{"home path", "path=" + home + "/project/file.txt",
			[]string{"~/project/file.txt"}, []string{home}, []string{RuleHomePath}},
		{"live key", "header " + key + " trailing",
			[]string{"header [REDACTED] trailing"}, []string{key}, []string{RuleKnownSecret}},
		{"ordinary signal text preserved", "ok JEV_DISTINCTIVE_SIGNAL_TOKEN_abc123 ready",
			[]string{"ok JEV_DISTINCTIVE_SIGNAL_TOKEN_abc123 ready"}, nil, nil},
		{"aws access key", "id AKIAIOSFODNN7EXAMPLE end",
			[]string{"id [REDACTED] end"}, []string{"AKIAIOSFODNN7EXAMPLE"}, []string{RuleAWSAccessKey}},
		{"github token", "t ghp_abcdefghijklmnopqrstuvwxyz0123 x",
			[]string{"t [REDACTED] x"}, []string{"ghp_abcdefghijklmnopqrstuvwxyz0123"}, []string{RuleGitHubToken}},
		{"slack token", "xoxb-1234567890-abcdefghij",
			[]string{"[REDACTED]"}, []string{"xoxb-1234567890"}, []string{RuleSlackToken}},
		{"authorization header", "Authorization: Bearer abcdef123456",
			[]string{"[REDACTED]"}, []string{"abcdef123456"}, []string{RuleAuthHeader}},
		{"bearer in prose", "curl -H 'x: Bearer abcdef123456'",
			nil, []string{"abcdef123456"}, []string{RuleBearerToken}},
		{"credential assignment", "password=hunter2 and secret: s3cr3t",
			[]string{"[REDACTED] and [REDACTED]"}, []string{"hunter2", "s3cr3t"}, []string{RuleCredentialAsg}},
		{"user path", "see /Users/alice/code/x.go", []string{"see ~/code/x.go"}, []string{"alice"}, []string{RuleUserPath}},
		{"email", "mail bob@example.com now", []string{"mail [REDACTED] now"}, []string{"bob@example.com"}, []string{RuleEmail}},
		{"ipv4", "host 10.1.2.3 up", []string{"host [REDACTED] up"}, []string{"10.1.2.3"}, []string{RuleIPv4}},
		{"high entropy", "blob q8Zx3Kp0Lm9Wv2Rt7Yb4Nc6Hd1Fj5Gs8Ta3Ue0Io2Pq9 end",
			[]string{"blob [REDACTED] end"}, []string{"q8Zx3Kp0"}, []string{RuleHighEntropy}},
		{"git sha preserved", "commit 3f786850e387550fdab836ed7e6dc881de23001b",
			[]string{"3f786850e387550fdab836ed7e6dc881de23001b"}, nil, nil},
		{"private key block", "a\n" + pem + "\nb",
			[]string{"a\n[REDACTED]\n[REDACTED]\n[REDACTED]\n[REDACTED]\nb"}, []string{"MIIEabc", "defGHI"}, []string{RulePrivateKey}},
		{"unterminated private key fails closed", "x\n-----BEGIN PRIVATE KEY-----\nAAAA\nBBBB",
			[]string{"x\n[REDACTED]\n[REDACTED]\n[REDACTED]"}, []string{"AAAA", "BBBB"}, []string{RulePrivateKey}},
		{"private key on one line", "k -----BEGIN PRIVATE KEY-----AAA-----END PRIVATE KEY----- z",
			[]string{"k [REDACTED] z"}, []string{"AAA"}, []string{RulePrivateKey}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mustNew(t, Options{Key: key, Home: home})
			res, err := r.Apply(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tc.wantHave {
				if !strings.Contains(res.Text, s) {
					t.Errorf("output %q missing %q", res.Text, s)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(res.Text, s) {
					t.Errorf("output %q leaks %q", res.Text, s)
				}
			}
			fired := map[string]bool{}
			for _, h := range res.Hits {
				fired[h.RuleID] = true
			}
			for _, w := range tc.wantRules {
				if !fired[w] {
					t.Errorf("hits %+v missing rule %s", res.Hits, w)
				}
			}
			if len(tc.wantRules) == 0 && len(res.Hits) != 0 {
				t.Errorf("unexpected hits %+v", res.Hits)
			}
			if strings.Count(res.Text, "\n") != strings.Count(tc.in, "\n") {
				t.Errorf("line count changed: %q", res.Text)
			}
		})
	}
}

func TestLineCountPreserved(t *testing.T) {
	res, err := mustNew(t, Options{}).Apply("line-one\nline-two\nline-three")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(res.Text, "\n"); got != 2 {
		t.Fatalf("newlines = %d, want 2", got)
	}
}

func TestMultiLineSecretRedactedPerLine(t *testing.T) {
	r := mustNew(t, Options{Key: "first-line-secret\nsecond-line-secret"})
	res, err := r.Apply("a first-line-secret\nb second-line-secret\nc")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "a [REDACTED]\nb [REDACTED]\nc" {
		t.Fatalf("got %q", res.Text)
	}
}

func TestHitsNeverCarryContent(t *testing.T) {
	const secret = "supersecretvalue99"
	res, err := mustNew(t, Options{}).Apply("FOO_BAR=" + secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", res.Hits), secret) || len(res.Hits) == 0 {
		t.Fatalf("hits = %+v", res.Hits)
	}
}

func TestHardRulesCannotBeTuned(t *testing.T) {
	for _, r := range Rules() {
		if r.Class != Hard {
			continue
		}
		if _, err := New(Options{DisableSoft: []string{r.ID}}); err == nil {
			t.Errorf("disabling %s succeeded", r.ID)
		}
		if _, err := New(Options{Allow: map[string][]string{r.ID: {".*"}}}); err == nil {
			t.Errorf("allowlisting %s succeeded", r.ID)
		}
	}
	if _, err := New(Options{DisableSoft: []string{"builtin.nope"}}); err == nil {
		t.Error("unknown rule accepted")
	}
	if _, err := New(Options{Allow: map[string][]string{RuleEmail: {"("}}}); err == nil {
		t.Error("bad allow pattern accepted")
	}
}

func TestSoftRulesTunable(t *testing.T) {
	r := mustNew(t, Options{
		DisableSoft: []string{RuleIPv4},
		Allow:       map[string][]string{RuleEmail: {`@example\.com$`}},
	})
	res, err := r.Apply("10.0.0.1 ok@example.com bad@other.org")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "10.0.0.1 ok@example.com [REDACTED]" {
		t.Fatalf("got %q", res.Text)
	}
}

func TestHardRuleIgnoresSoftAllowlist(t *testing.T) {
	r := mustNew(t, Options{Allow: map[string][]string{RuleEnvDump: {`.*`}}})
	res, err := r.Apply("password=hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "hunter2") {
		t.Fatal("hard rule was allowlisted")
	}
}

func TestOptionsFromEnv(t *testing.T) {
	o := OptionsFromEnv([]string{"HOME=/h/me", "TYPESAFE_API_KEY=kkkkkkkkkk", "MY_TOKEN=tttttttttt", "PATH=/bin", "SHORT_KEY=abc", "EMPTY_KEY="})
	if o.Home != "/h/me" || o.Key != "kkkkkkkkkk" || len(o.Secrets) != 1 || o.Secrets[0] != "tttttttttt" {
		t.Fatalf("%+v", o)
	}
}

func assertRejected(t *testing.T, err error) {
	t.Helper()
	var je *jev.Error
	if !errors.As(err, &je) || je.Code != jev.CodeRejected || jev.CodeOf(err) != jev.CodeRejected {
		t.Fatalf("want code-3 reject error, got %v", err)
	}
}

func TestFailingRuleRejects(t *testing.T) {
	r := mustNew(t, Options{})
	r.rules = append(r.rules, rule{id: "test.boom", class: Soft, line: func(string) (string, int, error) {
		return "", 0, errors.New("boom")
	}})
	res, err := r.Apply("should-not-leak")
	assertRejected(t, err)
	if res.Text != "" {
		t.Fatalf("text returned on failure: %q", res.Text)
	}
}

func TestPanickingRuleRejects(t *testing.T) {
	r := mustNew(t, Options{})
	r.rules = append(r.rules, rule{id: "test.panic", line: func(s string) (string, int, error) { panic("leak " + s) }})
	res, err := r.Apply("secret-input")
	assertRejected(t, err)
	if res.Text != "" || strings.Contains(err.Error(), "secret-input") {
		t.Fatalf("leaked on panic: %v %q", err, res.Text)
	}
}

func TestVerificationFailureRejects(t *testing.T) {
	r := mustNew(t, Options{})
	r.verifyHook = func(string) error { return errors.New("forced") }
	res, err := r.Apply("hello")
	assertRejected(t, err)
	if res.Text != "" {
		t.Fatalf("text returned on failure: %q", res.Text)
	}
}

func TestSurvivingSecretRejects(t *testing.T) {
	r := mustNew(t, Options{Key: "abcdefghij"})
	// A rule that re-introduces the key after the known-secret rule ran.
	r.rules = append(r.rules, rule{id: "test.reintroduce", line: func(s string) (string, int, error) {
		return s + " abcdefghij", 1, nil
	}})
	_, err := r.Apply("x")
	assertRejected(t, err)
}

func TestNewlineChangeRejects(t *testing.T) {
	r := mustNew(t, Options{})
	r.rules = append(r.rules, rule{id: "test.newline", line: func(s string) (string, int, error) {
		return s + "\n", 1, nil
	}})
	_, err := r.Apply("x")
	assertRejected(t, err)
}

// seedCheck is the shared property: either redaction fails closed, or the
// output holds no seeded secret and keeps the line count.
func seedCheck(t *testing.T, r *Redactor, secrets []string, text string) {
	t.Helper()
	res, err := r.Apply(text)
	if err != nil {
		assertRejected(t, err)
		return
	}
	for _, s := range secrets {
		if strings.Contains(res.Text, s) {
			t.Fatalf("secret %q survived in %q (input %q)", s, res.Text, text)
		}
	}
	if strings.Count(res.Text, "\n") != strings.Count(text, "\n") {
		t.Fatalf("line count changed: %q -> %q", text, res.Text)
	}
}

func FuzzRedactNeverLeaksSeed(f *testing.F) {
	secrets := []string{"s3cr3tVALUE-Zx9", "another-Seed_77"}
	r := mustNewFuzz(secrets)
	for _, seed := range []string{"", "a\nb", "x s3cr3tVALUE-Zx9 y", "k=another-Seed_77\nz", "-----BEGIN PRIVATE KEY-----\nAA\n", "password=abc", "\n\n"} {
		f.Add(seed, "pre\n", "\npost")
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		// Embed both seeded secrets among arbitrary text and newlines.
		text := a + secrets[0] + b + secrets[1] + c
		seedCheck(t, r, secrets, text)
	})
}

func mustNewFuzz(secrets []string) *Redactor {
	r, err := New(Options{Key: secrets[0], Secrets: secrets[1:], Home: "/home/fuzz"})
	if err != nil {
		panic(err)
	}
	return r
}

func TestPropertySeededSecrets(t *testing.T) {
	secrets := []string{"s3cr3tVALUE-Zx9", "another-Seed_77"}
	r := mustNewFuzz(secrets)
	fillers := []string{"", " ", "\n", "x=", "FOO=", "token: ", "Bearer ", "/home/fuzz/", "\r\n", "-----BEGIN PRIVATE KEY-----\n", "a@b.co ", "1.2.3.4 "}
	for _, a := range fillers {
		for _, b := range fillers {
			for _, c := range fillers {
				seedCheck(t, r, secrets, a+secrets[0]+b+secrets[1]+c)
			}
		}
	}
}
