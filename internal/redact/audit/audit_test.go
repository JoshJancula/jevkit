package audit_test

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

	"github.com/OWNER/jevkit/internal/redact/audit"
	"github.com/OWNER/jevkit/internal/redact/config"
)

const (
	secret = "hunter2-SEEDED-secret-9271"
	email  = "seeded.person@example.com"
)

func loadCfg(t *testing.T, environ ...string) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func okSend(sent *[]string) config.Send {
	return func(_ context.Context, s string) (string, error) {
		*sent = append(*sent, s)
		return "answer", nil
	}
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestAuditLineHasRuleIDsAndCountsButNoContent(t *testing.T) {
	dir := t.TempDir()
	tx := &audit.Transparency{Log: &audit.Log{Path: filepath.Join(dir, "state", audit.FileName)}}
	var sent []string
	text := "password=" + secret + "\ncontact " + email + " and " + email + "\n"
	_, _, ok, err := tx.Gate(context.Background(), loadCfg(t), audit.Meta{Agent: "claude", QuestionSet: "classify"}, "ls", text, okSend(&sent))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	raw, err := os.ReadFile(tx.Log.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{secret, email, "seeded.person", "hunter2"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("audit log contains %q: %s", leak, raw)
		}
	}
	for _, want := range []string{`"agent":"claude"`, `"question_set":"classify"`, `"builtin.email":2`, `"builtin.credential-assignment"`, `"bytes_before":`, `"bytes_after":`} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("audit line missing %s: %s", want, raw)
		}
	}
	recs, _, err := audit.Read(tx.Log.Path, time.Time{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("recs=%v err=%v", recs, err)
	}
	if recs[0].BytesBefore != len(text) || recs[0].BytesAfter != len(sent[0]) {
		t.Errorf("bytes %d/%d, want %d/%d", recs[0].BytesBefore, recs[0].BytesAfter, len(text), len(sent[0]))
	}
	if runtime.GOOS != "windows" && perm(t, tx.Log.Path) != 0o600 {
		t.Errorf("audit log mode %v", perm(t, tx.Log.Path))
	}
}

func TestAuditLabelsCannotCarryText(t *testing.T) {
	l := &audit.Log{Path: filepath.Join(t.TempDir(), audit.FileName)}
	if err := l.Append(audit.Record{Time: time.Now(), Agent: "a b\n" + secret + strings.Repeat("x", 200), QuestionSet: "q\"}"}); err != nil {
		t.Fatal(err)
	}
	recs, skipped, err := audit.Read(l.Path, time.Time{})
	if err != nil || skipped != 0 || len(recs) != 1 {
		t.Fatalf("recs=%v skipped=%d err=%v", recs, skipped, err)
	}
	if len(recs[0].Agent) > 64 || strings.ContainsAny(recs[0].Agent+recs[0].QuestionSet, " \n\"{}") {
		t.Errorf("labels not cleaned: %q %q", recs[0].Agent, recs[0].QuestionSet)
	}
}

func TestNeverSendIsNotAudited(t *testing.T) {
	tx := &audit.Transparency{Log: &audit.Log{Path: filepath.Join(t.TempDir(), audit.FileName)}}
	_, pat, ok, err := tx.Gate(context.Background(), loadCfg(t), audit.Meta{}, "cat .env", "x", func(context.Context, string) (string, error) {
		t.Fatal("sent")
		return "", nil
	})
	if err != nil || ok || pat == "" {
		t.Fatalf("pat=%q ok=%v err=%v", pat, ok, err)
	}
	if _, err := os.Stat(tx.Log.Path); !os.IsNotExist(err) {
		t.Errorf("audit written for a send that never happened: %v", err)
	}
}

func TestAuditFailureBlocksSend(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tx := &audit.Transparency{Log: &audit.Log{Path: filepath.Join(blocker, audit.FileName)}}
	var sent []string
	_, _, ok, err := tx.Gate(context.Background(), loadCfg(t), audit.Meta{}, "ls", "hi", okSend(&sent))
	if err == nil || ok || len(sent) != 0 {
		t.Fatalf("err=%v ok=%v sent=%v", err, ok, sent)
	}
}

func TestSummaryAggregationAndSince(t *testing.T) {
	path := filepath.Join(t.TempDir(), audit.FileName)
	l := &audit.Log{Path: path}
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i, r := range []audit.Record{
		{Time: base, Agent: "claude", QuestionSet: "a", BytesBefore: 100, BytesAfter: 80, Hits: map[string]int{"builtin.email": 2}},
		{Time: base.Add(24 * time.Hour), Agent: "codex", QuestionSet: "a", BytesBefore: 50, BytesAfter: 50, Hits: map[string]int{"builtin.email": 1, "builtin.home-path": 4}},
		{Time: base.Add(48 * time.Hour), Agent: "claude", QuestionSet: "b", BytesBefore: 10, BytesAfter: 9},
	} {
		if err := l.Append(r); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0); f != nil {
		_, _ = f.WriteString("not json\n")
		_ = f.Close()
	}
	recs, skipped, err := audit.Read(path, time.Time{})
	if err != nil || skipped != 1 || len(recs) != 3 {
		t.Fatalf("recs=%d skipped=%d err=%v", len(recs), skipped, err)
	}
	s := audit.Summarize(recs)
	if s.Sends != 3 || s.BytesBefore != 160 || s.BytesAfter != 139 {
		t.Errorf("totals %+v", s)
	}
	if s.Agents["claude"] != 2 || s.Agents["codex"] != 1 || s.QuestionSets["a"] != 2 {
		t.Errorf("groups %+v", s)
	}
	if s.Hits["builtin.email"] != 3 || s.Hits["builtin.home-path"] != 4 {
		t.Errorf("hits %+v", s.Hits)
	}
	if !s.First.Equal(base) || !s.Last.Equal(base.Add(48*time.Hour)) {
		t.Errorf("range %v..%v", s.First, s.Last)
	}
	if got := audit.SortedKeys(s.Hits); got[0] != "builtin.home-path" {
		t.Errorf("order %v", got)
	}
	late, _, _ := audit.Read(path, base.Add(12*time.Hour))
	if len(late) != 2 {
		t.Errorf("since kept %d, want 2", len(late))
	}
	if r, _, err := audit.Read(filepath.Join(t.TempDir(), "none"), time.Time{}); err != nil || r != nil {
		t.Errorf("missing file: %v %v", r, err)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestReviewHonorsMaxTTLAndPerms(t *testing.T) {
	c := &clock{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	rv := &audit.Review{Path: filepath.Join(t.TempDir(), "state", audit.ReviewFileName), Enabled: true, Max: 3, TTL: time.Hour, Now: c.now}
	for i := 0; i < 5; i++ {
		if err := rv.Add(audit.Entry{Agent: "a", QuestionSet: "q", Payload: "p" + string(rune('0'+i))}); err != nil {
			t.Fatal(err)
		}
		c.t = c.t.Add(10 * time.Minute)
	}
	got, err := rv.Last(0)
	if err != nil || len(got) != 3 || got[0].Payload != "p2" || got[2].Payload != "p4" {
		t.Fatalf("max not honored: %+v err=%v", got, err)
	}
	if got, _ = rv.Last(2); len(got) != 2 || got[1].Payload != "p4" {
		t.Errorf("last 2: %+v", got)
	}
	if runtime.GOOS != "windows" {
		if perm(t, rv.Path) != 0o600 {
			t.Errorf("store mode %v", perm(t, rv.Path))
		}
		if perm(t, filepath.Dir(rv.Path)) != 0o700 {
			t.Errorf("dir mode %v", perm(t, filepath.Dir(rv.Path)))
		}
	}
	// p2 was stored 20+ min before p4; advance so p2 and p3 expire but p4 lives.
	c.t = c.t.Add(30 * time.Minute) // now = start+80m; p2@20m (60m old: expired), p3@30m, p4@40m
	got, err = rv.Last(0)
	if err != nil || len(got) != 2 || got[0].Payload != "p3" {
		t.Fatalf("ttl purge: %+v err=%v", got, err)
	}
	if data, _ := os.ReadFile(rv.Path); strings.Contains(string(data), "p2") {
		t.Errorf("expired payload still on disk: %s", data)
	}
	c.t = c.t.Add(24 * time.Hour)
	if got, _ = rv.Last(0); len(got) != 0 {
		t.Errorf("everything should have expired: %+v", got)
	}
	if _, err := os.Stat(rv.Path); !os.IsNotExist(err) {
		t.Errorf("empty store should be removed: %v", err)
	}
	// An Add purges expired entries too.
	_ = rv.Add(audit.Entry{Payload: "old"})
	c.t = c.t.Add(2 * time.Hour)
	_ = rv.Add(audit.Entry{Payload: "new"})
	if data, _ := os.ReadFile(rv.Path); strings.Contains(string(data), "old") || !strings.Contains(string(data), "new") {
		t.Errorf("add did not purge: %s", data)
	}
}

func TestReviewDisabledStoresNothingAndPurges(t *testing.T) {
	path := filepath.Join(t.TempDir(), audit.ReviewFileName)
	on := &audit.Review{Path: path, Enabled: true}
	if err := on.Add(audit.Entry{Payload: "sensitive"}); err != nil {
		t.Fatal(err)
	}
	off := &audit.Review{Path: path}
	if got, err := off.Last(5); err != nil || len(got) != 0 {
		t.Fatalf("disabled Last: %v %v", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale payloads survived disabling: %v", err)
	}
	if err := off.Add(audit.Entry{Payload: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("disabled store wrote a file: %v", err)
	}
}

func TestGateStoresExactPayloadOnlyWhenReviewOn(t *testing.T) {
	dir := t.TempDir()
	text := "token password=" + secret + " " + email
	for _, enabled := range []bool{false, true} {
		tx := &audit.Transparency{
			Log:    &audit.Log{Path: filepath.Join(dir, audit.FileName)},
			Review: &audit.Review{Path: filepath.Join(dir, audit.ReviewFileName), Enabled: enabled},
		}
		sent := &[]string{}
		if _, _, ok, err := tx.Gate(context.Background(), loadCfg(t), audit.Meta{Agent: "a", QuestionSet: "q"}, "ls", text, okSend(sent)); err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		got, err := tx.Review.Last(0)
		if err != nil {
			t.Fatal(err)
		}
		if !enabled {
			if len(got) != 0 {
				t.Fatalf("stored while off: %+v", got)
			}
			continue
		}
		if len(got) != 1 || got[0].Payload != (*sent)[0] {
			t.Fatalf("stored payload differs from what was sent: %+v vs %q", got, *sent)
		}
		if strings.Contains(got[0].Payload, secret) || strings.Contains(got[0].Payload, email) {
			t.Errorf("stored payload is not redacted: %q", got[0].Payload)
		}
	}
}

type prompter struct {
	answer bool
	err    error
	asked  []string
}

func (p *prompter) Confirm(payload string) (bool, error) {
	p.asked = append(p.asked, payload)
	return p.answer, p.err
}

func TestConfirmModeBlocksUntilYesAndAbortsOnNo(t *testing.T) {
	dir := t.TempDir()
	cfg := loadCfg(t)
	cfg.Confirm = true
	text := "password=" + secret
	newTx := func(p audit.Prompter) *audit.Transparency {
		return &audit.Transparency{
			Log:      &audit.Log{Path: filepath.Join(dir, audit.FileName)},
			Review:   &audit.Review{Path: filepath.Join(dir, audit.ReviewFileName), Enabled: true},
			Prompter: p,
		}
	}
	var sent []string

	no := &prompter{}
	tx := newTx(no)
	_, _, ok, err := tx.Gate(context.Background(), cfg, audit.Meta{}, "ls", text, okSend(&sent))
	if !errors.Is(err, audit.ErrDeclined) || ok || len(sent) != 0 {
		t.Fatalf("no: err=%v ok=%v sent=%v", err, ok, sent)
	}
	if len(no.asked) != 1 || strings.Contains(no.asked[0], secret) {
		t.Errorf("prompt must show the redacted payload: %q", no.asked)
	}
	for _, f := range []string{audit.FileName, audit.ReviewFileName} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("declined send left %s behind", f)
		}
	}

	boom := &prompter{err: errors.New("no tty")}
	if _, _, ok, err = newTx(boom).Gate(context.Background(), cfg, audit.Meta{}, "ls", text, okSend(&sent)); err == nil || ok || len(sent) != 0 {
		t.Fatalf("prompt error must abort: err=%v ok=%v", err, ok)
	}

	yes := &prompter{answer: true}
	out, _, ok, err := newTx(yes).Gate(context.Background(), cfg, audit.Meta{}, "ls", text, okSend(&sent))
	if err != nil || !ok || out != "answer" || len(sent) != 1 || sent[0] != yes.asked[0] {
		t.Fatalf("yes: out=%q ok=%v err=%v sent=%v asked=%v", out, ok, err, sent, yes.asked)
	}

	// Hooks pass no prompter: confirm must not block them.
	sent = nil
	if _, _, ok, err = newTx(nil).Gate(context.Background(), cfg, audit.Meta{}, "ls", text, okSend(&sent)); err != nil || !ok || len(sent) != 1 {
		t.Fatalf("non-interactive: ok=%v err=%v sent=%v", ok, err, sent)
	}
	// And a prompter is not consulted when confirm is off.
	cfg.Confirm = false
	unused := &prompter{}
	if _, _, ok, err = newTx(unused).Gate(context.Background(), cfg, audit.Meta{}, "ls", text, okSend(&sent)); err != nil || !ok || len(unused.asked) != 0 {
		t.Fatalf("confirm off: ok=%v err=%v asked=%v", ok, err, unused.asked)
	}
}

func TestTerminalPrompter(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{{"y\n", true}, {"YES\n", true}, {" yes \n", true}, {"n\n", false}, {"\n", false}, {"", false}, {"maybe\n", false}, {"y", true}} {
		var out bytes.Buffer
		got, err := audit.NewPrompter(strings.NewReader(tc.in), &out).Confirm("REDACTED PAYLOAD")
		if err != nil || got != tc.want {
			t.Errorf("%q: got %v err %v, want %v", tc.in, got, err, tc.want)
		}
		if !strings.Contains(out.String(), "REDACTED PAYLOAD") {
			t.Errorf("payload not shown: %q", out.String())
		}
	}
}
