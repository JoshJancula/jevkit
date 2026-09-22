package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/OWNER/jevkit/internal/redact/audit"
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
	fs := a.newFlagSet("redact audit")
	since := fs.String("since", "", "only sends since a duration (24h, 7d), date or RFC 3339 time")
	format := fs.String("format", "text", "output format: text or json")
	pos, code, done := parseFlags(fs, args)
	if done {
		return code
	}
	if len(pos) > 0 || (*format != "text" && *format != "json") {
		a.errf("usage: jevkit redact audit [--since <when>] [--format text|json]\n")
		return exitUsage
	}
	from, err := parseSince(*since, a.now())
	if err != nil {
		a.errf("jevkit redact audit: %v\n", err)
		return exitUsage
	}
	if a.StateDir == "" {
		a.errf("jevkit redact audit: no state directory; set JEVKIT_STATE_DIR\n")
		return exitFail
	}
	recs, skipped, err := audit.Read(a.auditPath(), from)
	if err != nil {
		a.errf("jevkit redact audit: %v\n", err)
		return exitFail
	}
	sum := audit.Summarize(recs)
	if *format == "json" {
		out := struct {
			Path    string `json:"path"`
			Skipped int    `json:"skipped_lines"`
			audit.Summary
		}{a.auditPath(), skipped, sum}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			a.errf("jevkit redact audit: %v\n", err)
			return exitFail
		}
		a.outf("%s\n", b)
		return exitOK
	}
	a.outf("audit log: %s\n", a.auditPath())
	if sum.Sends == 0 {
		a.outf("no sends recorded\n")
		return exitOK
	}
	a.outf("sends: %d (%s to %s)\n", sum.Sends, sum.First.Format(time.RFC3339), sum.Last.Format(time.RFC3339))
	a.outf("bytes: %d before redaction, %d after\n", sum.BytesBefore, sum.BytesAfter)
	if skipped > 0 {
		a.outf("skipped %d unreadable line(s)\n", skipped)
	}
	for _, sec := range []struct {
		title string
		m     map[string]int
	}{{"agent", sum.Agents}, {"question set", sum.QuestionSets}, {"rule", sum.Hits}} {
		if len(sec.m) == 0 {
			continue
		}
		a.outf("\n")
		tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", strings.ToUpper(sec.title), map[bool]string{true: "HITS", false: "SENDS"}[sec.title == "rule"])
		for _, k := range audit.SortedKeys(sec.m) {
			_, _ = fmt.Fprintf(tw, "%s\t%d\n", k, sec.m[k])
		}
		_ = tw.Flush()
	}
	return exitOK
}

func (a *App) redactLast(args []string) int {
	fs := a.newFlagSet("redact last")
	pos, code, done := parseFlags(fs, args)
	if done {
		return code
	}
	n := 1
	if len(pos) > 1 {
		a.errf("usage: jevkit redact last [n]\n")
		return exitUsage
	}
	if len(pos) == 1 {
		v, err := strconv.Atoi(pos[0])
		if err != nil || v < 1 {
			a.errf("jevkit redact last: n must be a positive integer, got %q\n", pos[0])
			return exitUsage
		}
		n = v
	}
	cfg, ok := a.load("redact last")
	if !ok {
		return exitFail
	}
	if a.StateDir == "" {
		a.errf("jevkit redact last: no state directory; set JEVKIT_STATE_DIR\n")
		return exitFail
	}
	rv := &audit.Review{Path: a.reviewPath(), Enabled: cfg.Review, Max: cfg.ReviewMax, TTL: cfg.ReviewTTL, Now: a.Now}
	entries, err := rv.Last(n)
	if err != nil {
		a.errf("jevkit redact last: %v\n", err)
		return exitFail
	}
	if !cfg.Review {
		a.outf("review mode is off, so no payloads are stored.\nturn it on with `review: true` in redact.yaml or JEVKIT_REDACT_REVIEW=1; stored payloads are sensitive.\n")
		return exitOK
	}
	if len(entries) == 0 {
		a.outf("no stored sends (kept: last %d, for %s)\n", cfg.ReviewMax, cfg.ReviewTTL)
		return exitOK
	}
	for i, e := range entries {
		if i > 0 {
			a.outf("\n")
		}
		a.outf("=== %s %s/%s (%d bytes) ===\n%s\n", e.Time.Format(time.RFC3339), e.Agent, e.QuestionSet, len(e.Payload), e.Payload)
	}
	return exitOK
}
