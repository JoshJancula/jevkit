package registry

import (
	"bufio"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/jev"
)

// testRegistry has a choice set with declared options, a call-time choice set,
// and a noul-primary set, all with act 0.8 / escalate 0.5.
const testRegistry = `{
  "registryVersion": "7",
  "questionSets": {
    "t.choice": {"id":"t.choice","version":3,"surface":"graph","description":"d",
      "questions":{"pick":{"type":"choice","instructions":"i","criteria":{"a":"A","b":"B"}}},
      "policy":{"primaryQuestion":"pick","actThreshold":0.8,"escalateThreshold":0.5,"fallback":"f"}},
    "t.open": {"id":"t.open","version":1,"surface":"compaction","description":"d",
      "questions":{"pick":{"type":"choice","instructions":"i","criteria":{}}},
      "policy":{"primaryQuestion":"pick","actThreshold":0.8,"escalateThreshold":0.5,"fallback":"f"}},
    "t.noul": {"id":"t.noul","version":1,"surface":"mcp","description":"d",
      "questions":{"q":{"type":"noul","instructions":"i"}},
      "policy":{"primaryQuestion":"q","actThreshold":0.8,"escalateThreshold":0.5,"fallback":"f"}},
    "t.score": {"id":"t.score","version":1,"surface":"mcp","description":"d",
      "questions":{"s":{"type":"score","instructions":"i","criteria":["lo","hi"]}},
      "policy":{"primaryQuestion":"s","actThreshold":0.8,"escalateThreshold":0.5,"fallback":"f"}}
  }
}`

func newDecider(t *testing.T, shadow string) (*Decider, string) {
	t.Helper()
	r, err := Parse([]byte(testRegistry))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return &Decider{
		Registry: r,
		StateDir: dir,
		Getenv: func(k string) string {
			if k == "JEVKIT_SHADOW" {
				return shadow
			}
			return ""
		},
		Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	}, dir
}

func readLog(t *testing.T, dir string) []Decision {
	t.Helper()
	f, err := os.Open(DecisionsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []Decision
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var d Decision
		if err := json.Unmarshal(sc.Bytes(), &d); err != nil {
			t.Fatalf("invalid line %q: %v", sc.Text(), err)
		}
		out = append(out, d)
	}
	return out
}

func TestThresholdBoundaries(t *testing.T) {
	tests := []struct {
		conf float64
		want string
	}{
		{1, Act},
		{0.8, Act},
		{0.7999999, Gather},
		{0.65, Gather},
		{0.5, Gather},
		{0.4999999, Fallback},
		{0, Fallback},
	}
	for _, tt := range tests {
		for _, id := range []string{"t.choice", "t.noul", "t.score"} {
			d, _ := newDecider(t, "")
			var ans jev.Answer
			switch id {
			case "t.choice":
				ans = jev.ChoiceAnswer{Choice: "a", Confidence: tt.conf}
			case "t.noul":
				ans = jev.NoulAnswer{Noul: tt.conf}
			default:
				ans = jev.ScoreAnswer{Score: 1, Confidence: tt.conf}
			}
			primaryQ := d.Registry.QuestionSets[id].Policy.PrimaryQuestion
			got, err := d.Decide(id, map[string]jev.Answer{primaryQ: ans})
			if err != nil {
				t.Fatalf("%s %v: %v", id, tt.conf, err)
			}
			if got.Decision != tt.want || got.Reason != tt.want {
				t.Errorf("%s conf %v: decision %q reason %q, want %q", id, tt.conf, got.Decision, got.Reason, tt.want)
			}
			if got.FallbackUsed != (tt.want == Fallback) {
				t.Errorf("%s conf %v: fallbackUsed %v", id, tt.conf, got.FallbackUsed)
			}
			if got.Shadow {
				t.Errorf("%s: shadow set without JEVKIT_SHADOW", id)
			}
		}
	}
}

func TestDecisionFields(t *testing.T) {
	d, dir := newDecider(t, "")
	got, err := d.Decide("t.choice", map[string]jev.Answer{"pick": jev.ChoiceAnswer{Choice: "b", Confidence: 0.9}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Chosen == nil || *got.Chosen != "b" || got.QuestionSetID != "t.choice" ||
		got.QuestionSetVersion != 3 || got.Surface != "graph" || got.RegistryVersion != "7" ||
		got.Timestamp != "2026-01-02T03:04:05Z" {
		t.Errorf("fields: %+v", got)
	}
	if logged := readLog(t, dir); len(logged) != 1 || logged[0].Decision != Act {
		t.Errorf("log: %+v", logged)
	}
	noul, err := d.Decide("t.noul", map[string]jev.Answer{"q": jev.NoulAnswer{Noul: 0.9}})
	if err != nil || noul.Chosen != nil {
		t.Errorf("noul chosen = %v, %v", noul.Chosen, err)
	}
}

func TestOptionNotOffered(t *testing.T) {
	d, _ := newDecider(t, "")
	got, err := d.Decide("t.choice", map[string]jev.Answer{"pick": jev.ChoiceAnswer{Choice: "zzz", Confidence: 0.99}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision != Fallback || got.Reason != ReasonOptionNotOffered || !got.FallbackUsed {
		t.Errorf("got %+v", got)
	}
	// A call-time choice set (empty criteria) accepts any option.
	open, err := d.Decide("t.open", map[string]jev.Answer{"pick": jev.ChoiceAnswer{Choice: "L007", Confidence: 0.99}})
	if err != nil || open.Decision != Act {
		t.Errorf("open set: %+v, %v", open, err)
	}
}

func TestDecideErrors(t *testing.T) {
	d, dir := newDecider(t, "")
	if _, err := d.Decide("nope", nil); !errors.Is(err, ErrUnknownSet) {
		t.Errorf("unknown set: %v", err)
	}
	if _, err := d.Decide("t.choice", map[string]jev.Answer{}); !errors.Is(err, ErrNoDecision) {
		t.Errorf("missing answer: %v", err)
	}
	if _, err := d.Decide("t.choice", map[string]jev.Answer{"pick": nil}); !errors.Is(err, ErrNoDecision) {
		t.Errorf("nil answer: %v", err)
	}
	if _, err := d.Decide("t.noul", map[string]jev.Answer{"q": jev.NoulAnswer{Noul: math.NaN()}}); !errors.Is(err, ErrNoDecision) {
		t.Errorf("NaN: %v", err)
	}
	if _, err := os.Stat(DecisionsPath(dir)); !os.IsNotExist(err) {
		t.Errorf("errors must not be logged: %v", err)
	}
}

func TestShadowMode(t *testing.T) {
	tests := []struct {
		conf      float64
		wouldHave string
	}{{0.95, Act}, {0.6, Gather}, {0.1, Fallback}}
	for _, tt := range tests {
		d, dir := newDecider(t, "1")
		answers := map[string]jev.Answer{"pick": jev.ChoiceAnswer{Choice: "a", Confidence: tt.conf}}
		got, err := d.Decide("t.choice", answers)
		if err != nil {
			t.Fatal(err)
		}
		if got.Decision != Fallback || got.Reason != ReasonShadow || !got.Shadow || !got.FallbackUsed {
			t.Errorf("conf %v returned %+v", tt.conf, got)
		}
		logged := readLog(t, dir)
		if len(logged) != 1 {
			t.Fatalf("logged %d records", len(logged))
		}
		l := logged[0]
		if l.Decision != tt.wouldHave || !l.Shadow || !l.FallbackUsed || l.Reason != tt.wouldHave {
			t.Errorf("conf %v logged %+v, want would-have %s", tt.conf, l, tt.wouldHave)
		}
		if l.Answers["pick"] == nil {
			t.Errorf("shadow record lacks answers: %+v", l)
		}
	}
}

func TestShadowOnlyForExactlyOne(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes", "2"} {
		d, _ := newDecider(t, v)
		got, err := d.Decide("t.noul", map[string]jev.Answer{"q": jev.NoulAnswer{Noul: 0.9}})
		if err != nil || got.Decision != Act || got.Shadow {
			t.Errorf("JEVKIT_SHADOW=%q: %+v, %v", v, got, err)
		}
	}
}

func TestShadowOptionNotOfferedKeepsWouldHave(t *testing.T) {
	d, dir := newDecider(t, "1")
	if _, err := d.Decide("t.choice", map[string]jev.Answer{"pick": jev.ChoiceAnswer{Choice: "x", Confidence: 0.99}}); err != nil {
		t.Fatal(err)
	}
	if l := readLog(t, dir)[0]; l.Decision != Fallback || l.Reason != ReasonOptionNotOffered {
		t.Errorf("logged %+v", l)
	}
}

func TestLoggingIsBestEffort(t *testing.T) {
	d, dir := newDecider(t, "")
	// A file where the state directory should be makes logging fail.
	blocker := dir + "/blocked"
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d.StateDir = blocker
	got, err := d.Decide("t.noul", map[string]jev.Answer{"q": jev.NoulAnswer{Noul: 0.9}})
	if err != nil || got.Decision != Act {
		t.Errorf("got %+v, %v", got, err)
	}
	d.StateDir = ""
	if _, err := d.Decide("t.noul", map[string]jev.Answer{"q": jev.NoulAnswer{Noul: 0.9}}); err != nil {
		t.Errorf("empty state dir: %v", err)
	}
}

func TestThresholdsOnlyFromRegistry(t *testing.T) {
	// The API takes no threshold parameter: Policy.Classify uses the set's own.
	d, _ := newDecider(t, "")
	s, _ := d.Registry.Set("t.noul")
	s.Policy.ActThreshold, s.Policy.EscalateThreshold = 0.3, 0.1
	got, _ := d.Decide("t.noul", map[string]jev.Answer{"q": jev.NoulAnswer{Noul: 0.35}})
	if got.Decision != Act {
		t.Errorf("registry threshold not used: %+v", got)
	}
}

func TestConcurrentDecisionLogLinesStayValid(t *testing.T) {
	d, dir := newDecider(t, "1")
	const workers, each = 16, 20
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				answers := map[string]jev.Answer{"pick": jev.ChoiceAnswer{Choice: strings.Repeat("a", 1), Confidence: 0.7}}
				if _, err := d.Decide("t.choice", answers); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n := len(readLog(t, dir)); n != workers*each {
		t.Fatalf("logged %d, want %d", n, workers*each)
	}
}
