package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/redact/audit"
)

var t0 = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

func seedAudit(t *testing.T, a *App) {
	t.Helper()
	l := &audit.Log{Path: a.auditPath()}
	for _, r := range []audit.Record{
		{Time: t0.Add(-10 * 24 * time.Hour), Agent: "claude", QuestionSet: "old", BytesBefore: 1000, BytesAfter: 900, Hits: map[string]int{"builtin.email": 9}},
		{Time: t0.Add(-2 * time.Hour), Agent: "claude", QuestionSet: "classify", BytesBefore: 200, BytesAfter: 150, Hits: map[string]int{"builtin.email": 2, "builtin.home-path": 1}},
		{Time: t0.Add(-1 * time.Hour), Agent: "codex", QuestionSet: "classify", BytesBefore: 100, BytesAfter: 100},
	} {
		if err := l.Append(r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditSummaryTextAndJSON(t *testing.T) {
	a := newApp(t)
	a.Now = func() time.Time { return t0 }

	code, out, _ := run(a, "", "redact", "audit")
	if code != 0 || !strings.Contains(out, "no sends recorded") {
		t.Fatalf("empty: code=%d %q", code, out)
	}

	seedAudit(t, a)
	code, out, errb := run(a, "", "redact", "audit")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	for _, want := range []string{"sends: 3", "1300 before redaction, 1150 after", "claude", "codex", "classify", "builtin.email", "11"} {
		if !strings.Contains(out, want) {
			t.Errorf("text summary missing %q:\n%s", want, out)
		}
	}

	code, out, _ = run(a, "", "redact", "audit", "--since", "24h", "--format", "json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got struct {
		Path        string         `json:"path"`
		Sends       int            `json:"sends"`
		BytesBefore int            `json:"bytes_before"`
		Agents      map[string]int `json:"agents"`
		Hits        map[string]int `json:"hits"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Sends != 2 || got.BytesBefore != 300 || got.Agents["claude"] != 1 || got.Hits["builtin.email"] != 2 || got.Path != a.auditPath() {
		t.Errorf("json summary: %+v", got)
	}

	// Duration in days, a date and an RFC 3339 time are all accepted.
	for since, want := range map[string]string{"30d": "sends: 3", "2026-09-10": "sends: 2", "2026-09-10T10:30:00Z": "sends: 1"} {
		if _, out, _ = run(a, "", "redact", "audit", "--since", since); !strings.Contains(out, want) {
			t.Errorf("--since %s: want %q in\n%s", since, want, out)
		}
	}
	for _, bad := range [][]string{{"--since", "yesterday"}, {"--format", "xml"}, {"extra"}} {
		if code, _, _ := run(a, "", append([]string{"redact", "audit"}, bad...)...); code != exitUsage {
			t.Errorf("%v: code=%d, want usage", bad, code)
		}
	}
}

func TestLastShowsStoredPayloadsOnlyInReviewMode(t *testing.T) {
	a := newApp(t)
	a.Now = func() time.Time { return t0 }
	seed := func(payloads ...string) {
		rv := &audit.Review{Path: a.reviewPath(), Enabled: true, Now: a.Now}
		for i, p := range payloads {
			if err := rv.Add(audit.Entry{Time: t0.Add(time.Duration(i-5) * time.Minute), Agent: "claude", QuestionSet: "q", Payload: p}); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("first payload", "second payload", "third payload")

	// Off by default: nothing is shown and stale payloads are purged.
	code, out, _ := run(a, "", "redact", "last")
	if code != 0 || !strings.Contains(out, "review mode is off") || strings.Contains(out, "third payload") {
		t.Fatalf("off: code=%d %q", code, out)
	}
	if _, err := os.Stat(a.reviewPath()); err == nil {
		t.Error("stale review file survived while review is off")
	}

	seed("first payload", "second payload", "third payload")
	a.Environ = append(a.Environ, "JEVKIT_REDACT_REVIEW=1")
	code, out, _ = run(a, "", "redact", "last")
	if code != 0 || !strings.Contains(out, "third payload") || strings.Contains(out, "second payload") {
		t.Fatalf("last: code=%d %q", code, out)
	}
	if _, out, _ = run(a, "", "redact", "last", "2"); !strings.Contains(out, "second payload") || !strings.Contains(out, "third payload") || strings.Contains(out, "first payload") {
		t.Errorf("last 2: %q", out)
	}
	if _, out, _ = run(a, "", "redact", "last", "50"); !strings.Contains(out, "first payload") || !strings.Contains(out, "claude/q") {
		t.Errorf("last 50: %q", out)
	}
	for _, bad := range []string{"0", "-1", "x"} {
		if code, _, _ := run(a, "", "redact", "last", bad); code != exitUsage {
			t.Errorf("last %s: code=%d", bad, code)
		}
	}

	// A user file can turn it on and shorten the TTL; entries past it vanish.
	a.Environ = a.Environ[:len(a.Environ)-1]
	writeFile(t, a.userPath(), "version: 1\nreview: true\nreview_ttl: 270s\n")
	if _, out, _ = run(a, "", "redact", "last", "9"); strings.Contains(out, "first payload") || !strings.Contains(out, "second payload") {
		t.Errorf("ttl: %q", out)
	}
	a.Now = func() time.Time { return t0.Add(time.Hour) }
	if _, out, _ = run(a, "", "redact", "last"); !strings.Contains(out, "no stored sends") {
		t.Errorf("all expired: %q", out)
	}
}

func TestParseSince(t *testing.T) {
	if got, err := parseSince("", t0); err != nil || !got.IsZero() {
		t.Errorf("empty: %v %v", got, err)
	}
	if got, _ := parseSince("90m", t0); !got.Equal(t0.Add(-90 * time.Minute)) {
		t.Errorf("90m: %v", got)
	}
	if got, _ := parseSince("2d", t0); !got.Equal(t0.AddDate(0, 0, -2)) {
		t.Errorf("2d: %v", got)
	}
	if _, err := parseSince("-5h", t0); err == nil {
		t.Error("negative duration accepted")
	}
}

func TestDefaultStateDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := defaultStateDir(env(map[string]string{"JEVKIT_STATE_DIR": "/s"}), "linux"); got != "/s" {
		t.Errorf("override: %q", got)
	}
	if got := defaultStateDir(env(map[string]string{"XDG_STATE_HOME": "/x"}), "linux"); got != "/x/jevkit" {
		t.Errorf("xdg: %q", got)
	}
	if got := defaultStateDir(env(map[string]string{"LOCALAPPDATA": "/l"}), "windows"); got != "/l/jevkit" {
		t.Errorf("windows: %q", got)
	}
}
