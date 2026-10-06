package redact

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/redact/audit"
)

// parseSince accepts a duration back from now ("36h", "7d"), a date
// ("2026-09-01") or an RFC 3339 timestamp.
func parseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if days, err := strconv.Atoi(n); err == nil && days >= 0 {
			return now.AddDate(0, 0, -days), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: use a duration like 24h or 7d, a date like 2026-09-01, or an RFC 3339 time", s)
}

func (a *App) redactAudit(args []string) int {
	fs := a.NewFlagSet("redact audit")
	since := fs.String("since", "", "only sends since a duration (24h, 7d), date or RFC 3339 time")
	format := fs.String("format", "text", "output format: text or json")
	pos, code, done := app.ParseFlags(fs, args)
	if done {
		return code
	}
	if len(pos) > 0 || (*format != "text" && *format != "json") {
		a.Errf("usage: jevkit redact audit [--since <when>] [--format text|json]\n")
		return app.ExitUsage
	}
	from, err := parseSince(*since, a.Clock())
	if err != nil {
		a.Errf("jevkit redact audit: %v\n", err)
		return app.ExitUsage
	}
	if a.StateDir == "" {
		a.Errf("jevkit redact audit: no state directory; set JEVKIT_STATE_DIR\n")
		return app.ExitFail
	}
	recs, skipped, err := audit.Read(a.AuditPath(), from)
	if err != nil {
		a.Errf("jevkit redact audit: %v\n", err)
		return app.ExitFail
	}
	sum := audit.Summarize(recs)
	if *format == "json" {
		out := struct {
			Path    string `json:"path"`
			Skipped int    `json:"skipped_lines"`
			audit.Summary
		}{a.AuditPath(), skipped, sum}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			a.Errf("jevkit redact audit: %v\n", err)
			return app.ExitFail
		}
		a.Outf("%s\n", b)
		return app.ExitOK
	}
	a.Outf("audit log: %s\n", a.AuditPath())
	if sum.Sends == 0 {
		a.Outf("no sends recorded\n")
		return app.ExitOK
	}
	a.Outf("sends: %d (%s to %s)\n", sum.Sends, sum.First.Format(time.RFC3339), sum.Last.Format(time.RFC3339))
	a.Outf("bytes: %d before redaction, %d after\n", sum.BytesBefore, sum.BytesAfter)
	if skipped > 0 {
		a.Outf("skipped %d unreadable line(s)\n", skipped)
	}
	for _, sec := range []struct {
		title string
		m     map[string]int
	}{{"agent", sum.Agents}, {"question set", sum.QuestionSets}, {"rule", sum.Hits}} {
		if len(sec.m) == 0 {
			continue
		}
		a.Outf("\n")
		a.Heading(strings.ToUpper(sec.title[:1]) + sec.title[1:] + "s")
		rows := make([][]string, 0, len(sec.m))
		for _, k := range audit.SortedKeys(sec.m) {
			rows = append(rows, []string{k, strconv.Itoa(sec.m[k])})
		}
		a.Table([]string{strings.ToUpper(sec.title), map[bool]string{true: "HITS", false: "SENDS"}[sec.title == "rule"]}, rows)
	}
	return app.ExitOK
}

func (a *App) redactLast(args []string) int {
	fs := a.NewFlagSet("redact last")
	pos, code, done := app.ParseFlags(fs, args)
	if done {
		return code
	}
	n := 1
	if len(pos) > 1 {
		a.Errf("usage: jevkit redact last [n]\n")
		return app.ExitUsage
	}
	if len(pos) == 1 {
		v, err := strconv.Atoi(pos[0])
		if err != nil || v < 1 {
			a.Errf("jevkit redact last: n must be a positive integer, got %q\n", pos[0])
			return app.ExitUsage
		}
		n = v
	}
	cfg, ok := a.LoadConfig("redact last")
	if !ok {
		return app.ExitFail
	}
	if a.StateDir == "" {
		a.Errf("jevkit redact last: no state directory; set JEVKIT_STATE_DIR\n")
		return app.ExitFail
	}
	rv := &audit.Review{Path: a.ReviewPath(), Enabled: cfg.Review, Max: cfg.ReviewMax, TTL: cfg.ReviewTTL, Now: a.Now}
	entries, err := rv.Last(n)
	if err != nil {
		a.Errf("jevkit redact last: %v\n", err)
		return app.ExitFail
	}
	if !cfg.Review {
		a.Outf("review mode is off, so no payloads are stored.\nturn it on with `review: true` in redact.yaml or JEVKIT_REDACT_REVIEW=1; stored payloads are sensitive.\n")
		return app.ExitOK
	}
	if len(entries) == 0 {
		a.Outf("no stored sends (kept: last %d, for %s)\n", cfg.ReviewMax, cfg.ReviewTTL)
		return app.ExitOK
	}
	for i, e := range entries {
		if i > 0 {
			a.Outf("\n")
		}
		a.Outf("=== %s %s/%s (%d bytes) ===\n%s\n", e.Time.Format(time.RFC3339), e.Agent, e.QuestionSet, len(e.Payload), e.Payload)
	}
	return app.ExitOK
}
