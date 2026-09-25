package jev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MaxChoiceOptions is the per-question cap on choice criteria entries.
const MaxChoiceOptions = 255

// Question is a sealed set: NoulQuestion, ChoiceQuestion and ScoreQuestion.
type Question interface {
	questionType() string
	validate() error
}

// Str wraps a plain string as a criteria value: the ergonomic default for a
// Choice, Noul or Score criteria entry.
func Str(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return json.RawMessage(b)
}

// Null is the JSON null criteria value: the option or level speaks for
// itself and needs no further description.
func Null() json.RawMessage { return json.RawMessage("null") }

// NoulQuestion asks a yes/no style question answered with a value in [0,1].
// Criteria is optional: it may hold a "true" and/or a "false" entry, each a
// plain string (the ergonomic default), or arbitrary JSON (object, array or
// null).
type NoulQuestion struct {
	Instructions string
	Criteria     map[string]json.RawMessage
}

// ChoiceQuestion asks for one of a closed set of options. Criteria is
// required: option label to description, where each description is a plain
// string (the ergonomic default), or arbitrary JSON (object, array or null
// when the label alone is self-explanatory).
type ChoiceQuestion struct {
	Instructions string
	Criteria     map[string]json.RawMessage
}

// ScoreQuestion asks for a numeric score against an ordered rubric. Criteria
// is required: 2-10 levels ordered low to high, each a plain string (the
// ergonomic default) or arbitrary JSON (object or array).
type ScoreQuestion struct {
	Instructions string
	Criteria     []json.RawMessage
}

func (NoulQuestion) questionType() string   { return "noul" }
func (ChoiceQuestion) questionType() string { return "choice" }
func (ScoreQuestion) questionType() string  { return "score" }

// jsonKind classifies a JSON value: "string", "object", "array", "null" or
// "" when raw is empty, invalid JSON, or any other JSON type (number, bool).
func jsonKind(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	switch v.(type) {
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case nil:
		return "null"
	default:
		return ""
	}
}

// validCriteriaValue reports whether raw is a string, object or array, and
// (when allowNull) also accepts null. Nothing else (numbers, booleans,
// invalid JSON) is an acceptable criteria value.
func validCriteriaValue(raw json.RawMessage, allowNull bool) bool {
	switch jsonKind(raw) {
	case "string", "object", "array":
		return true
	case "null":
		return allowNull
	default:
		return false
	}
}

func (q NoulQuestion) validate() error {
	for k, v := range q.Criteria {
		if k != "true" && k != "false" {
			return fmt.Errorf("noul criteria key %q must be \"true\" or \"false\"", k)
		}
		if !validCriteriaValue(v, true) {
			return fmt.Errorf("noul criteria %q must be a string, object, array or null", k)
		}
	}
	return nil
}

func (q ChoiceQuestion) validate() error {
	if len(q.Criteria) == 0 {
		return errors.New("choice requires at least one criteria entry")
	}
	if len(q.Criteria) > MaxChoiceOptions {
		return fmt.Errorf("choice has %d criteria entries, cap is %d", len(q.Criteria), MaxChoiceOptions)
	}
	for k, v := range q.Criteria {
		if k == "" {
			return errors.New("choice criteria key must not be empty")
		}
		if !validCriteriaValue(v, true) {
			return fmt.Errorf("choice criteria %q must be a string, object, array or null", k)
		}
	}
	return nil
}

func (q ScoreQuestion) validate() error {
	if len(q.Criteria) < 2 || len(q.Criteria) > 10 {
		return fmt.Errorf("score requires 2-10 criteria levels, got %d", len(q.Criteria))
	}
	for i, v := range q.Criteria {
		if !validCriteriaValue(v, false) {
			return fmt.Errorf("score criteria[%d] must be a string, object or array", i)
		}
	}
	return nil
}

// IsLiteralJSON reports whether s, once trimmed, is literal JSON text (a
// quoted string, an object or an array) that [wireText] would embed on the
// wire as-is rather than wrap as a plain string. A caller redacting a State
// or Instructions value before it reaches the wire can use this to choose
// JSON-aware redaction over plain-string redaction for the same text.
func IsLiteralJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || !json.Valid([]byte(t)) {
		return false
	}
	switch t[0] {
	case '{', '[', '"':
		return true
	}
	return false
}

// wireText encodes a State or Instructions value. A plain string is the
// ergonomic default and is embedded as a JSON string; a caller may instead
// pass literal JSON text (a quoted string, an object or an array) and it is
// embedded on the wire as-is, so the documented protocol's "JSON string,
// object, or array" shapes are all reachable through one Go string field.
func wireText(s string) json.RawMessage {
	if IsLiteralJSON(s) {
		return json.RawMessage(strings.TrimSpace(s))
	}
	return Str(s)
}

type wireQuestion struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

func (q NoulQuestion) MarshalJSON() ([]byte, error) {
	var criteria json.RawMessage
	if len(q.Criteria) > 0 {
		b, err := json.Marshal(q.Criteria)
		if err != nil {
			return nil, err
		}
		criteria = b
	}
	return json.Marshal(wireQuestion{Type: q.questionType(), Instructions: wireText(q.Instructions), Criteria: criteria})
}

func (q ChoiceQuestion) MarshalJSON() ([]byte, error) {
	criteria, err := json.Marshal(q.Criteria)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireQuestion{Type: q.questionType(), Instructions: wireText(q.Instructions), Criteria: criteria})
}

func (q ScoreQuestion) MarshalJSON() ([]byte, error) {
	criteria, err := json.Marshal(q.Criteria)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireQuestion{Type: q.questionType(), Instructions: wireText(q.Instructions), Criteria: criteria})
}

// Request is one SystemOne call. QuestionSetID is local routing metadata
// (fixture lookup, decision records); it is never serialized onto the wire.
type Request struct {
	QuestionSetID string `json:"-"`
	// State is a plain string (the ergonomic default) or literal JSON text
	// (a quoted string, an object or an array); see [wireText].
	State     string              `json:"-"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

type wireRequest struct {
	State     json.RawMessage     `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// MarshalJSON encodes the wire body: State is expanded per [wireText] and
// QuestionSetID never appears.
func (r Request) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireRequest{State: wireText(r.State), Model: r.Model, Questions: r.Questions})
}

// Validate checks req against the local typed schema, without making a
// request: at least one question, each with a non-empty id and a valid
// question body per its own kind (Choice 1-255 distinct criteria keys,
// Score 2-10 ordered levels, valid JSON criteria value types, and so on).
// [Client.Ask] also calls this before it builds the wire body, so a caller
// that validates first only sees the same rejection earlier and offline.
func (r Request) Validate() error {
	if len(r.Questions) == 0 {
		return errors.New("no questions")
	}
	for name, q := range r.Questions {
		if name == "" {
			return errors.New("question id must not be empty")
		}
		if q == nil {
			return fmt.Errorf("question %q is nil", name)
		}
		if err := q.validate(); err != nil {
			return fmt.Errorf("question %q: %w", name, err)
		}
	}
	return nil
}

// Answer is a sealed set: NoulAnswer, ChoiceAnswer and ScoreAnswer.
type Answer interface{ answerType() string }

// NoulAnswer is the answer to a noul question.
type NoulAnswer struct{ Noul float64 }

// ChoiceAnswer is the answer to a choice question.
type ChoiceAnswer struct {
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
}

// ScoreAnswer is the answer to a score question. Legend and Distribution are
// populated only when SystemOne returns them: Legend is the ordered level
// labels (low to high) and Distribution maps each label to its probability.
type ScoreAnswer struct {
	Score        float64
	Confidence   float64
	Legend       []string
	Distribution map[string]float64
	// Raw fields retain response shapes that the typed display fields do not
	// understand yet, so offline calibration can replay them without loss.
	RawLegend       json.RawMessage
	RawDistribution json.RawMessage
}

func (NoulAnswer) answerType() string   { return "noul" }
func (ChoiceAnswer) answerType() string { return "choice" }
func (ScoreAnswer) answerType() string  { return "score" }

// Usage reports token accounting from the API.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is a decoded SystemOne response.
type Response struct {
	Model         string
	Answers       map[string]Answer
	Usage         Usage
	UsageReported bool
}

// ErrNoAnswers is returned when a response body lacks the `answers` object.
var ErrNoAnswers = errors.New("response missing answers")

// DecodeResponse strictly decodes a response body: it must be a JSON object
// with an `answers` object whose members are recognizable typed answers.
func DecodeResponse(data []byte) (*Response, error) {
	var raw struct {
		Model   string          `json:"model"`
		Answers json.RawMessage `json:"answers"`
		Usage   *Usage          `json:"usage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(raw.Answers) == 0 || bytes.Equal(bytes.TrimSpace(raw.Answers), []byte("null")) {
		return nil, ErrNoAnswers
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw.Answers, &members); err != nil {
		return nil, fmt.Errorf("decode answers: %w", err)
	}
	resp := &Response{Model: raw.Model, UsageReported: raw.Usage != nil, Answers: make(map[string]Answer, len(members))}
	if raw.Usage != nil {
		resp.Usage = *raw.Usage
	}
	for name, m := range members {
		a, err := decodeAnswer(m)
		if err != nil {
			return nil, fmt.Errorf("answer %q: %w", name, err)
		}
		resp.Answers[name] = a
	}
	return resp, nil
}

func decodeAnswer(data json.RawMessage) (Answer, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	kind := ""
	if t, ok := fields["type"]; ok {
		if err := json.Unmarshal(t, &kind); err != nil {
			return nil, fmt.Errorf("bad type: %w", err)
		}
	} else {
		for _, k := range []string{"choice", "noul", "score"} {
			if _, ok := fields[k]; ok {
				kind = k
				break
			}
		}
	}
	switch kind {
	case "noul":
		var a struct {
			Noul *float64 `json:"noul"`
		}
		if err := json.Unmarshal(data, &a); err != nil || a.Noul == nil {
			return nil, errors.New("invalid noul answer")
		}
		return NoulAnswer{Noul: *a.Noul}, nil
	case "choice":
		var a struct {
			Choice        *string            `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
			Confidence    float64            `json:"confidence"`
		}
		if err := json.Unmarshal(data, &a); err != nil || a.Choice == nil {
			return nil, errors.New("invalid choice answer")
		}
		return ChoiceAnswer{Choice: *a.Choice, Probabilities: a.Probabilities, Confidence: a.Confidence}, nil
	case "score":
		var a struct {
			Score        *float64        `json:"score"`
			Confidence   float64         `json:"confidence"`
			Legend       json.RawMessage `json:"legend"`
			Distribution json.RawMessage `json:"distribution"`
		}
		if err := json.Unmarshal(data, &a); err != nil {
			return nil, fmt.Errorf("invalid score answer: %w", err)
		}
		if a.Score == nil {
			return nil, errors.New("invalid score answer")
		}
		answer := ScoreAnswer{Score: *a.Score, Confidence: a.Confidence, RawLegend: a.Legend, RawDistribution: a.Distribution}
		_ = json.Unmarshal(a.Legend, &answer.Legend)
		_ = json.Unmarshal(a.Distribution, &answer.Distribution)
		return answer, nil
	}
	return nil, fmt.Errorf("unrecognized answer type %q", kind)
}
