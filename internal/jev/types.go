package jev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxChoiceOptions is the per-question cap on choice options.
const MaxChoiceOptions = 255

// Question is a sealed set: NoulQuestion, ChoiceQuestion and ScoreQuestion.
type Question interface {
	questionType() string
	validate() error
}

// NoulQuestion asks a yes/no style question answered with a single value.
type NoulQuestion struct {
	Instructions string
	Criteria     json.RawMessage
}

// ChoiceQuestion asks for one of a closed set of options. The option
// descriptions may be nil when the option text is already present in state.
type ChoiceQuestion struct {
	Instructions string
	Options      map[string]*string
	Criteria     json.RawMessage
}

// ScoreQuestion asks for a numeric score.
type ScoreQuestion struct {
	Instructions string
	Criteria     json.RawMessage
}

func (NoulQuestion) questionType() string   { return "noul" }
func (ChoiceQuestion) questionType() string { return "choice" }
func (ScoreQuestion) questionType() string  { return "score" }

func (NoulQuestion) validate() error  { return nil }
func (ScoreQuestion) validate() error { return nil }
func (q ChoiceQuestion) validate() error {
	if len(q.Options) > MaxChoiceOptions {
		return fmt.Errorf("choice has %d options, cap is %d", len(q.Options), MaxChoiceOptions)
	}
	return nil
}

type wireQuestion struct {
	Type         string             `json:"type"`
	Instructions string             `json:"instructions"`
	Options      map[string]*string `json:"options,omitempty"`
	Criteria     json.RawMessage    `json:"criteria,omitempty"`
}

func (q NoulQuestion) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireQuestion{Type: q.questionType(), Instructions: q.Instructions, Criteria: q.Criteria})
}

func (q ChoiceQuestion) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireQuestion{Type: q.questionType(), Instructions: q.Instructions, Options: q.Options, Criteria: q.Criteria})
}

func (q ScoreQuestion) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireQuestion{Type: q.questionType(), Instructions: q.Instructions, Criteria: q.Criteria})
}

// Request is one SystemOne call. QuestionSetID is local routing metadata
// (fixture lookup, decision records); it is never serialized onto the wire.
type Request struct {
	QuestionSetID string              `json:"-"`
	State         string              `json:"state"`
	Model         string              `json:"model"`
	Questions     map[string]Question `json:"questions"`
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

// ScoreAnswer is the answer to a score question.
type ScoreAnswer struct {
	Score      float64
	Confidence float64
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
	Model   string
	Answers map[string]Answer
	Usage   Usage
}

// ErrNoAnswers is returned when a response body lacks the `answers` object.
var ErrNoAnswers = errors.New("response missing answers")

// DecodeResponse strictly decodes a response body: it must be a JSON object
// with an `answers` object whose members are recognizable typed answers.
func DecodeResponse(data []byte) (*Response, error) {
	var raw struct {
		Model   string          `json:"model"`
		Answers json.RawMessage `json:"answers"`
		Usage   Usage           `json:"usage"`
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
	resp := &Response{Model: raw.Model, Usage: raw.Usage, Answers: make(map[string]Answer, len(members))}
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
			Score      *float64 `json:"score"`
			Confidence float64  `json:"confidence"`
		}
		if err := json.Unmarshal(data, &a); err != nil || a.Score == nil {
			return nil, errors.New("invalid score answer")
		}
		return ScoreAnswer{Score: *a.Score, Confidence: a.Confidence}, nil
	}
	return nil, fmt.Errorf("unrecognized answer type %q", kind)
}
