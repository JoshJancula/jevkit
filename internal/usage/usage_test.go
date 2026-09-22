package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const helperEnv = "USAGE_TEST_APPEND_HELPER"

// TestMain lets the test binary double as a child process that appends
// records, to exercise cross-process locking.
func TestMain(m *testing.M) {
	if dir := os.Getenv(helperEnv); dir != "" {
		id := os.Getenv("USAGE_TEST_ID")
		for i := 0; i < 50; i++ {
			err := Append(dir, Record{Model: "jev-1.13.0", QuestionSetID: "child-" + id, InputTokens: i, UsageSource: SourceMeasured, Agent: strings.Repeat("x", 300)})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func assertAllValidJSON(t *testing.T, path string, want int) []Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		t.Fatal("file does not end in newline")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != want {
		t.Fatalf("got %d lines, want %d", len(lines), want)
	}
	var recs []Record
	for i, l := range lines {
		var r Record
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("line %d is not valid JSON: %v: %q", i, err, l)
		}
		recs = append(recs, r)
	}
	return recs
}

func TestConcurrentGoroutineAppends(t *testing.T) {
	dir := t.TempDir()
	const n, per = 32, 25
	var wg sync.WaitGroup
	for g := 0; g < n; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				// Long agent string pushes lines past any atomic-write size.
				if err := Append(dir, Record{Model: "m", QuestionSetID: fmt.Sprint("g", g), InputTokens: i, UsageSource: SourceMeasured, Agent: strings.Repeat("a", 5000)}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	recs := assertAllValidJSON(t, Path(dir), n*per)
	if len(recs) != n*per {
		t.Fatal("record count")
	}
}

func TestConcurrentProcessAppends(t *testing.T) {
	dir := t.TempDir()
	const procs = 6
	var cmds []*exec.Cmd
	var outs []*bytes.Buffer
	for i := 0; i < procs; i++ {
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), helperEnv+"="+dir, fmt.Sprintf("USAGE_TEST_ID=%d", i))
		b := &bytes.Buffer{}
		c.Stderr = b
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds, outs = append(cmds, c), append(outs, b)
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("child %d: %v: %s", i, err, outs[i])
		}
	}
	recs := assertAllValidJSON(t, Path(dir), procs*50)
	per := map[string]int{}
	for _, r := range recs {
		per[r.QuestionSetID]++
	}
	for i := 0; i < procs; i++ {
		if per[fmt.Sprint("child-", i)] != 50 {
			t.Fatalf("child %d wrote %d records", i, per[fmt.Sprint("child-", i)])
		}
	}
}

func TestAppendDefaultsAndPerms(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, Record{Model: "jev-1.13.0", InputTokens: -5, OutputTokens: 7}); err != nil {
		t.Fatal(err)
	}
	r := assertAllValidJSON(t, Path(dir), 1)[0]
	if _, err := time.Parse(time.RFC3339, r.Timestamp); err != nil {
		t.Fatalf("timestamp %q: %v", r.Timestamp, err)
	}
	if r.Transport != TransportHTTPS || r.UsageSource != SourceUnavailable || r.InputTokens != 0 || r.OutputTokens != 7 {
		t.Fatalf("defaults: %+v", r)
	}
	if runtimeUnix() {
		st, _ := os.Stat(Path(dir))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("perm %v", st.Mode().Perm())
		}
		if !strings.HasSuffix(filepath.Dir(Path(dir)), filepath.Join(filepath.Base(dir), "jevkit")) {
			t.Fatalf("path %s", Path(dir))
		}
	}
}

func runtimeUnix() bool { return os.PathSeparator == '/' }

func TestAppendFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Record{Timestamp: "2026-01-02T03:04:05Z", Model: "jev-1.13.0", QuestionSetID: "noul-success", InputTokens: 96, OutputTokens: 12,
		UsageSource: SourceMeasured, Transport: TransportFixture, Agent: "claude", PlanKey: "p", Session: "s1"}
	if err := Append(dir, want); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	data, _ := os.ReadFile(Path(dir))
	_ = json.Unmarshal(data, &raw)
	for _, k := range []string{"timestamp", "model", "questionSetId", "input_tokens", "output_tokens", "usageSource", "transport", "agent", "planKey", "session"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing field %s", k)
		}
	}
	if got := assertAllValidJSON(t, Path(dir), 1)[0]; got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestReadRecordsSkipsGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "usage.jsonl")
	_ = os.WriteFile(p, []byte("\n{not json\n{\"model\":\"a\"}\n[1]\n   \n{\"model\":\"b\"}"), 0o600)
	recs, err := ReadRecords(p)
	if err != nil || len(recs) != 2 {
		t.Fatalf("recs=%v err=%v", recs, err)
	}
	if recs, err := ReadRecords(filepath.Join(t.TempDir(), "none")); err != nil || recs != nil {
		t.Fatalf("missing file: %v %v", recs, err)
	}
}

// Ported from jev-usage.bats.
func fixtureCall() Record {
	return Record{Timestamp: "t", Model: "jev-1.13.0", QuestionSetID: "noul-success", InputTokens: 96, OutputTokens: 12,
		UsageSource: SourceMeasured, Transport: TransportFixture, PlanKey: "jev-usage-test"}
}

func TestAggregateIgnoresFixtureByDefault(t *testing.T) {
	recs := []Record{fixtureCall()}
	s := Aggregate(recs, Filter{}, env(nil))
	if s.Calls != 0 || s.Cost != nil {
		t.Fatalf("%+v", s)
	}
	var b bytes.Buffer
	_ = Render(&b, s, FormatText, false)
	if !strings.Contains(b.String(), "no recorded calls") {
		t.Fatal(b.String())
	}
	s = Aggregate(recs, Filter{IncludeFixture: true}, env(nil))
	if s.Calls != 1 || s.InputTokens != 96 || s.OutputTokens != 12 {
		t.Fatalf("%+v", s)
	}
}

func TestOmitEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, Aggregate(nil, Filter{}, nil), FormatText, true); err != nil || b.Len() != 0 {
		t.Fatalf("%q %v", b.String(), err)
	}
}

func TestTextAndJSONReport(t *testing.T) {
	recs := []Record{{Timestamp: "t", Model: "jev-1.13.0", QuestionSetID: "graph.router-confidence", InputTokens: 296, OutputTokens: 20, UsageSource: SourceMeasured, Transport: TransportHTTPS, PlanKey: "p"}}
	s := Aggregate(recs, Filter{}, env(nil))
	var b bytes.Buffer
	if err := Render(&b, s, FormatText, false); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"Jev (TypeSafe AI) usage", "input 296, output 20", "graph.router-confidence", "measured 1, usage unavailable 0"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("text missing %q:\n%s", w, b.String())
		}
	}
	b.Reset()
	if err := Render(&b, s, FormatJSON, false); err != nil {
		t.Fatal(err)
	}
	var j map[string]any
	if err := json.Unmarshal(b.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	if j["calls"] != float64(1) || j["input_tokens"] != float64(296) || j["kind"] != "jev_usage" {
		t.Fatalf("%v", j)
	}
	if err := Render(&b, s, "yaml", false); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestFilters(t *testing.T) {
	mk := func(plan, sess, agent, model, qs, ts, tr string) Record {
		return Record{PlanKey: plan, Session: sess, Agent: agent, Model: model, QuestionSetID: qs, Timestamp: ts, Transport: tr,
			InputTokens: 10, OutputTokens: 1, UsageSource: SourceMeasured}
	}
	recs := []Record{
		mk("A", "s1", "claude", "m1", "q1", "2026-01-01T00:00:00Z", "https"),
		mk("A", "s2", "codex", "m1", "q2", "2026-02-01T00:00:00Z", "https"),
		mk("B", "s3", "claude", "m2", "q1", "2026-03-01T00:00:00Z", "https"),
		mk("A", "s1", "claude", "m1", "q1", "bad", "https"),
	}
	feb := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		f    Filter
		want int
	}{
		{"none", Filter{}, 4},
		{"plan", Filter{PlanKey: "A"}, 3},
		{"session", Filter{Session: "s1"}, 2},
		{"agent", Filter{Agent: "claude"}, 3},
		{"model", Filter{Model: "m2"}, 1},
		{"qs", Filter{QuestionSetID: "q1"}, 3},
		{"since", Filter{Since: feb}, 2},
		{"until", Filter{Until: feb}, 2},
		{"combined", Filter{PlanKey: "A", Agent: "claude", Since: feb.Add(-24 * time.Hour * 60)}, 1},
		{"no match", Filter{PlanKey: "zzz"}, 0},
	}
	for _, c := range cases {
		if got := Aggregate(recs, c.f, env(nil)).Calls; got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestPlanScopedTotals(t *testing.T) {
	recs := []Record{
		{Model: "jev-1.13.0", QuestionSetID: "graph.router-confidence", InputTokens: 296, OutputTokens: 20, UsageSource: SourceMeasured, PlanKey: "PLANJ"},
		{Model: "jev-1.13.0", QuestionSetID: "graph.router-confidence", InputTokens: 900, OutputTokens: 90, UsageSource: SourceMeasured, PlanKey: "OTHER"},
	}
	s := Aggregate(recs, Filter{PlanKey: "PLANJ"}, env(nil))
	if s.Calls != 1 || s.InputTokens != 296 || s.OutputTokens != 20 {
		t.Fatalf("%+v", s)
	}
}

func TestGroupingAndCounts(t *testing.T) {
	recs := []Record{
		{Model: "m", QuestionSetID: "a", InputTokens: 1, UsageSource: SourceMeasured},
		{Model: "m", QuestionSetID: "a", InputTokens: 2, UsageSource: SourceUnavailable},
		{QuestionSetID: "", InputTokens: 4, UsageSource: SourceMeasured, Agent: "x"},
	}
	s := Aggregate(recs, Filter{}, env(nil))
	if s.CallsMeasured != 2 || s.CallsUnavailable != 1 {
		t.Fatalf("%+v", s)
	}
	if s.ByQuestionSet["a"].Calls != 2 || s.ByQuestionSet["a"].InputTokens != 3 || s.ByQuestionSet["(unnamed)"].Calls != 1 {
		t.Fatalf("%+v", s.ByQuestionSet)
	}
	if s.ByModel["(unresolved)"].Calls != 1 || s.ByAgent["x"].InputTokens != 4 || s.ByAgent["(none)"].Calls != 2 {
		t.Fatalf("%+v %+v", s.ByModel, s.ByAgent)
	}
}

func TestCost(t *testing.T) {
	recs := []Record{{InputTokens: 1_000_000, OutputTokens: 500_000, UsageSource: SourceMeasured}}
	cases := []struct {
		name    string
		env     map[string]string
		usd     float64
		in, out float64
	}{
		{"defaults", nil, 0.042, 0.042, 0},
		{"override in", map[string]string{EnvInputRate: "1"}, 1, 1, 0},
		{"override both", map[string]string{EnvInputRate: "1", EnvOutputRate: "2"}, 2, 1, 2},
		{"whitespace ok", map[string]string{EnvInputRate: " 0.5 "}, 0.5, 0.5, 0},
		{"garbage falls back", map[string]string{EnvInputRate: "abc", EnvOutputRate: "-1"}, 0.042, 0.042, 0},
		{"partial number falls back", map[string]string{EnvInputRate: "1x"}, 0.042, 0.042, 0},
		{"nan falls back", map[string]string{EnvInputRate: "NaN"}, 0.042, 0.042, 0},
		{"zero input", map[string]string{EnvInputRate: "0"}, 0, 0, 0},
	}
	for _, c := range cases {
		s := Aggregate(recs, Filter{}, env(c.env))
		if s.Cost == nil || s.Cost.EstimatedUSD != c.usd || s.Cost.InputUSDPerMTok != c.in || s.Cost.OutputUSDPerMTok != c.out || s.Cost.Note != "estimated" {
			t.Errorf("%s: %+v", c.name, s.Cost)
		}
	}
	if s := Aggregate(nil, Filter{}, env(nil)); s.Cost != nil {
		t.Fatal("cost present with no calls")
	}
	// json emits null cost when empty
	var b bytes.Buffer
	_ = Render(&b, Aggregate(nil, Filter{}, env(nil)), FormatJSON, false)
	if !strings.Contains(b.String(), `"cost": null`) {
		t.Fatal(b.String())
	}
}
