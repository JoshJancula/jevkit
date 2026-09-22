package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "sk-test-SECRET-key-123"

const okBody = `{"model":"jev-1.13.0","answers":{"pick":{"choice":"a","probabilities":{"a":0.9,"b":0.1},"confidence":0.9},"flag":{"type":"noul","noul":0.8},"rate":{"score":0.5}},"usage":{"input_tokens":3,"output_tokens":1}}`

func str(s string) *string { return &s }

func sampleRequest() Request {
	return Request{
		QuestionSetID: "test.set",
		State:         "some state",
		Questions: map[string]Question{
			"pick": ChoiceQuestion{Instructions: "which?", Options: map[string]*string{"a": nil, "b": str("bee")}},
			"flag": NoulQuestion{Instructions: "is it?"},
			"rate": ScoreQuestion{Instructions: "how much?"},
		},
	}
}

type step struct {
	status int
	body   string
}

// newServer replays steps in order (the last repeats) and records requests.
func newServer(t *testing.T, steps ...step) (*httptest.Server, *atomic.Int32, *[]http.Request, *[][]byte) {
	t.Helper()
	var n atomic.Int32
	var reqs []http.Request
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqs = append(reqs, *r)
		bodies = append(bodies, b)
		i := int(n.Add(1)) - 1
		s := steps[min(i, len(steps)-1)]
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(srv.Close)
	return srv, &n, &reqs, &bodies
}

func testClient(endpoint string) (*Client, *[]time.Duration, *MemoryBreaker) {
	var sleeps []time.Duration
	br := &MemoryBreaker{}
	c := New(Config{Endpoint: endpoint, Model: DefaultModel, Timeout: 2 * time.Second, MaxRetries: DefaultMaxRetries},
		func() (string, error) { return testKey, nil })
	c.Breaker = br
	c.Sleep = func(_ context.Context, d time.Duration) { sleeps = append(sleeps, d) }
	return c, &sleeps, br
}

func TestAskSuccess(t *testing.T) {
	srv, n, reqs, bodies := newServer(t, step{200, okBody})
	c, _, br := testClient(srv.URL)
	resp, err := c.Ask(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 1 || br.IsOpen() {
		t.Fatalf("calls=%d open=%v", n.Load(), br.IsOpen())
	}
	if got := (*reqs)[0].Header.Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("auth header = %q", got)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal((*bodies)[0], &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["questionSetId"]; ok {
		t.Error("questionSetId leaked onto the wire")
	}
	if string(wire["model"]) != `"jev-latest"` {
		t.Errorf("model = %s", wire["model"])
	}
	if a, ok := resp.Answers["pick"].(ChoiceAnswer); !ok || a.Choice != "a" || a.Confidence != 0.9 {
		t.Errorf("pick = %#v", resp.Answers["pick"])
	}
	if a, ok := resp.Answers["flag"].(NoulAnswer); !ok || a.Noul != 0.8 {
		t.Errorf("flag = %#v", resp.Answers["flag"])
	}
	if a, ok := resp.Answers["rate"].(ScoreAnswer); !ok || a.Score != 0.5 {
		t.Errorf("rate = %#v", resp.Answers["rate"])
	}
	if resp.Usage.InputTokens != 3 || resp.Model != "jev-1.13.0" {
		t.Errorf("resp = %#v", resp)
	}
}

func TestAskStatusHandling(t *testing.T) {
	tests := []struct {
		name       string
		steps      []step
		wantCalls  int32
		wantSleeps []time.Duration
		wantErr    bool
		wantConfig bool
		wantOpen   bool
	}{
		{"401 no retry, opens breaker", []step{{401, `{}`}}, 1, nil, true, true, true},
		{"422 no retry, opens breaker", []step{{422, `{}`}}, 1, nil, true, true, true},
		{"429 then success", []step{{429, ``}, {200, okBody}}, 2, []time.Duration{time.Second}, false, false, false},
		{"529 twice then success", []step{{529, ``}, {529, ``}, {200, okBody}}, 3, []time.Duration{time.Second, 2 * time.Second}, false, false, false},
		{"429 exhausts retries", []step{{429, ``}}, 3, []time.Duration{time.Second, 2 * time.Second}, true, false, false},
		{"500 fails without retry", []step{{500, `oops`}}, 1, nil, true, false, false},
		{"400 fails without retry", []step{{400, `{}`}}, 1, nil, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, n, _, _ := newServer(t, tt.steps...)
			c, sleeps, br := testClient(srv.URL)
			_, err := c.Ask(context.Background(), sampleRequest())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if n.Load() != tt.wantCalls {
				t.Errorf("calls = %d, want %d", n.Load(), tt.wantCalls)
			}
			if len(*sleeps) != len(tt.wantSleeps) {
				t.Fatalf("sleeps = %v, want %v", *sleeps, tt.wantSleeps)
			}
			for i, d := range tt.wantSleeps {
				if (*sleeps)[i] != d {
					t.Errorf("sleep[%d] = %v, want %v", i, (*sleeps)[i], d)
				}
			}
			if br.IsOpen() != tt.wantOpen {
				t.Errorf("breaker open = %v", br.IsOpen())
			}
			if err != nil {
				var je *Error
				if !errors.As(err, &je) || je.Code != CodeTransport || je.Config != tt.wantConfig {
					t.Errorf("err = %#v", err)
				}
			}
		})
	}
}

func TestBreakerOpenDeclines(t *testing.T) {
	srv, n, _, _ := newServer(t, step{401, `{}`}, step{200, okBody})
	c, _, _ := testClient(srv.URL)
	_, _ = c.Ask(context.Background(), sampleRequest())
	_, err := c.Ask(context.Background(), sampleRequest())
	if CodeOf(err) != CodeDeclined || n.Load() != 1 {
		t.Fatalf("err = %v calls = %d", err, n.Load())
	}
}

func TestTwoFailuresOpenBreaker(t *testing.T) {
	srv, _, _, _ := newServer(t, step{500, ``})
	c, _, br := testClient(srv.URL)
	_, _ = c.Ask(context.Background(), sampleRequest())
	if br.IsOpen() {
		t.Fatal("opened after one failure")
	}
	_, _ = c.Ask(context.Background(), sampleRequest())
	if !br.IsOpen() {
		t.Fatal("not open after two failures")
	}
}

func TestMalformedBody(t *testing.T) {
	tests := map[string]string{
		"not json":             `<html>`,
		"empty":                ``,
		"missing answers":      `{"model":"jev-1.13.0","usage":{}}`,
		"null answers":         `{"answers":null}`,
		"answers not object":   `{"answers":[]}`,
		"unknown answer":       `{"answers":{"q":{"foo":1}}}`,
		"bad choice":           `{"answers":{"q":{"choice":5}}}`,
		"type mismatch":        `{"answers":{"q":{"type":"score","noul":0.5}}}`,
		"top level not object": `[1,2]`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			srv, n, _, _ := newServer(t, step{200, body})
			c, _, _ := testClient(srv.URL)
			_, err := c.Ask(context.Background(), sampleRequest())
			if CodeOf(err) != CodeTransport {
				t.Fatalf("err = %v", err)
			}
			if n.Load() != 1 {
				t.Errorf("calls = %d, protocol errors must not retry", n.Load())
			}
		})
	}
	if _, err := DecodeResponse([]byte(`{"model":"x"}`)); !errors.Is(err, ErrNoAnswers) {
		t.Errorf("err = %v, want ErrNoAnswers", err)
	}
}

func TestInputRejectedBeforeNetwork(t *testing.T) {
	opts := func(n int) map[string]*string {
		m := make(map[string]*string, n)
		for i := range n {
			m[string(rune('a'+i%26))+strings.Repeat("x", i)] = nil
		}
		return m
	}
	huge := strings.Repeat("x", budgetBytes+1)
	tests := map[string]Request{
		"oversize state": {State: huge, Questions: map[string]Question{"q": NoulQuestion{Instructions: "?"}}},
		"state plus longest question": {
			State:     strings.Repeat("x", budgetBytes-10),
			Questions: map[string]Question{"q": NoulQuestion{Instructions: strings.Repeat("y", 100)}},
		},
		"256 options":  {State: "s", Questions: map[string]Question{"q": ChoiceQuestion{Instructions: "?", Options: opts(256)}}},
		"nil question": {State: "s", Questions: map[string]Question{"q": nil}},
		"no questions": {State: "s"},
	}
	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			srv, n, _, _ := newServer(t, step{200, okBody})
			c, _, br := testClient(srv.URL)
			_, err := c.Ask(context.Background(), req)
			if CodeOf(err) != CodeRejected {
				t.Fatalf("err = %v", err)
			}
			if n.Load() != 0 || br.IsOpen() {
				t.Errorf("calls = %d open = %v", n.Load(), br.IsOpen())
			}
		})
	}

	t.Run("255 options and exact budget accepted", func(t *testing.T) {
		srv, _, _, _ := newServer(t, step{200, okBody})
		c, _, _ := testClient(srv.URL)
		req := Request{State: "s", Questions: map[string]Question{"q": ChoiceQuestion{Instructions: "?", Options: opts(255)}}}
		if _, err := c.Ask(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	})
}

func TestKeyNeverInErrors(t *testing.T) {
	echo := `{"error":"bad key ` + testKey + `"}`
	for _, tt := range []struct {
		name string
		st   step
	}{{"500 echoing key", step{500, echo}}, {"401 echoing key", step{401, echo}}, {"protocol echoing key", step{200, echo}}, {"429 echoing key", step{429, echo}}} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _, _ := newServer(t, tt.st)
			c, _, _ := testClient(srv.URL)
			_, err := c.Ask(context.Background(), sampleRequest())
			if err == nil || strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("err = %v", err)
			}
		})
	}

	t.Run("transport error containing key in endpoint", func(t *testing.T) {
		c, _, _ := testClient("http://127.0.0.1:1/" + testKey)
		c.Config.MaxRetries = 0
		_, err := c.Ask(context.Background(), sampleRequest())
		if err == nil || strings.Contains(err.Error(), testKey) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("key lookup error is not surfaced", func(t *testing.T) {
		c, _, _ := testClient("http://127.0.0.1:1")
		c.Key = func() (string, error) { return "", errors.New("keychain says " + testKey) }
		_, err := c.Ask(context.Background(), sampleRequest())
		if CodeOf(err) != CodeTransport || strings.Contains(err.Error(), testKey) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestConnectionFailureRetries(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c, sleeps, _ := testClient(url)
	_, err := c.Ask(context.Background(), sampleRequest())
	if CodeOf(err) != CodeTransport || len(*sleeps) != 2 {
		t.Fatalf("err = %v sleeps = %v", err, *sleeps)
	}
}

func TestFixtureTransport(t *testing.T) {
	newFixtureClient := func() (*Client, *[]time.Duration, *MemoryBreaker) {
		c, sleeps, br := testClient("http://invalid.invalid")
		c.Config.Transport = TransportFixture
		c.Config.FixtureDir = "../../testdata/jev"
		c.Key = nil // fixture transport never needs a key
		return c, sleeps, br
	}
	req := func(id string) Request {
		return Request{QuestionSetID: id, State: "s", Questions: map[string]Question{"q": NoulQuestion{Instructions: "?"}}}
	}

	t.Run("success by set id", func(t *testing.T) {
		c, _, _ := newFixtureClient()
		resp, err := c.Ask(context.Background(), req("choice-high-confidence"))
		if err != nil {
			t.Fatal(err)
		}
		if a := resp.Answers["relevant_lines"].(ChoiceAnswer); a.Choice != "L000" || a.Confidence != 0.91 {
			t.Errorf("answer = %#v", a)
		}
	})
	t.Run("noul", func(t *testing.T) {
		c, _, _ := newFixtureClient()
		resp, err := c.Ask(context.Background(), req("noul-success"))
		if err != nil || resp.Answers["has_failure"].(NoulAnswer).Noul != 0.88 {
			t.Fatalf("resp = %#v err = %v", resp, err)
		}
	})
	t.Run("mixed compaction fixture", func(t *testing.T) {
		c, _, _ := newFixtureClient()
		resp, err := c.Ask(context.Background(), req("compaction.line-relevance"))
		if err != nil || len(resp.Answers) != 2 {
			t.Fatalf("resp = %#v err = %v", resp, err)
		}
	})
	t.Run("401 fixture opens breaker without retry", func(t *testing.T) {
		c, sleeps, br := newFixtureClient()
		_, err := c.Ask(context.Background(), req("error-401"))
		var je *Error
		if !errors.As(err, &je) || !je.Config || !br.IsOpen() || len(*sleeps) != 0 {
			t.Fatalf("err = %v open = %v sleeps = %v", err, br.IsOpen(), *sleeps)
		}
	})
	t.Run("429 sequence then success", func(t *testing.T) {
		c, sleeps, _ := newFixtureClient()
		resp, err := c.Ask(context.Background(), req("retry-429-then-success"))
		if err != nil || len(*sleeps) != 1 {
			t.Fatalf("err = %v sleeps = %v", err, *sleeps)
		}
		if resp.Answers["relevant_lines"].(ChoiceAnswer).Choice != "L000" {
			t.Errorf("resp = %#v", resp)
		}
	})
	t.Run("missing fixture is input rejection", func(t *testing.T) {
		c, _, _ := newFixtureClient()
		_, err := c.Ask(context.Background(), req("does-not-exist"))
		if CodeOf(err) != CodeRejected {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unsafe set id", func(t *testing.T) {
		c, _, _ := newFixtureClient()
		_, err := c.Ask(context.Background(), req("../../etc/passwd"))
		if CodeOf(err) != CodeRejected {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("hash lookup without set id", func(t *testing.T) {
		c, _, _ := newFixtureClient()
		_, err := c.Ask(context.Background(), req(""))
		if CodeOf(err) != CodeRejected {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestConfigFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	def := ConfigFromEnv(env(nil))
	if def.Endpoint != DefaultEndpoint || def.Model != "jev-latest" || def.Timeout != 4*time.Second || def.MaxRetries != 2 {
		t.Errorf("defaults = %#v", def)
	}
	got := ConfigFromEnv(env(map[string]string{
		"JEVKIT_ENDPOINT": "http://x", "JEVKIT_MODEL": "m", "JEVKIT_TIMEOUT_MS": "1500",
		"JEVKIT_MAX_RETRIES": "0", "JEVKIT_TRANSPORT": "fixture", "JEVKIT_FIXTURE_DIR": "/f",
	}))
	want := Config{Endpoint: "http://x", Model: "m", Timeout: 1500 * time.Millisecond, MaxRetries: 0, Transport: "fixture", FixtureDir: "/f"}
	if got != want {
		t.Errorf("got %#v want %#v", got, want)
	}
	if bad := ConfigFromEnv(env(map[string]string{"JEVKIT_TIMEOUT_MS": "abc", "JEVKIT_MAX_RETRIES": "-1"})); bad.Timeout != 4*time.Second || bad.MaxRetries != 2 {
		t.Errorf("bad = %#v", bad)
	}
}

func TestQuestionWireFormat(t *testing.T) {
	b, err := json.Marshal(ChoiceQuestion{Instructions: "i", Options: map[string]*string{"a": nil}})
	if err != nil || string(b) != `{"type":"choice","instructions":"i","options":{"a":null}}` {
		t.Errorf("choice = %s err = %v", b, err)
	}
	b, _ = json.Marshal(NoulQuestion{Instructions: "i"})
	if string(b) != `{"type":"noul","instructions":"i"}` {
		t.Errorf("noul = %s", b)
	}
}
