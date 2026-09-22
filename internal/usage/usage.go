// Package usage records one JSONL line per successful Jev call and
// aggregates those lines into a report with an estimated cost.
//
// Only the final successful attempt of a call is recorded; failed and
// retried attempts never reach Append. Appends hold an exclusive file lock
// so lines from concurrent goroutines and processes never interleave.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
)

// Usage sources and transports.
const (
	SourceMeasured    = "measured"
	SourceUnavailable = "unavailable"
	TransportHTTPS    = "https"
	TransportFixture  = "fixture"
)

// Record is one usage.jsonl line.
type Record struct {
	Timestamp     string `json:"timestamp"`
	Model         string `json:"model"`
	QuestionSetID string `json:"questionSetId"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	UsageSource   string `json:"usageSource"`
	Transport     string `json:"transport"`
	Agent         string `json:"agent,omitempty"`
	PlanKey       string `json:"planKey,omitempty"`
	Session       string `json:"session,omitempty"`
}

// Path is <stateDir>/jevkit/usage.jsonl.
func Path(stateDir string) string {
	return filepath.Join(stateDir, "jevkit", "usage.jsonl")
}

// Append writes rec as one line under an exclusive lock. Missing fields are
// defaulted: the timestamp to now (UTC), the transport to https, and the
// usage source from whether any token count is present. Negative counts clamp
// to zero.
func Append(stateDir string, rec Record) error {
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if rec.Transport == "" {
		rec.Transport = TransportHTTPS
	}
	if rec.UsageSource == "" {
		rec.UsageSource = SourceUnavailable
	}
	rec.InputTokens, rec.OutputTokens = max(rec.InputTokens, 0), max(rec.OutputTokens, 0)
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	path := Path(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	l, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer l.Release()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReadRecords returns every parseable record in path. A missing file yields
// none; blank and malformed lines are skipped.
func ReadRecords(path string) ([]Record, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Record
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var rec Record
			if json.Unmarshal(line, &rec) == nil {
				out = append(out, rec)
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// Filter selects records. Empty fields match everything. Fixture-transport
// records (offline tests) are excluded unless IncludeFixture is set. Since
// and Until bound the record timestamp (inclusive); records with an
// unparseable timestamp are excluded when either bound is set.
type Filter struct {
	PlanKey        string
	Session        string
	Agent          string
	Model          string
	QuestionSetID  string
	Since, Until   time.Time
	IncludeFixture bool
}

func (f Filter) match(r Record) bool {
	if !f.IncludeFixture && r.Transport == TransportFixture {
		return false
	}
	for _, c := range [][2]string{
		{f.PlanKey, r.PlanKey}, {f.Session, r.Session}, {f.Agent, r.Agent},
		{f.Model, r.Model}, {f.QuestionSetID, r.QuestionSetID},
	} {
		if c[0] != "" && c[0] != c[1] {
			return false
		}
	}
	if !f.Since.IsZero() || !f.Until.IsZero() {
		t, err := time.Parse(time.RFC3339, r.Timestamp)
		if err != nil || (!f.Since.IsZero() && t.Before(f.Since)) || (!f.Until.IsZero() && t.After(f.Until)) {
			return false
		}
	}
	return true
}

// Default rates: input tokens cost USD 0.042 per million, output is free.
const (
	DefaultInputUSDPerMTok  = 0.042
	DefaultOutputUSDPerMTok = 0.0
	EnvInputRate            = "JEVKIT_INPUT_USD_PER_MTOK"
	EnvOutputRate           = "JEVKIT_OUTPUT_USD_PER_MTOK"
)

// Tokens is a call and token tally.
type Tokens struct {
	Calls        int `json:"calls"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Cost is an estimate from the configured rates.
type Cost struct {
	EstimatedUSD     float64 `json:"estimated_usd"`
	InputUSDPerMTok  float64 `json:"input_rate_usd_per_mtok"`
	OutputUSDPerMTok float64 `json:"output_rate_usd_per_mtok"`
	Note             string  `json:"note"`
}

// Summary is the aggregate report.
type Summary struct {
	Kind             string             `json:"kind"`
	SchemaVersion    int                `json:"schema_version"`
	Calls            int                `json:"calls"`
	InputTokens      int                `json:"input_tokens"`
	OutputTokens     int                `json:"output_tokens"`
	CallsMeasured    int                `json:"calls_measured"`
	CallsUnavailable int                `json:"calls_unavailable"`
	ByQuestionSet    map[string]*Tokens `json:"by_question_set"`
	ByModel          map[string]*Tokens `json:"by_model"`
	ByAgent          map[string]*Tokens `json:"by_agent"`
	Cost             *Cost              `json:"cost"`
}

// Aggregate tallies the records that pass f. getenv supplies the rate
// overrides (nil means os.Getenv). Invalid or negative rates fall back to the
// defaults. Cost is nil when nothing matched.
func Aggregate(recs []Record, f Filter, getenv func(string) string) Summary {
	if getenv == nil {
		getenv = os.Getenv
	}
	s := Summary{
		Kind: "jev_usage", SchemaVersion: 1,
		ByQuestionSet: map[string]*Tokens{}, ByModel: map[string]*Tokens{}, ByAgent: map[string]*Tokens{},
	}
	add := func(m map[string]*Tokens, k string, in, out int) {
		t := m[k]
		if t == nil {
			t = &Tokens{}
			m[k] = t
		}
		t.Calls++
		t.InputTokens += in
		t.OutputTokens += out
	}
	for _, r := range recs {
		if !f.match(r) {
			continue
		}
		in, out := max(r.InputTokens, 0), max(r.OutputTokens, 0)
		s.Calls++
		s.InputTokens += in
		s.OutputTokens += out
		if r.UsageSource == SourceMeasured {
			s.CallsMeasured++
		} else {
			s.CallsUnavailable++
		}
		add(s.ByQuestionSet, orDefault(r.QuestionSetID, "(unnamed)"), in, out)
		add(s.ByModel, orDefault(r.Model, "(unresolved)"), in, out)
		add(s.ByAgent, orDefault(r.Agent, "(none)"), in, out)
	}
	if s.Calls > 0 {
		inRate := rate(getenv(EnvInputRate), DefaultInputUSDPerMTok)
		outRate := rate(getenv(EnvOutputRate), DefaultOutputUSDPerMTok)
		usd := float64(s.InputTokens)*inRate/1e6 + float64(s.OutputTokens)*outRate/1e6
		s.Cost = &Cost{
			EstimatedUSD: round6(usd), InputUSDPerMTok: inRate, OutputUSDPerMTok: outRate, Note: "estimated",
		}
	}
	return s
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func rate(raw string, def float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return def
	}
	return v
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }
