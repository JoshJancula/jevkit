package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OWNER/jevkit/internal/jev"
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
		Description: "UNVERSIONED and UNAUDITED raw escape hatch for exploration. Accepts arbitrary state plus a questions object; " +
			"prefer curated tools (jev_classify_request, jev_classify_failure, jev_rank_relevance) for auditable versioned decisions. " +
			"Same redaction and request-size policy as curated tools; it escapes the registry only, not the data policy.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"state": map[string]any{"type": "string", "description": "State text passed through redaction and size estimation before transport."},
				"questions": map[string]any{
					"type":        "object",
					"description": "Arbitrary SystemOne questions: name to {type: noul|choice|score, instructions, options?, criteria?}. Not a registry question-set id.",
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
	redacted, err := s.cfg.Redact(state)
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
			opts := map[string]*string{}
			if emptyCriteria(q.Criteria) {
				for _, o := range options {
					opts[o] = nil
				}
			} else {
				var m map[string]string
				if err := json.Unmarshal(q.Criteria, &m); err != nil {
					return nil, err
				}
				for k, v := range m {
					opts[k] = &v
				}
			}
			out[id] = jev.ChoiceQuestion{Instructions: q.Instructions, Options: opts}
		case "score":
			out[id] = jev.ScoreQuestion{Instructions: q.Instructions, Criteria: q.Criteria}
		case "noul":
			out[id] = jev.NoulQuestion{Instructions: q.Instructions, Criteria: q.Criteria}
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

// askQuestion is one raw jev_ask question.
type askQuestion struct {
	Type         string             `json:"type"`
	Instructions string             `json:"instructions"`
	Options      map[string]*string `json:"options"`
	Criteria     json.RawMessage    `json:"criteria"`
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
	state, err := stateArg(args)
	if err != nil {
		return nil, err
	}
	questions, err := parseQuestions(args["questions"])
	if err != nil {
		return nil, err
	}
	if reason := s.cfg.Unavailable(ctx); reason != "" {
		s.logf("tool=%s available=false reason=%s", tool, reason)
		return unavailable(reason), nil
	}
	redacted, err := s.cfg.Redact(state)
	if err != nil {
		s.logf("tool=%s rejected=redaction", tool)
		return nil, invalid(msgStateRejected)
	}
	resp, res, err := s.ask(ctx, tool, jev.Request{State: redacted, Questions: questions}, tool)
	if res != nil || err != nil {
		return res, err
	}
	s.logf("tool=%s answered", tool)
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: "jev_ask UNVERSIONED/UNAUDITED answer returned (prefer curated tools for auditable decisions)"}},
		StructuredContent: map[string]any{
			"answer":      answersJSON(resp.Answers),
			"unversioned": true,
			"unaudited":   true,
		},
	}, nil
}

func parseQuestions(raw json.RawMessage) (map[string]jev.Question, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil || in == nil {
		return nil, invalid("questions must be an object")
	}
	if len(in) == 0 {
		return nil, invalid("questions must not be empty")
	}
	out := make(map[string]jev.Question, len(in))
	for name, r := range in {
		var q askQuestion
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&q); err != nil {
			return nil, invalid("question %q must be {type, instructions, options?, criteria?}", name)
		}
		switch q.Type {
		case "noul":
			out[name] = jev.NoulQuestion{Instructions: q.Instructions, Criteria: q.Criteria}
		case "score":
			out[name] = jev.ScoreQuestion{Instructions: q.Instructions, Criteria: q.Criteria}
		case "choice":
			opts := q.Options
			if len(opts) == 0 && len(q.Criteria) > 0 {
				// Accept the criteria object as the option set.
				if err := json.Unmarshal(q.Criteria, &opts); err != nil {
					return nil, invalid("question %q: choice options must be an object", name)
				}
			}
			if len(opts) == 0 || len(opts) > jev.MaxChoiceOptions {
				return nil, invalid("question %q needs options: 1-%d entries", name, jev.MaxChoiceOptions)
			}
			out[name] = jev.ChoiceQuestion{Instructions: q.Instructions, Options: opts}
		default:
			return nil, invalid("question %q: type must be noul, choice or score", name)
		}
	}
	return out, nil
}
