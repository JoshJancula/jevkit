package redact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/redact/audit"
)

var t0 = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

func seedAudit(t *testing.T, a *App) {
	t.Helper()
	l := &audit.Log{Path: a.AuditPath()}
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
	if got.Sends != 2 || got.BytesBefore != 300 || got.Agents["claude"] != 1 || got.Hits["builtin.email"] != 2 || got.Path != a.AuditPath() {
		t.Errorf("json summary: %+v", got)
	}

	// Duration in days, a date and an RFC 3339 time are all accepted.
	for since, want := range map[string]string{"30d": "sends: 3", "2026-09-10": "sends: 2", "2026-09-10T10:30:00Z": "sends: 1"} {
		if _, out, _ = run(a, "", "redact", "audit", "--since", since); !strings.Contains(out, want) {
			t.Errorf("--since %s: want %q in\n%s", since, want, out)
		}
	}
	for _, bad := range [][]string{{"--since", "yesterday"}, {"--format", "xml"}, {"extra"}} {
		if code, _, _ := run(a, "", append([]string{"redact", "audit"}, bad...)...); code != app.ExitUsage {
			t.Errorf("%v: code=%d, want usage", bad, code)
		}
	}
}

func TestLastShowsStoredPayloadsOnlyInReviewMode(t *testing.T) {
	a := newApp(t)
	a.Now = func() time.Time { return t0 }
	seed := func(payloads ...string) {
		rv := &audit.Review{Path: a.ReviewPath(), Enabled: true, Now: a.Now}
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
	if _, err := os.Stat(a.ReviewPath()); err == nil {
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
		if code, _, _ := run(a, "", "redact", "last", bad); code != app.ExitUsage {
			t.Errorf("last %s: code=%d", bad, code)
		}
	}

	// A user file can turn it on and shorten the TTL; entries past it vanish.
	a.Environ = a.Environ[:len(a.Environ)-1]
	testkit.WriteFile(t, a.UserPath(), "version: 1\nreview: true\nreview_ttl: 270s\n")
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

// TestAuditReviewFilesUsePrivatePermissions confirms the audit and review
// stores create their files and directories with the same 0700/0600 mode
// every other jevkit state package uses.
func TestAuditReviewFilesUsePrivatePermissions(t *testing.T) {
	a := &App{App: &app.App{StateDir: t.TempDir(), Now: func() time.Time { return t0 }}}
	seedAudit(t, a)
	rv := &audit.Review{Path: a.ReviewPath(), Enabled: true, Now: a.Now}
	if err := rv.Add(audit.Entry{Time: t0}); err != nil {
		t.Fatalf("review add: %v", err)
	}
	dir := filepath.Dir(a.AuditPath())
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o700 {
		t.Errorf("audit dir perm = %o, want 0700", perm)
	}
	for _, p := range []string{a.AuditPath(), a.ReviewPath()} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
			t.Errorf("%s perm = %o, want 0600", p, perm)
		}
	}
}
