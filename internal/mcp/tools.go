package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/registry"
)

// JSON-RPC error codes the tools return.
const (
	codeInvalidParams = jsonrpc.CodeInvalidParams // -32602
	codeServer        = -32000
)

const msgStateRejected = "state rejected before transport (redaction or size)"

func rpcErr(code int64, format string, args ...any) error {
	return &jsonrpc.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) error { return rpcErr(codeInvalidParams, format, args...) }

// needsOptions reports whether a set declares a choice question with no fixed
// criteria; its options are then supplied per call (router allowed targets,
// compaction line ids), because SystemOne rejects a choice with none.
func needsOptions(set *registry.Set) bool {
	for _, q := range set.Questions {
		if q.Type == "choice" && emptyCriteria(q.Criteria) {
			return true
		}
	}
	return false
}

func emptyCriteria(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) != nil || len(m) == 0
}

func (s *Server) curatedTool(name, setID, blurb string) (*sdk.Tool, error) {
	set, ok := s.cfg.Decider.Registry.Set(setID)
	if !ok {
		return nil, fmt.Errorf("mcp: registry has no question set %q", setID)
	}
	props := map[string]any{
		"state": map[string]any{"type": "string", "description": "State text for the question set (task text, logs, evidence lines); it is redacted before it is sent."},
	}
	required := []string{"state"}
	if needsOptions(set) {
		props["options"] = map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string", "minLength": 1},
			"minItems":    1,
			"maxItems":    jev.MaxChoiceOptions,
			"description": "REQUIRED for this tool: the closed set of choices. This question set's choice options are populated at call time (SystemOne rejects a choice with zero options).",
		}
		required = []string{"state", "options"}
	}
	return &sdk.Tool{
		Name: name,
		Description: fmt.Sprintf("%s via the versioned %s question set. Returns the typed answer plus the registry policy decision (act/gather/fallback) and the registry version.",
			blurb, setID),
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           props,
			"required":             required,
		},
	}, nil
}

func (s *Server) askTool() *sdk.Tool {
	return &sdk.Tool{
		Name: "jev_ask",
		Description: `Send an unregistered, unversioned SystemOne request: a state value plus one or more independently-answered named questions. Prefer the curated tools (jev_classify_request, jev_classify_failure, jev_rank_relevance) for auditable, versioned decisions; jev_ask escapes the registry only, not the data policy. The same redaction (JSON-aware for structured input) and request-size limits apply, every call is marked unregistered, and a privacy-safe local audit line is recorded (timestamp, caller, question ids/types, byte counts and redaction rule counts, never payload text). Arbitrary answers are returned as data only; jevkit never turns a jev_ask answer into an automatic tool action.

state and each question's instructions accept a plain string, or literal JSON (an object or array) for structured context. Each question is {type: "noul"|"choice"|"score", instructions, criteria}: criteria is required for choice (1-255 entries) and score (2-10 ordered levels, low to high), optional for noul ("true"/"false" entries); each criteria value may itself be a string, object, array or null.

Detailed choice example (a criteria object with a rubric per option, not just bare labels):
{"state": "Customer says the invoice total looks wrong", "questions": {"route": {"type": "choice", "instructions": "Which team should handle this?", "criteria": {"billing": "Payment, invoice or refund issues", "technical": "Product defects or errors", "sales": "Pricing or new purchase questions"}}}}

DEPRECATED: a question's "options" field (an array of bare choice labels) is a compatibility alias that expands to null-valued Choice criteria; do not use it in new integrations, never combine it with criteria on the same question (that call is rejected), and it will be removed in a future breaking release. Use "criteria" for new calls, even for bare labels ({"a": null, "b": null}).

For large context (long logs, multi-file diffs), load and redact the content into state/instructions/criteria from a file rather than pasting megabytes inline; jevkit's CLI equivalent, "jevkit ask request --file <path|->", shows the same file/stdin pattern for a human operator.`,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"state": map[string]any{
					"description": "State text or context for the questions: a plain string, or literal JSON (an object or array) for structured state. Redacted (JSON-aware when structured) before transport.",
				},
				"questions": map[string]any{
					"type":        "object",
					"description": "One or more named questions: id to {type, instructions, criteria, options?}. See the tool description for the wire shape and the deprecated 'options' alias. Not a registry question-set id.",
				},
			},
			"required": []string{"state", "questions"},
		},
	}
}

// decodeArgs parses tools/call arguments as a JSON object.
func decodeArgs(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, invalid("invalid params: arguments must be an object")
	}
	return args, nil
}

// checkKeys rejects any key outside allowed.
func checkKeys(args map[string]json.RawMessage, allowed ...string) error {
	for k := range args {
		known := false
		for _, a := range allowed {
			known = known || k == a
		}
		if !known {
			return invalid("invalid params: only %s are allowed", strings.Join(allowed, " and "))
		}
	}
	return nil
}

func stateArg(args map[string]json.RawMessage) (string, error) {
	var state string
	if err := json.Unmarshal(args["state"], &state); err != nil || state == "" {
		return "", invalid("state is required")
	}
	return state, nil
}

func (s *Server) handleCurated(ctx context.Context, tool, setID string, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	set, ok := s.cfg.Decider.Registry.Set(setID)
	if !ok {
		return nil, rpcErr(codeServer, "unknown question set: %s", setID)
	}
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, err
	}
	wantOptions := needsOptions(set)
	allowed := []string{"state"}
	if wantOptions {
		allowed = append(allowed, "options")
	}
	if err := checkKeys(args, allowed...); err != nil {
		return nil, err
	}
	state, err := stateArg(args)
	if err != nil {
		return nil, err
	}
	// Missing Jev is a soft, successful result: agents fall back to native
	// behavior instead of retrying.
	if reason := s.cfg.Unavailable(ctx); reason != "" {
		s.logf("tool=%s available=false reason=%s", tool, reason)
		return unavailable(reason), nil
	}
	var options []string
	if wantOptions {
		if options, err = optionsArg(args, setID); err != nil {
			return nil, err
		}
	}
	questions, err := buildQuestions(set, options)
	if err != nil {
		return nil, rpcErr(codeServer, "failed to apply options to %s", setID)
	}
	redacted, _, err := s.cfg.Redact(state)
	if err != nil {
		s.logf("tool=%s rejected=redaction", tool)
		return nil, invalid(msgStateRejected)
	}
	resp, res, err := s.ask(ctx, tool, jev.Request{QuestionSetID: setID, State: redacted, Questions: questions}, setID)
	if res != nil || err != nil {
		return res, err
	}
	dec, derr := s.cfg.Decider.Decide(setID, resp.Answers)
	if derr != nil {
		s.logf("tool=%s policy-decide-failed", tool)
		return nil, rpcErr(codeServer, "policy decide failed for %s", setID)
	}
	version := dec.RegistryVersion
	if version == "" {
		version = s.cfg.Decider.Registry.RegistryVersion
	}
	s.logf("tool=%s questionSetId=%s decision=%s", tool, setID, dec.Decision)
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("questionSetId=%s decision=%s reason=%s confidence=%v",
			setID, dec.Decision, dec.Reason, dec.Confidence)}},
		StructuredContent: map[string]any{
			"answer":          answersJSON(resp.Answers),
			"decision":        dec,
			"questionSetId":   setID,
			"registryVersion": version,
		},
	}, nil
}

func optionsArg(args map[string]json.RawMessage, setID string) ([]string, error) {
	bad := func() error {
		return invalid("question set %s needs options: 1-255 non-empty strings", setID)
	}
	var opts []string
	if err := json.Unmarshal(args["options"], &opts); err != nil || len(opts) == 0 || len(opts) > jev.MaxChoiceOptions {
		return nil, bad()
	}
	for _, o := range opts {
		if o == "" {
			return nil, bad()
		}
	}
	return opts, nil
}

// buildQuestions turns a registry set into wire questions, filling the
// call-time options of any choice that declares none.
func buildQuestions(set *registry.Set, options []string) (map[string]jev.Question, error) {
	out := make(map[string]jev.Question, len(set.Questions))
	for id, q := range set.Questions {
		switch q.Type {
		case "choice":
			criteria := map[string]json.RawMessage{}
			if emptyCriteria(q.Criteria) {
				for _, o := range options {
					criteria[o] = jev.Null()
				}
			} else if err := json.Unmarshal(q.Criteria, &criteria); err != nil {
				return nil, err
			}
			out[id] = jev.ChoiceQuestion{Instructions: q.Instructions, Criteria: criteria}
		case "score":
			var levels []json.RawMessage
			if err := json.Unmarshal(q.Criteria, &levels); err != nil {
				return nil, err
			}
			out[id] = jev.ScoreQuestion{Instructions: q.Instructions, Criteria: levels}
		case "noul":
			criteria := map[string]json.RawMessage{}
			if len(q.Criteria) > 0 {
				if err := json.Unmarshal(q.Criteria, &criteria); err != nil {
					return nil, err
				}
			}
			out[id] = jev.NoulQuestion{Instructions: q.Instructions, Criteria: criteria}
		default:
			return nil, fmt.Errorf("unknown question type %q", q.Type)
		}
	}
	return out, nil
}

// ask calls Jev. On any failure it returns the finished tool result or
// JSON-RPC error to hand back; only a nil result and nil error means resp is
// usable. Client errors are reduced to a fixed message: wrapped detail can
// carry response text.
func (s *Server) ask(ctx context.Context, tool string, req jev.Request, label string) (*jev.Response, *sdk.CallToolResult, error) {
	resp, err := s.cfg.Client.Ask(ctx, req)
	if err == nil {
		if resp == nil || len(resp.Answers) == 0 {
			return nil, nil, rpcErr(codeServer, "jev response missing answers")
		}
		return resp, nil, nil
	}
	var je *jev.Error
	if errors.As(err, &je) {
		switch {
		case je.Code == jev.CodeDeclined:
			s.logf("tool=%s available=false reason=%s", tool, je.Reason)
			return nil, unavailable(je.Reason), nil
		case je.Code == jev.CodeTransport && je.Reason == "no api key":
			s.logf("tool=%s available=false reason=no-key", tool)
			return nil, unavailable("no-key"), nil
		case je.Code == jev.CodeRejected:
			s.logf("tool=%s rejected=input", tool)
			return nil, nil, invalid(msgStateRejected)
		}
		s.logf("tool=%s failed reason=%s code=%d", tool, je.Reason, je.Code)
	} else {
		s.logf("tool=%s failed", tool)
	}
	return nil, nil, rpcErr(codeServer, "jev transport or protocol failure for %s", label)
}

func unavailable(reason string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		Content:           []sdk.Content{&sdk.TextContent{Text: "Jev unavailable: " + reason}},
		StructuredContent: map[string]any{"available": false, "reason": reason},
	}
}

// answersJSON renders answers in the wire shape (no type tags).
func answersJSON(answers map[string]jev.Answer) map[string]any {
	out := make(map[string]any, len(answers))
	for name, a := range answers {
		switch v := a.(type) {
		case jev.NoulAnswer:
			out[name] = map[string]any{"noul": v.Noul}
		case jev.ChoiceAnswer:
			out[name] = map[string]any{"choice": v.Choice, "probabilities": v.Probabilities, "confidence": v.Confidence}
		case jev.ScoreAnswer:
			out[name] = map[string]any{"score": v.Score, "confidence": v.Confidence}
		}
	}
	return out
}

// answerKind names an answer's SystemOne type.
func answerKind(a jev.Answer) string {
	switch a.(type) {
	case jev.NoulAnswer:
		return "noul"
	case jev.ChoiceAnswer:
		return "choice"
	case jev.ScoreAnswer:
		return "score"
	default:
		return ""
	}
}

// fullAnswerJSON renders one answer with every field SystemOne returned:
// type, confidence, probabilities and (for Score) legend/distribution.
func fullAnswerJSON(a jev.Answer) map[string]any {
	out := map[string]any{"type": answerKind(a)}
	switch v := a.(type) {
	case jev.NoulAnswer:
		out["noul"] = v.Noul
	case jev.ChoiceAnswer:
		out["choice"] = v.Choice
		out["confidence"] = v.Confidence
		if len(v.Probabilities) > 0 {
			out["probabilities"] = v.Probabilities
		}
	case jev.ScoreAnswer:
		out["score"] = v.Score
		out["confidence"] = v.Confidence
		if len(v.Legend) > 0 {
			out["legend"] = v.Legend
		}
		if len(v.Distribution) > 0 {
			out["distribution"] = v.Distribution
		}
	}
	return out
}

// fullAnswersJSON renders every answer via [fullAnswerJSON].
func fullAnswersJSON(answers map[string]jev.Answer) map[string]any {
	out := make(map[string]any, len(answers))
	for name, a := range answers {
		out[name] = fullAnswerJSON(a)
	}
	return out
}

// askQuestion is one jev_ask question. Options is a DEPRECATED shorthand for
// Criteria (a bare closed set of choice labels, Choice-only, expanding to
// null-valued criteria); a caller must not supply both.
type askQuestion struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Options      []string        `json:"options"`
	Criteria     json.RawMessage `json:"criteria"`
}

// resolveText classifies raw as the documented state/instructions shape: a
// non-empty JSON string (plain text) or literal JSON (an object or array,
// structured content). Anything else (number, bool, null, empty, invalid)
// is not ok.
func resolveText(raw json.RawMessage) (plain string, structured json.RawMessage, ok bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", nil, false
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return "", nil, false
		}
		return t, nil, true
	case map[string]any, []any:
		return "", bytes.TrimSpace(raw), true
	default:
		return "", nil, false
	}
}

// callerID names the connected MCP client for audit entries, from its
// initialize handshake; "unknown" when the session or handshake is absent
// (e.g. a fixture test harness that skips initialize).
func callerID(req *sdk.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return "unknown"
	}
	init := req.Session.InitializeParams()
	if init == nil || init.ClientInfo == nil || init.ClientInfo.Name == "" {
		return "unknown"
	}
	if init.ClientInfo.Version != "" {
		return init.ClientInfo.Name + "/" + init.ClientInfo.Version
	}
	return init.ClientInfo.Name
}

func (s *Server) handleAsk(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	const tool = "jev_ask"
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, err
	}
	if err := checkKeys(args, "state", "questions"); err != nil {
		return nil, err
	}
	statePlain, stateStructured, ok := resolveText(args["state"])
	if !ok {
		return nil, invalid("state is required: a non-empty string, object or array")
	}
	questions, auditQs, err := s.parseAskQuestions(args["questions"])
	if err != nil {
		return nil, err
	}
	if reason := s.cfg.Unavailable(ctx); reason != "" {
		s.logf("tool=%s available=false reason=%s", tool, reason)
		return unavailable(reason), nil
	}
	redactedState, hits, err := s.redactText(statePlain, stateStructured)
	if err != nil {
		s.logf("tool=%s rejected=redaction", tool)
		return nil, invalid(msgStateRejected)
	}

	wireQuestions := make(map[string]jev.Question, len(questions))
	for name, bq := range questions {
		q, qhits, err := s.buildAskQuestion(name, bq)
		if err != nil {
			return nil, err
		}
		wireQuestions[name] = q
		hits = append(hits, qhits...)
	}
	built := jev.Request{State: redactedState, Questions: wireQuestions}
	if err := built.Validate(); err != nil {
		return nil, invalid("%v", err)
	}

	s.audit(tool, AuditEntry{
		Tool:          tool,
		Caller:        callerID(req),
		Unregistered:  true,
		StateBytes:    len(args["state"]),
		Questions:     auditQs,
		RedactionHits: mergeHits(hits),
	})

	resp, res, err := s.ask(ctx, tool, built, tool)
	if res != nil || err != nil {
		return res, err
	}
	s.logf("tool=%s answered unregistered=true", tool)
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: "jev_ask unregistered answer returned (prefer curated tools for auditable, versioned decisions)"}},
		StructuredContent: map[string]any{
			"answer":       fullAnswersJSON(resp.Answers),
			"model":        resp.Model,
			"usage":        map[string]any{"input_tokens": resp.Usage.InputTokens, "output_tokens": resp.Usage.OutputTokens},
			"unversioned":  true,
			"unaudited":    true,
			"unregistered": true,
		},
	}, nil
}

// audit records a privacy-safe local audit entry; failures are logged, not
// returned, since a missing/broken audit log must not block a call.
func (s *Server) audit(tool string, e AuditEntry) {
	if s.cfg.AuditDir == "" {
		return
	}
	if err := AppendAudit(s.cfg.AuditDir, e); err != nil {
		s.logf("tool=%s audit-write-failed", tool)
	}
}

// redactText applies plain or JSON-aware redaction per [resolveText]'s
// classification, returning the wire-ready text and the rules that fired.
func (s *Server) redactText(plain string, structured json.RawMessage) (string, []redact.Hit, error) {
	if structured != nil {
		b, hits, err := s.cfg.RedactJSON(structured)
		return string(b), hits, err
	}
	return s.cfg.Redact(plain)
}

// parseAskQuestions decodes the questions object, and builds the
// privacy-safe audit shape (ids, types, byte counts) alongside it; question
// bodies are validated and redacted separately in buildAskQuestion so a
// caller can compute the audit entry before any state reaches the wire.
func (s *Server) parseAskQuestions(raw json.RawMessage) (map[string]askQuestion, []AuditQuestion, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil || in == nil {
		return nil, nil, invalid("questions must be an object")
	}
	if len(in) == 0 {
		return nil, nil, invalid("questions must not be empty")
	}
	out := make(map[string]askQuestion, len(in))
	audit := make([]AuditQuestion, 0, len(in))
	for name, r := range in {
		var q askQuestion
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&q); err != nil {
			return nil, nil, invalid("question %q must be {type, instructions, criteria?, options?}", name)
		}
		out[name] = q
		audit = append(audit, AuditQuestion{
			ID: name, Type: q.Type,
			InstructionsLen: len(q.Instructions),
			CriteriaLen:     len(q.Criteria),
		})
	}
	sort.Slice(audit, func(i, j int) bool { return audit[i].ID < audit[j].ID })
	return out, audit, nil
}

// buildAskQuestion validates, redacts and constructs one wire question. The
// deprecated Options alias (Choice-only, bare labels) expands to null-valued
// criteria and is rejected in combination with Criteria.
func (s *Server) buildAskQuestion(name string, q askQuestion) (jev.Question, []redact.Hit, error) {
	plain, structured, ok := resolveText(q.Instructions)
	if !ok {
		return nil, nil, invalid("question %q: instructions is required (a non-empty string, object or array)", name)
	}
	instructions, hits, err := s.redactText(plain, structured)
	if err != nil {
		return nil, nil, invalid(msgStateRejected)
	}

	if len(q.Options) > 0 && len(q.Criteria) > 0 {
		return nil, nil, invalid("question %q: options and criteria are conflicting shorthand for the same field; supply only one", name)
	}
	if len(q.Options) > 0 && q.Type != "choice" {
		return nil, nil, invalid("question %q: options is a choice-only shorthand", name)
	}

	criteria := q.Criteria
	if len(q.Options) > 0 {
		if len(q.Options) > jev.MaxChoiceOptions {
			return nil, nil, invalid("question %q: options has %d entries, cap is %d", name, len(q.Options), jev.MaxChoiceOptions)
		}
		m := make(map[string]json.RawMessage, len(q.Options))
		for _, o := range q.Options {
			if o == "" {
				return nil, nil, invalid("question %q: options entries must not be empty", name)
			}
			m[o] = jev.Null()
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, nil, rpcErr(codeServer, "failed to expand options for %q", name)
		}
		criteria = b
	}

	redactCriteria := func() (json.RawMessage, error) {
		rc, chits, err := s.cfg.RedactJSON(criteria)
		if err != nil {
			return nil, invalid(msgStateRejected)
		}
		hits = append(hits, chits...)
		return rc, nil
	}

	switch q.Type {
	case "noul":
		crit := map[string]json.RawMessage{}
		if len(criteria) > 0 {
			rc, err := redactCriteria()
			if err != nil {
				return nil, nil, err
			}
			if err := json.Unmarshal(rc, &crit); err != nil {
				return nil, nil, invalid("question %q: noul criteria must be an object", name)
			}
		}
		return jev.NoulQuestion{Instructions: instructions, Criteria: crit}, hits, nil
	case "score":
		if len(criteria) == 0 {
			return nil, nil, invalid("question %q needs criteria: an array of 2-10 levels", name)
		}
		rc, err := redactCriteria()
		if err != nil {
			return nil, nil, err
		}
		var levels []json.RawMessage
		if err := json.Unmarshal(rc, &levels); err != nil {
			return nil, nil, invalid("question %q: score criteria must be an array of 2-10 levels", name)
		}
		return jev.ScoreQuestion{Instructions: instructions, Criteria: levels}, hits, nil
	case "choice":
		if len(criteria) == 0 {
			return nil, nil, invalid("question %q needs criteria: 1-%d entries", name, jev.MaxChoiceOptions)
		}
		rc, err := redactCriteria()
		if err != nil {
			return nil, nil, err
		}
		crit := map[string]json.RawMessage{}
		if err := json.Unmarshal(rc, &crit); err != nil {
			return nil, nil, invalid("question %q: choice criteria must be an object", name)
		}
		return jev.ChoiceQuestion{Instructions: instructions, Criteria: crit}, hits, nil
	default:
		return nil, nil, invalid("question %q: type must be noul, choice or score", name)
	}
}
