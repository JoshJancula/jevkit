package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/registry"
)

const testKey = "jev-mcp-secret-key-NEVER-EMIT"

// fixtureDir holds the recorded SystemOne responses shared with internal/jev.
const fixtureDir = "../../testdata/jev"

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// recorder wraps an Asker, recording requests and optionally overriding it.
type recorder struct {
	mu    sync.Mutex
	inner Asker
	calls []jev.Request
	err   error
}

func (r *recorder) Ask(ctx context.Context, req jev.Request) (*jev.Response, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return r.inner.Ask(ctx, req)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recorder) last() jev.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

func fixtureClient() Asker {
	cfg := jev.Config{Transport: jev.TransportFixture, FixtureDir: fixtureDir, Timeout: time.Second}
	return jev.New(cfg, func() (string, error) { return "", errors.New("fixture transport needs no key") })
}

// harness drives a Server in-process over io.Pipe, one JSON-RPC message per
// line, like the ralph bats suite drove the shell server over stdin.
type harness struct {
	t        *testing.T
	in       *io.PipeWriter
	lines    chan string
	stdout   *lockedBuf
	log      *lockedBuf
	rec      *recorder
	next     int
	init     response
	done     chan error
	auditDir string
}

type option func(*Config, *recorder)

func withUnavailable(reason string) option {
	return func(c *Config, _ *recorder) { c.Unavailable = func(context.Context) string { return reason } }
}

func withRedact(f func(string) (string, error)) option {
	return func(c *Config, _ *recorder) {
		c.Redact = func(s string) (string, []redact.Hit, error) {
			out, err := f(s)
			return out, nil, err
		}
	}
}

func withRedactJSON(f func(json.RawMessage) (json.RawMessage, []redact.Hit, error)) option {
	return func(c *Config, _ *recorder) { c.RedactJSON = f }
}

func withAskErr(err error) option { return func(_ *Config, r *recorder) { r.err = err } }

func withInner(a Asker) option { return func(_ *Config, r *recorder) { r.inner = a } }

func withAuditDir(dir string) option {
	return func(c *Config, _ *recorder) { c.AuditDir = dir }
}

func withDecider(d *registry.Decider) option {
	return func(c *Config, _ *recorder) { c.Decider = d }
}

// passthroughRedactJSON is the default fixture RedactJSON: it re-marshals
// raw to normalize whitespace, matching how a real Redactor leaves
// already-clean JSON alone, with no hits.
func passthroughRedactJSON(raw json.RawMessage) (json.RawMessage, []redact.Hit, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(v)
	return b, nil, err
}

func start(t *testing.T, opts ...option) *harness {
	t.Helper()
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{inner: fixtureClient()}
	cfg := Config{
		Decider:     &registry.Decider{Registry: reg, StateDir: t.TempDir()},
		Redact:      func(s string) (string, []redact.Hit, error) { return s, nil, nil },
		RedactJSON:  passthroughRedactJSON,
		Unavailable: func(context.Context) string { return "" },
		AuditDir:    t.TempDir(),
		Version:     "test",
	}
	h := &harness{t: t, rec: rec, log: &lockedBuf{}, stdout: &lockedBuf{}, lines: make(chan string, 64), done: make(chan error, 1)}
	for _, o := range opts {
		o(&cfg, rec)
	}
	h.auditDir = cfg.AuditDir
	cfg.Client = rec
	cfg.Log = h.log
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h.in = inW
	go func() { h.done <- srv.RunStdio(context.Background(), inR, outW); _ = outW.Close() }()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, 1<<22)
		for sc.Scan() {
			_, _ = h.stdout.Write(append(sc.Bytes(), '\n'))
			h.lines <- sc.Text()
		}
		close(h.lines)
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after stdin closed")
		}
	})

	h.init = h.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	})
	h.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return h
}

func (h *harness) send(msg map[string]any) {
	h.t.Helper()
	b, _ := json.Marshal(msg)
	if _, err := h.in.Write(append(b, '\n')); err != nil {
		h.t.Fatalf("write: %v", err)
	}
}

// response is one decoded JSON-RPC reply plus its raw result.
type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (h *harness) request(method string, params any) response {
	h.t.Helper()
	h.next++
	msg := map[string]any{"jsonrpc": "2.0", "id": h.next, "method": method}
	if params != nil {
		msg["params"] = params
	}
	h.send(msg)
	select {
	case line, ok := <-h.lines:
		if !ok {
			h.t.Fatal("server closed stdout")
		}
		var r response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			h.t.Fatalf("bad reply %q: %v", line, err)
		}
		return r
	case <-time.After(5 * time.Second):
		h.t.Fatalf("no reply to %s", method)
	}
	return response{}
}

// toolResult is a decoded tools/call result.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Structured map[string]any `json:"structuredContent"`
	IsError    bool           `json:"isError"`
}

func (h *harness) call(name string, args any) (toolResult, response) {
	h.t.Helper()
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	r := h.request("tools/call", params)
	var tr toolResult
	if r.Error == nil {
		if err := json.Unmarshal(r.Result, &tr); err != nil {
			h.t.Fatalf("bad result %s: %v", r.Result, err)
		}
	}
	return tr, r
}

func (h *harness) wantInvalid(name string, args any) {
	h.t.Helper()
	before := h.rec.count()
	_, r := h.call(name, args)
	if r.Error == nil || r.Error.Code != -32602 {
		h.t.Fatalf("%s(%v): want -32602, got error=%+v result=%s", name, args, r.Error, r.Result)
	}
	if h.rec.count() != before {
		h.t.Fatalf("%s(%v): rejected call still reached Jev", name, args)
	}
}

func mustGet[T any](t *testing.T, m map[string]any, path ...string) T {
	t.Helper()
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object", path, p)
		}
		cur = mm[p]
	}
	v, ok := cur.(T)
	if !ok {
		t.Fatalf("path %v = %#v, want %T", path, cur, v)
	}
	return v
}

// ---------------------------------------------------------------------------
// tools/list
// ---------------------------------------------------------------------------

func TestToolsListHasNoNextCursor(t *testing.T) {
	h := start(t)
	r := h.request("tools/list", nil)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Result, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["nextCursor"]; ok {
		t.Errorf("tools/list result carries nextCursor: %s", r.Result)
	}
	if _, ok := raw["tools"]; !ok {
		t.Errorf("tools/list result has no tools: %s", r.Result)
	}
	if strings.Contains(string(r.Result), "nextCursor") {
		t.Errorf("nextCursor appears somewhere in %s", r.Result)
	}
}

func listNames(t *testing.T, h *harness) []string {
	t.Helper()
	r := h.request("tools/list", nil)
	var res struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties is not false", tool.Name)
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		_, hasOptions := props["options"]
		wantOptions := tool.Name == "jev_classify_request" || tool.Name == "jev_rank_relevance"
		if strings.HasPrefix(tool.Name, "jev_classify") || tool.Name == "jev_rank_relevance" {
			if hasOptions != wantOptions {
				t.Errorf("%s: options in schema = %v, want %v", tool.Name, hasOptions, wantOptions)
			}
		}
		if wantOptions {
			req, _ := tool.InputSchema["required"].([]any)
			if !slices.Contains(req, any("options")) {
				t.Errorf("%s: options not required", tool.Name)
			}
		}
	}
	return names
}

func TestToolsListNamesSortedAndStableAcrossStarts(t *testing.T) {
	a := listNames(t, start(t))
	b := listNames(t, start(t))
	want := []string{"jev_ask", "jev_classify_failure", "jev_classify_request", "jev_developer_assess", "jev_rank_relevance"}
	if !slices.Equal(a, want) || !slices.Equal(b, want) {
		t.Errorf("names = %v then %v, want %v", a, b, want)
	}
}

// TestJevAskDescribesCriteriaNotOptions checks the tools/list catalog
// documents "criteria" as the way to shape a jev_ask question, and only
// mentions the legacy "options" alias as deprecated.
func TestJevAskDescribesCriteriaNotOptions(t *testing.T) {
	h := start(t)
	r := h.request("tools/list", nil)
	var res struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	var desc string
	for _, tool := range res.Tools {
		if tool.Name == "jev_ask" {
			desc = tool.Description
		}
	}
	if desc == "" {
		t.Fatal("jev_ask not in tools/list")
	}
	if !strings.Contains(desc, "criteria") {
		t.Errorf("description does not mention criteria: %q", desc)
	}
	if !strings.Contains(desc, "DEPRECATED") || !strings.Contains(desc, "options") {
		t.Errorf("description does not mark options deprecated: %q", desc)
	}
	if idx := strings.Index(desc, "Detailed choice example"); idx == -1 || !strings.Contains(desc[idx:idx+300], "billing") {
		t.Errorf("description missing copyable detailed choice example: %q", desc)
	}
}

func TestInitializeAdvertisesToolsOnly(t *testing.T) {
	h := start(t)
	var res struct {
		ServerInfo   map[string]any `json:"serverInfo"`
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(h.init.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.ServerInfo["name"] != "jevkit" || res.ServerInfo["version"] != "test" {
		t.Errorf("serverInfo = %v", res.ServerInfo)
	}
	if _, ok := res.Capabilities["tools"]; !ok {
		t.Errorf("capabilities = %v", res.Capabilities)
	}
	if _, ok := res.Capabilities["logging"]; ok {
		t.Errorf("logging capability advertised: %v", res.Capabilities)
	}
	if r := h.request("ping", nil); r.Error != nil {
		t.Errorf("ping: %v", r.Error)
	}
}

// ---------------------------------------------------------------------------
// curated tools
// ---------------------------------------------------------------------------

func TestCuratedToolsReturnAnswerDecisionSetAndVersion(t *testing.T) {
	cases := []struct {
		tool, set string
		args      map[string]any
		answerKey string
		wantOpts  []string
	}{
		{"jev_classify_request", "graph.router-confidence", map[string]any{"state": "implement the router target", "options": []string{"implement", "investigate", "ask"}}, "target", []string{"ask", "implement", "investigate"}},
		{"jev_classify_failure", "graph.failure-class", map[string]any{"state": "stage failed: tests"}, "failure_class", nil},
		{"jev_rank_relevance", "compaction.line-relevance", map[string]any{"state": "L000 noise", "options": []string{"L000", "L001"}}, "relevant_lines", []string{"L000", "L001"}},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			h := start(t)
			tr, r := h.call(c.tool, c.args)
			if r.Error != nil {
				t.Fatalf("error: %+v", r.Error)
			}
			if tr.IsError {
				t.Fatal("isError is true")
			}
			if got := mustGet[string](t, tr.Structured, "questionSetId"); got != c.set {
				t.Errorf("questionSetId = %q", got)
			}
			if got := mustGet[string](t, tr.Structured, "registryVersion"); got != "1" {
				t.Errorf("registryVersion = %q", got)
			}
			if _, ok := mustGet[map[string]any](t, tr.Structured, "answer")[c.answerKey]; !ok {
				t.Errorf("answer lacks %q: %v", c.answerKey, tr.Structured["answer"])
			}
			switch mustGet[string](t, tr.Structured, "decision", "decision") {
			case "act", "gather", "fallback":
			default:
				t.Errorf("decision = %v", tr.Structured["decision"])
			}
			if len(tr.Content) != 1 || tr.Content[0].Type != "text" || !strings.Contains(tr.Content[0].Text, "questionSetId="+c.set) {
				t.Errorf("content = %+v", tr.Content)
			}

			// The set id is fixed by the tool, and choice options come from the call.
			req := h.rec.last()
			if req.QuestionSetID != c.set {
				t.Errorf("request set = %q", req.QuestionSetID)
			}
			if c.wantOpts != nil {
				choice := req.Questions[c.answerKey].(jev.ChoiceQuestion)
				var got []string
				for k := range choice.Criteria {
					got = append(got, k)
				}
				slices.Sort(got)
				if !slices.Equal(got, c.wantOpts) {
					t.Errorf("options = %v, want %v", got, c.wantOpts)
				}
			}
		})
	}
}

func TestClassifyRequestFixtureDecision(t *testing.T) {
	h := start(t)
	tr, r := h.call("jev_classify_request", map[string]any{"state": "implement the router target", "options": []string{"implement", "investigate"}})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	if got := mustGet[string](t, tr.Structured, "answer", "target", "choice"); got != "implement" {
		t.Errorf("answer.target.choice = %q", got)
	}
	if got := mustGet[string](t, tr.Structured, "decision", "decision"); got != "act" {
		t.Errorf("decision = %q", got)
	}
	if got := mustGet[string](t, tr.Structured, "decision", "chosen"); got != "implement" {
		t.Errorf("chosen = %q", got)
	}
}

func TestCuratedInvalidParams(t *testing.T) {
	long := make([]string, 256)
	for i := range long {
		long[i] = fmt.Sprintf("o%d", i)
	}
	h := start(t)
	bad := []struct {
		name string
		tool string
		args any
	}{
		{"unknown key", "jev_classify_request", map[string]any{"state": "ok", "options": []string{"implement"}, "extra": "nope"}},
		{"no arguments", "jev_classify_request", nil},
		{"empty object", "jev_classify_request", map[string]any{}},
		{"arguments not an object", "jev_classify_request", []string{"state"}},
		{"missing state", "jev_classify_request", map[string]any{"options": []string{"a"}}},
		{"empty state", "jev_classify_request", map[string]any{"state": "", "options": []string{"a"}}},
		{"state not a string", "jev_classify_request", map[string]any{"state": 5, "options": []string{"a"}}},
		{"missing options", "jev_classify_request", map[string]any{"state": "ok"}},
		{"empty options", "jev_classify_request", map[string]any{"state": "ok", "options": []string{}}},
		{"empty option string", "jev_classify_request", map[string]any{"state": "ok", "options": []string{"a", ""}}},
		{"options not strings", "jev_classify_request", map[string]any{"state": "ok", "options": []any{1}}},
		{"options not an array", "jev_classify_request", map[string]any{"state": "ok", "options": "a"}},
		{"256 options", "jev_classify_request", map[string]any{"state": "ok", "options": long}},
		{"rank missing options", "jev_rank_relevance", map[string]any{"state": "L1"}},
		{"failure rejects options", "jev_classify_failure", map[string]any{"state": "x", "options": []string{"a"}}},
		{"failure unknown key", "jev_classify_failure", map[string]any{"state": "x", "extra": 1}},
		{"failure no state", "jev_classify_failure", map[string]any{}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) { h.wantInvalid(c.tool, c.args) })
	}
	// 255 options is the cap and is accepted.
	if _, r := h.call("jev_classify_request", map[string]any{"state": "ok", "options": long[:255]}); r.Error != nil {
		t.Errorf("255 options rejected: %+v", r.Error)
	}
}

// ---------------------------------------------------------------------------
// unavailable
// ---------------------------------------------------------------------------

func TestUnavailableIsSuccessfulEnvelope(t *testing.T) {
	h := start(t, withUnavailable("no-key"))
	calls := map[string]map[string]any{
		"jev_classify_request": {"state": "route me", "options": []string{"implement", "investigate"}},
		"jev_classify_failure": {"state": "stage failed"},
		"jev_rank_relevance":   {"state": "L000 noise", "options": []string{"L000"}},
		"jev_ask":              {"state": "s", "questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": "ok?"}}},
	}
	for tool, args := range calls {
		tr, r := h.call(tool, args)
		if r.Error != nil {
			t.Fatalf("%s: error %+v", tool, r.Error)
		}
		if tr.IsError {
			t.Errorf("%s: isError is true", tool)
		}
		if mustGet[bool](t, tr.Structured, "available") {
			t.Errorf("%s: available is true", tool)
		}
		if mustGet[string](t, tr.Structured, "reason") != "no-key" {
			t.Errorf("%s: reason = %v", tool, tr.Structured["reason"])
		}
		if len(tr.Content) != 1 || !strings.Contains(tr.Content[0].Text, "Jev unavailable") {
			t.Errorf("%s: content = %+v", tool, tr.Content)
		}
	}
	if h.rec.count() != 0 {
		t.Errorf("unavailable calls reached Jev %d times", h.rec.count())
	}
	// Bad params still win over unavailability.
	h.wantInvalid("jev_classify_request", map[string]any{"state": "x", "bogus": 1})
}

func TestDeclinedByClientIsUnavailable(t *testing.T) {
	h := start(t, withAskErr(&jev.Error{Code: jev.CodeDeclined, Reason: "breaker-open"}))
	tr, r := h.call("jev_classify_failure", map[string]any{"state": "boom"})
	if r.Error != nil || tr.IsError || mustGet[bool](t, tr.Structured, "available") {
		t.Fatalf("want soft unavailable, got %+v %+v", r.Error, tr)
	}
	if mustGet[string](t, tr.Structured, "reason") != "breaker-open" {
		t.Errorf("reason = %v", tr.Structured["reason"])
	}
	h = start(t, withAskErr(&jev.Error{Code: jev.CodeTransport, Reason: "no api key"}))
	tr, _ = h.call("jev_classify_failure", map[string]any{"state": "boom"})
	if mustGet[bool](t, tr.Structured, "available") {
		t.Error("no api key should be unavailable")
	}
}

// ---------------------------------------------------------------------------
// errors, redaction and secrecy
// ---------------------------------------------------------------------------

func TestClientErrorsMapToRPCErrors(t *testing.T) {
	wrapped := errors.New("response body: " + testKey)
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"rejected", &jev.Error{Code: jev.CodeRejected, Reason: "input rejected", Err: wrapped}, -32602},
		{"transport", &jev.Error{Code: jev.CodeTransport, Reason: "http-500", Err: wrapped}, -32000},
		{"plain", wrapped, -32000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, withAskErr(c.err))
			_, r := h.call("jev_classify_failure", map[string]any{"state": "x"})
			if r.Error == nil || r.Error.Code != c.code {
				t.Fatalf("want code %d, got %+v", c.code, r.Error)
			}
			if strings.Contains(r.Error.Message, testKey) || strings.Contains(h.log.String(), testKey) {
				t.Errorf("wrapped detail leaked: %q / %q", r.Error.Message, h.log.String())
			}
		})
	}
}

func TestStateIsRedactedBeforeSend(t *testing.T) {
	scrub := func(s string) (string, error) { return strings.ReplaceAll(s, testKey, "[REDACTED]"), nil }
	h := start(t, withRedact(scrub))
	if _, r := h.call("jev_classify_failure", map[string]any{"state": "failure with " + testKey}); r.Error != nil {
		t.Fatal(r.Error)
	}
	if got := h.rec.last().State; strings.Contains(got, testKey) || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("state sent = %q", got)
	}

	h = start(t, withRedact(func(string) (string, error) { return "", errors.New("config invalid: " + testKey) }))
	h.wantInvalid("jev_classify_failure", map[string]any{"state": "x"})
	h.wantInvalid("jev_ask", map[string]any{"state": "x", "questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": "i"}}})
	if strings.Contains(h.log.String(), testKey) {
		t.Errorf("redaction error detail logged: %q", h.log.String())
	}
}

func TestKeyNeverOnStdoutOrLog(t *testing.T) {
	scrub := func(s string) (string, error) { return strings.ReplaceAll(s, testKey, "[REDACTED]"), nil }
	h := start(t, withRedact(scrub))
	h.request("tools/list", nil)
	tr, r := h.call("jev_classify_request", map[string]any{"state": "route with " + testKey, "options": []string{"implement", "investigate"}})
	if r.Error != nil || mustGet[string](t, tr.Structured, "questionSetId") != "graph.router-confidence" {
		t.Fatalf("call failed: %+v %+v", r.Error, tr)
	}
	if strings.Contains(h.stdout.String(), testKey) || strings.Contains(h.log.String(), testKey) {
		t.Errorf("key leaked:\nstdout=%s\nlog=%s", h.stdout.String(), h.log.String())
	}
}

func TestStdoutIsProtocolOnlyAndLogsGoToLog(t *testing.T) {
	h := start(t)
	h.call("jev_classify_failure", map[string]any{"state": "boom"})
	for _, line := range strings.Split(strings.TrimSpace(h.stdout.String()), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil || v["jsonrpc"] != "2.0" {
			t.Errorf("stdout line is not a JSON-RPC message: %q", line)
		}
	}
	if !strings.Contains(h.log.String(), "tool=jev_classify_failure") {
		t.Errorf("no tool log line on the log writer: %q", h.log.String())
	}
	// Logs name the tool and outcome, never the state text.
	h.call("jev_classify_failure", map[string]any{"state": "very-unique-state-text"})
	if strings.Contains(h.log.String(), "very-unique-state-text") {
		t.Error("state text was logged")
	}
}

// ---------------------------------------------------------------------------
// jev_ask
// ---------------------------------------------------------------------------

type staticAsker struct{ resp *jev.Response }

func (s staticAsker) Ask(context.Context, jev.Request) (*jev.Response, error) { return s.resp, nil }

func TestJevAskIsRawAndUnregistered(t *testing.T) {
	resp := &jev.Response{
		Model: "jev-1.13.0",
		Usage: jev.Usage{InputTokens: 42, OutputTokens: 7},
		Answers: map[string]jev.Answer{
			"pick": jev.ChoiceAnswer{Choice: "a", Probabilities: map[string]float64{"a": 0.9}, Confidence: 0.9},
			"ok":   jev.NoulAnswer{Noul: 0.7},
			"n":    jev.ScoreAnswer{Score: 3, Confidence: 0.5, Legend: []string{"none", "lots"}, Distribution: map[string]float64{"none": 0.2, "lots": 0.8}},
		},
	}
	h := start(t, withInner(staticAsker{resp}))
	tr, r := h.call("jev_ask", map[string]any{"state": "raw state", "questions": map[string]any{
		"pick": map[string]any{"type": "choice", "instructions": "which?", "criteria": map[string]any{"a": nil, "b": "bee"}},
		"ok":   map[string]any{"type": "noul", "instructions": "fine?"},
		"n":    map[string]any{"type": "score", "instructions": "how many?", "criteria": []string{"none", "lots"}},
	}})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	if !mustGet[bool](t, tr.Structured, "unversioned") || !mustGet[bool](t, tr.Structured, "unaudited") || !mustGet[bool](t, tr.Structured, "unregistered") {
		t.Errorf("flags = %v", tr.Structured)
	}
	if got := mustGet[string](t, tr.Structured, "answer", "pick", "choice"); got != "a" {
		t.Errorf("choice = %q", got)
	}
	if got := mustGet[string](t, tr.Structured, "answer", "pick", "type"); got != "choice" {
		t.Errorf("pick type = %q", got)
	}
	if got := mustGet[float64](t, tr.Structured, "answer", "ok", "noul"); got != 0.7 {
		t.Errorf("noul = %v", got)
	}
	if got := mustGet[string](t, tr.Structured, "model"); got != "jev-1.13.0" {
		t.Errorf("model = %q", got)
	}
	if got := mustGet[float64](t, tr.Structured, "usage", "input_tokens"); got != 42 {
		t.Errorf("usage.input_tokens = %v", got)
	}
	legend := mustGet[[]any](t, tr.Structured, "answer", "n", "legend")
	if len(legend) != 2 {
		t.Errorf("legend = %v", legend)
	}
	for _, k := range []string{"decision", "questionSetId", "registryVersion"} {
		if _, ok := tr.Structured[k]; ok {
			t.Errorf("raw tool returned %q", k)
		}
	}
	if len(tr.Content) != 1 || !strings.Contains(tr.Content[0].Text, "unregistered") {
		t.Errorf("content = %+v", tr.Content)
	}
	req := h.rec.last()
	if req.QuestionSetID != "" || req.State != "raw state" || len(req.Questions) != 3 {
		t.Errorf("request = %+v", req)
	}
}

func TestJevAskOptionsAliasNormalizesToNullCriteria(t *testing.T) {
	resp := &jev.Response{Answers: map[string]jev.Answer{"pick": jev.ChoiceAnswer{Choice: "a"}}}
	h := start(t, withInner(staticAsker{resp}))
	tr, r := h.call("jev_ask", map[string]any{"state": "s", "questions": map[string]any{
		"pick": map[string]any{"type": "choice", "instructions": "which?", "options": []string{"a", "b"}},
	}})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	cq, ok := h.rec.last().Questions["pick"].(jev.ChoiceQuestion)
	if !ok {
		t.Fatalf("question = %#v", h.rec.last().Questions["pick"])
	}
	if len(cq.Criteria) != 2 || string(cq.Criteria["a"]) != "null" || string(cq.Criteria["b"]) != "null" {
		t.Errorf("criteria = %v", cq.Criteria)
	}
}

func TestJevAskInvalidParams(t *testing.T) {
	h := start(t, withInner(staticAsker{&jev.Response{}}))
	q := map[string]any{"q": map[string]any{"type": "noul", "instructions": "i"}}
	for name, args := range map[string]any{
		"no arguments":           nil,
		"missing state":          map[string]any{"questions": q},
		"blank state":            map[string]any{"state": "", "questions": q},
		"numeric state":          map[string]any{"state": 1, "questions": q},
		"missing question":       map[string]any{"state": "s"},
		"questions array":        map[string]any{"state": "s", "questions": []any{}},
		"empty questions":        map[string]any{"state": "s", "questions": map[string]any{}},
		"unknown key":            map[string]any{"state": "s", "questions": q, "options": []string{"a"}},
		"bad type":               map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "yesno", "instructions": "i"}}},
		"unknown field":          map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": "i", "extra": 1}}},
		"choice no criteria":     map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "choice", "instructions": "i"}}},
		"score one level":        map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "score", "instructions": "i", "criteria": []string{"only"}}}},
		"question string":        map[string]any{"state": "s", "questions": map[string]any{"q": "noul"}},
		"blank instructions":     map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": ""}}},
		"numeric instructions":   map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": 1}}},
		"options on non-choice":  map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": "i", "options": []string{"a"}}}},
		"options plus criteria":  map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "choice", "instructions": "i", "options": []string{"a"}, "criteria": map[string]any{"a": nil}}}},
		"empty option label":     map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "choice", "instructions": "i", "options": []string{""}}}},
		"invalid criteria shape": map[string]any{"state": "s", "questions": map[string]any{"q": map[string]any{"type": "choice", "instructions": "i", "criteria": []string{"a"}}}},
	} {
		t.Run(name, func(t *testing.T) { h.wantInvalid("jev_ask", args) })
	}
}

func TestJevAskStructuredStateAndInstructions(t *testing.T) {
	resp := &jev.Response{Answers: map[string]jev.Answer{"ok": jev.NoulAnswer{Noul: 0.5}}}
	h := start(t, withInner(staticAsker{resp}))
	tr, r := h.call("jev_ask", map[string]any{
		"state": map[string]any{"summary": "structured state", "count": 3},
		"questions": map[string]any{
			"ok": map[string]any{"type": "noul", "instructions": []any{"step one", "step two"}},
		},
	})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	req := h.rec.last()
	var state map[string]any
	if err := json.Unmarshal([]byte(req.State), &state); err != nil {
		t.Fatalf("state not JSON: %q: %v", req.State, err)
	}
	if state["summary"] != "structured state" {
		t.Errorf("state = %v", state)
	}
	nq, ok := req.Questions["ok"].(jev.NoulQuestion)
	if !ok {
		t.Fatalf("question = %#v", req.Questions["ok"])
	}
	var instr []any
	if err := json.Unmarshal([]byte(nq.Instructions), &instr); err != nil || len(instr) != 2 {
		t.Errorf("instructions = %q (err=%v)", nq.Instructions, err)
	}
}

func TestJevAskStructuredStateRedactedAndInvalidRejected(t *testing.T) {
	h := start(t, withRedactJSON(func(raw json.RawMessage) (json.RawMessage, []redact.Hit, error) {
		s := strings.ReplaceAll(string(raw), testKey, `"[REDACTED]"`)
		return json.RawMessage(s), []redact.Hit{{RuleID: "builtin.key", Count: 1}}, nil
	}), withInner(staticAsker{&jev.Response{Answers: map[string]jev.Answer{"ok": jev.NoulAnswer{Noul: 0.1}}}}))
	q := map[string]any{"ok": map[string]any{"type": "noul", "instructions": "fine?"}}
	tr, r := h.call("jev_ask", map[string]any{"state": map[string]any{"secret": testKey}, "questions": q})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	if strings.Contains(h.rec.last().State, testKey) {
		t.Errorf("secret leaked into state: %q", h.rec.last().State)
	}

	h2 := start(t, withRedactJSON(func(json.RawMessage) (json.RawMessage, []redact.Hit, error) {
		return nil, nil, errors.New("config invalid: " + testKey)
	}))
	h2.wantInvalid("jev_ask", map[string]any{"state": map[string]any{"a": 1}, "questions": q})
	if strings.Contains(h2.log.String(), testKey) {
		t.Errorf("redaction error detail logged: %q", h2.log.String())
	}
}

func TestJevAskAuditEntry(t *testing.T) {
	dir := t.TempDir()
	resp := &jev.Response{Answers: map[string]jev.Answer{
		"ok": jev.NoulAnswer{Noul: 0.5},
	}}
	h := start(t, withInner(staticAsker{resp}), withAuditDir(dir), withRedact(func(s string) (string, error) {
		return strings.ReplaceAll(s, testKey, "[REDACTED]"), nil
	}))
	tr, r := h.call("jev_ask", map[string]any{
		"state": "secret is " + testKey,
		"questions": map[string]any{
			"ok": map[string]any{"type": "noul", "instructions": "fine?"},
		},
	})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	data, err := os.ReadFile(AuditPath(dir))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if strings.Contains(string(data), testKey) {
		t.Errorf("audit log leaked payload text: %q", data)
	}
	var entry AuditEntry
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatalf("bad audit line: %v", err)
	}
	if entry.Tool != "jev_ask" || !entry.Unregistered {
		t.Errorf("entry = %+v", entry)
	}
	if entry.Caller != "test/1" {
		t.Errorf("caller = %q", entry.Caller)
	}
	if entry.StateBytes == 0 || len(entry.Questions) != 1 || entry.Questions[0].ID != "ok" || entry.Questions[0].Type != "noul" {
		t.Errorf("entry = %+v", entry)
	}
	if entry.Timestamp == "" {
		t.Errorf("timestamp missing")
	}
}

// ---------------------------------------------------------------------------
// jev_developer_assess
// ---------------------------------------------------------------------------

// TestDeveloperAssessToolDiscoverable checks tools/list documents every
// built-in developer.* assessment, its shared state fields, and the
// no-automatic-action guarantee, so the dispatcher is self-describing.
func TestDeveloperAssessToolDiscoverable(t *testing.T) {
	h := start(t)
	r := h.request("tools/list", nil)
	var res struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	var desc string
	for _, tool := range res.Tools {
		if tool.Name == "jev_developer_assess" {
			desc = tool.Description
		}
	}
	if desc == "" {
		t.Fatal("jev_developer_assess not in tools/list")
	}
	for _, id := range developerAssessmentIDs() {
		if !strings.Contains(desc, id) {
			t.Errorf("description missing assessment %q: %q", id, desc)
		}
	}
	for _, field := range []string{"diffSummary", "affectedAreas", "testOutput", "environment", "constraints"} {
		if !strings.Contains(desc, field) {
			t.Errorf("description missing state field %q", field)
		}
	}
	if !strings.Contains(desc, "modifies code") {
		t.Errorf("description does not state the no-automatic-action guarantee: %q", desc)
	}
}

func developerAssessState(id string) map[string]any {
	switch id {
	case "developer.change-risk":
		return map[string]any{"diffSummary": "widen auth token TTL", "affectedAreas": []string{"auth"}}
	case "developer.failure-triage":
		return map[string]any{"testOutput": "FAIL TestLogin: timeout after 30s"}
	case "developer.test-priority":
		return map[string]any{"diffSummary": "renamed internal helper, no behavior change"}
	case "developer.review-disposition":
		return map[string]any{"diffSummary": "removed dead code path"}
	case "developer.release-readiness":
		return map[string]any{"testOutput": "all tests passed", "constraints": "no DB migration"}
	}
	return nil
}

// TestDeveloperAssessRequestConstruction drives each built-in assessment end
// to end: the right question set is called, the answer round-trips, and the
// response carries only data (answer/decision/assessment/registryVersion),
// never an action field.
func TestDeveloperAssessRequestConstruction(t *testing.T) {
	cases := []struct {
		id       string
		question string
		answer   jev.Answer
	}{
		{"developer.change-risk", "risk", jev.ScoreAnswer{Score: 3, Confidence: 0.9}},
		{"developer.failure-triage", "cause", jev.ChoiceAnswer{Choice: "regression", Confidence: 0.9}},
		{"developer.test-priority", "priority", jev.ChoiceAnswer{Choice: "targeted-tests", Confidence: 0.9}},
		{"developer.review-disposition", "disposition", jev.ChoiceAnswer{Choice: "no-finding", Confidence: 0.9}},
		{"developer.release-readiness", "ready", jev.NoulAnswer{Noul: 0.9}},
	}
	if len(cases) != len(developerAssessments) {
		t.Fatalf("cases cover %d assessments, want %d", len(cases), len(developerAssessments))
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			resp := &jev.Response{Answers: map[string]jev.Answer{c.question: c.answer}}
			h := start(t, withInner(staticAsker{resp}))
			tr, r := h.call("jev_developer_assess", map[string]any{"assessment": c.id, "state": developerAssessState(c.id)})
			if r.Error != nil || tr.IsError {
				t.Fatalf("error: %+v", r.Error)
			}
			if got := mustGet[string](t, tr.Structured, "assessment"); got != c.id {
				t.Errorf("assessment = %q, want %q", got, c.id)
			}
			if got := mustGet[string](t, tr.Structured, "registryVersion"); got != "1" {
				t.Errorf("registryVersion = %q", got)
			}
			switch mustGet[string](t, tr.Structured, "decision", "decision") {
			case "act", "gather", "fallback":
			default:
				t.Errorf("decision = %v", tr.Structured["decision"])
			}
			for k := range tr.Structured {
				switch k {
				case "answer", "decision", "assessment", "registryVersion":
				default:
					t.Errorf("unexpected structured key %q (no automatic-action fields expected)", k)
				}
			}
			req := h.rec.last()
			if req.QuestionSetID != c.id {
				t.Errorf("request set = %q, want %q", req.QuestionSetID, c.id)
			}
		})
	}
}

// TestDeveloperAssessConfidenceThresholds checks the shared registry policy
// (act >= 0.85, gather >= 0.6, else fallback) applies to a developer.*
// assessment exactly like it does to the curated tools.
func TestDeveloperAssessConfidenceThresholds(t *testing.T) {
	for _, c := range []struct {
		confidence float64
		want       string
	}{
		{0.9, "act"},
		{0.7, "gather"},
		{0.3, "fallback"},
	} {
		t.Run(c.want, func(t *testing.T) {
			resp := &jev.Response{Answers: map[string]jev.Answer{"risk": jev.ScoreAnswer{Score: 2, Confidence: c.confidence}}}
			h := start(t, withInner(staticAsker{resp}))
			tr, r := h.call("jev_developer_assess", map[string]any{
				"assessment": "developer.change-risk",
				"state":      developerAssessState("developer.change-risk"),
			})
			if r.Error != nil || tr.IsError {
				t.Fatalf("error: %+v", r.Error)
			}
			if got := mustGet[string](t, tr.Structured, "decision", "decision"); got != c.want {
				t.Errorf("confidence %v decision = %q, want %q", c.confidence, got, c.want)
			}
		})
	}
}

// TestDeveloperAssessShadowLogsWouldHaveDecision checks JEVKIT_SHADOW=1
// forces a fallback response while the logged decisions.jsonl record keeps
// the would-have decision, the same shadow-rollout mechanism documented for
// calibrating a new registry set before trusting it live.
func TestDeveloperAssessShadowLogsWouldHaveDecision(t *testing.T) {
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	dec := &registry.Decider{Registry: reg, StateDir: stateDir, Getenv: func(k string) string {
		if k == "JEVKIT_SHADOW" {
			return "1"
		}
		return ""
	}}
	resp := &jev.Response{Answers: map[string]jev.Answer{"risk": jev.ScoreAnswer{Score: 4, Confidence: 0.95}}}
	h := start(t, withInner(staticAsker{resp}), withDecider(dec))
	tr, r := h.call("jev_developer_assess", map[string]any{
		"assessment": "developer.change-risk",
		"state":      developerAssessState("developer.change-risk"),
	})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	if got := mustGet[string](t, tr.Structured, "decision", "decision"); got != "fallback" {
		t.Errorf("shadow-mode returned decision = %q, want fallback", got)
	}
	data, err := os.ReadFile(registry.DecisionsPath(stateDir))
	if err != nil {
		t.Fatalf("read decisions log: %v", err)
	}
	var logged registry.Decision
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &logged); err != nil {
		t.Fatalf("bad decision line: %v", err)
	}
	if logged.Decision != "act" {
		t.Errorf("logged would-have decision = %q, want act", logged.Decision)
	}
}

// TestDeveloperAssessRedactedBeforeTransport checks structured state reaches
// the wire only after JSON-aware redaction, the same guarantee jev_ask
// carries.
func TestDeveloperAssessRedactedBeforeTransport(t *testing.T) {
	h := start(t, withRedactJSON(func(raw json.RawMessage) (json.RawMessage, []redact.Hit, error) {
		s := strings.ReplaceAll(string(raw), testKey, `"[REDACTED]"`)
		return json.RawMessage(s), []redact.Hit{{RuleID: "builtin.key", Count: 1}}, nil
	}), withInner(staticAsker{&jev.Response{Answers: map[string]jev.Answer{"ready": jev.NoulAnswer{Noul: 0.9}}}}))
	tr, r := h.call("jev_developer_assess", map[string]any{
		"assessment": "developer.release-readiness",
		"state":      map[string]any{"testOutput": "ok", "constraints": testKey},
	})
	if r.Error != nil || tr.IsError {
		t.Fatalf("error: %+v", r.Error)
	}
	if strings.Contains(h.rec.last().State, testKey) {
		t.Errorf("secret leaked into state: %q", h.rec.last().State)
	}
}

// TestDeveloperAssessInvalidParams checks unknown assessments, unknown state
// keys, missing required state, and non-structured state are all rejected
// before Jev is called.
func TestDeveloperAssessInvalidParams(t *testing.T) {
	h := start(t, withInner(staticAsker{&jev.Response{}}))
	for name, args := range map[string]any{
		"no arguments":            nil,
		"missing assessment":      map[string]any{"state": map[string]any{"diffSummary": "x", "affectedAreas": []string{"a"}}},
		"unknown assessment":      map[string]any{"assessment": "developer.nope", "state": map[string]any{"diffSummary": "x"}},
		"missing state":           map[string]any{"assessment": "developer.change-risk"},
		"state not object":        map[string]any{"assessment": "developer.change-risk", "state": "diff"},
		"unknown state key":       map[string]any{"assessment": "developer.change-risk", "state": map[string]any{"diffSummary": "x", "affectedAreas": []string{"a"}, "bogus": 1}},
		"missing required key":    map[string]any{"assessment": "developer.change-risk", "state": map[string]any{"diffSummary": "x"}},
		"registry set not a dev.": map[string]any{"assessment": "graph.failure-class", "state": map[string]any{"diffSummary": "x"}},
		"unknown top-level key":   map[string]any{"assessment": "developer.change-risk", "state": developerAssessState("developer.change-risk"), "options": []string{"a"}},
	} {
		t.Run(name, func(t *testing.T) { h.wantInvalid("jev_developer_assess", args) })
	}
}

// ---------------------------------------------------------------------------
// protocol edges and construction
// ---------------------------------------------------------------------------

func TestUnknownToolAndMethod(t *testing.T) {
	h := start(t)
	if _, r := h.call("jev_nope", map[string]any{}); r.Error == nil {
		t.Errorf("unknown tool succeeded: %s", r.Result)
	}
	if r := h.request("frobnicate", nil); r.Error == nil || r.Error.Code != -32601 {
		t.Errorf("unknown method: %+v", r.Error)
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	reg, _ := registry.Load()
	full := Config{
		Decider:     &registry.Decider{Registry: reg},
		Client:      staticAsker{},
		Redact:      func(s string) (string, []redact.Hit, error) { return s, nil, nil },
		RedactJSON:  passthroughRedactJSON,
		Unavailable: func(context.Context) string { return "" },
	}
	if _, err := New(full); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Config){
		"decider":     func(c *Config) { c.Decider = nil },
		"registry":    func(c *Config) { c.Decider = &registry.Decider{} },
		"client":      func(c *Config) { c.Client = nil },
		"redact":      func(c *Config) { c.Redact = nil },
		"redactJSON":  func(c *Config) { c.RedactJSON = nil },
		"unavailable": func(c *Config) { c.Unavailable = nil },
	} {
		c := full
		mut(&c)
		if _, err := New(c); err == nil {
			t.Errorf("New without %s succeeded", name)
		}
	}
}
