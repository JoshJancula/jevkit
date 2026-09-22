package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/redact/config"
)

const redactionDoc = "../../docs/REDACTION.md"

// fence is one fenced code block of the doc.
type fence struct {
	info string
	body string
	line int
}

func fences(t *testing.T) []fence {
	t.Helper()
	data, err := os.ReadFile(redactionDoc)
	if err != nil {
		t.Fatal(err)
	}
	var out []fence
	var cur *fence
	for i, l := range strings.Split(string(data), "\n") {
		switch {
		case cur == nil && strings.HasPrefix(l, "```"):
			cur = &fence{info: strings.TrimSpace(strings.TrimPrefix(l, "```")), line: i + 1}
		case cur != nil && strings.HasPrefix(l, "```"):
			out = append(out, *cur)
			cur = nil
		case cur != nil:
			cur.body += l + "\n"
		}
	}
	if cur != nil {
		t.Fatalf("unterminated code fence at line %d", cur.line)
	}
	return out
}

// TestRedactionDocYAMLValid checks that every yaml fence parses and passes
// schema and semantic validation. A fence tagged "yaml project" is checked as
// an untrusted project file.
func TestRedactionDocYAMLValid(t *testing.T) {
	n := 0
	for _, f := range fences(t) {
		var trusted bool
		switch f.info {
		case "yaml":
			trusted = true
		case "yaml project":
		default:
			continue
		}
		n++
		if err := config.ValidateFile("REDACTION.md", []byte(f.body), trusted); err != nil {
			t.Errorf("yaml fence at line %d: %v\n%s", f.line, err, f.body)
		}
	}
	if n < 10 {
		t.Errorf("only %d yaml fences found; the doc should show every key", n)
	}
}

// TestRedactionDocSubcommandsExist checks every `jevkit redact <sub>` the doc
// mentions is dispatched, and that the four tuning commands are documented.
func TestRedactionDocSubcommandsExist(t *testing.T) {
	data, err := os.ReadFile(redactionDoc)
	if err != nil {
		t.Fatal(err)
	}
	subs := map[string]bool{}
	for _, m := range regexp.MustCompile("jevkit redact ([a-z]+)").FindAllStringSubmatch(string(data), -1) {
		subs[m[1]] = true
	}
	for _, want := range []string{"test", "check", "audit", "last", "init", "list", "explain", "add"} {
		if !subs[want] {
			t.Errorf("doc never mentions `jevkit redact %s`", want)
		}
	}
	for sub := range subs {
		a := newApp(t)
		_, stdout, stderr := run(a, "", "redact", sub, "-h")
		if strings.Contains(stdout+stderr, "unknown subcommand") {
			t.Errorf("doc mentions `jevkit redact %s`, which does not exist", sub)
		}
		if !strings.Contains(redactUsage, "\n  "+sub+" ") {
			t.Errorf("`jevkit redact %s` is missing from the usage text", sub)
		}
	}
}
