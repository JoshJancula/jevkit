// Package registry loads the embedded Jev question-set registry and applies
// the act/gather/fallback decision policy to answers.
//
// Thresholds come only from the registry: nothing in this package accepts a
// caller-supplied act or escalate threshold.
package registry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/OWNER/jevkit/internal/jev"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed questions.registry.json
var registryJSON []byte

//go:embed jev-question-set.schema.json
var schemaJSON []byte

const schemaID = "https://jevkit.invalid/jev-question-set.schema.json"

// maxChoiceOptions mirrors the per-question cap on choice options.
const maxChoiceOptions = 255

// Registry is the parsed question-set registry.
type Registry struct {
	RegistryVersion string          `json:"registryVersion"`
	QuestionSets    map[string]*Set `json:"questionSets"`
}

// Set is one registered question set.
type Set struct {
	ID          string              `json:"id"`
	Version     int                 `json:"version"`
	Surface     string              `json:"surface"`
	Description string              `json:"description"`
	Questions   map[string]Question `json:"questions"`
	Policy      Policy              `json:"policy"`
	Calibration string              `json:"calibration,omitempty"`
}

// Question is one registered question definition.
type Question struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
	CriteriaMode string          `json:"criteriaMode,omitempty"`
}

// JevQuestions turns registry rubrics into wire questions. Dynamic options are
// accepted only for questions that explicitly declare call-time criteria.
func (s *Set) JevQuestions(callTime map[string]map[string]json.RawMessage) (map[string]jev.Question, error) {
	for id := range callTime {
		if _, ok := s.Questions[id]; !ok {
			return nil, fmt.Errorf("registry: undeclared call-time question %q", id)
		}
	}
	out := make(map[string]jev.Question, len(s.Questions))
	for id, q := range s.Questions {
		if len(callTime[id]) > 0 && q.CriteriaMode != "call-time" {
			return nil, fmt.Errorf("registry: %s.%s does not accept call-time criteria", s.ID, id)
		}
		switch q.Type {
		case "choice", "noul":
			var declared map[string]string
			if len(q.Criteria) > 0 {
				if err := json.Unmarshal(q.Criteria, &declared); err != nil {
					return nil, err
				}
			}
			criteria := make(map[string]json.RawMessage, len(declared)+len(callTime[id]))
			for k, v := range declared {
				criteria[k] = jev.Str(v)
			}
			for k, v := range callTime[id] {
				if _, exists := criteria[k]; exists {
					return nil, fmt.Errorf("registry: duplicate option %q", k)
				}
				criteria[k] = v
			}
			if q.Type == "choice" {
				if len(criteria) == 0 || len(criteria) > jev.MaxChoiceOptions {
					return nil, fmt.Errorf("registry: invalid choice count for %s", id)
				}
				out[id] = jev.ChoiceQuestion{Instructions: q.Instructions, Criteria: criteria}
			} else {
				out[id] = jev.NoulQuestion{Instructions: q.Instructions, Criteria: criteria}
			}
		case "score":
			var legend []string
			if err := json.Unmarshal(q.Criteria, &legend); err != nil {
				return nil, err
			}
			criteria := make([]json.RawMessage, len(legend))
			for i, v := range legend {
				criteria[i] = jev.Str(v)
			}
			out[id] = jev.ScoreQuestion{Instructions: q.Instructions, Criteria: criteria}
		default:
			return nil, fmt.Errorf("registry: unknown question type %q", q.Type)
		}
	}
	return out, nil
}

// Policy holds a set's decision thresholds.
type Policy struct {
	PrimaryQuestion   string  `json:"primaryQuestion"`
	ActThreshold      float64 `json:"actThreshold"`
	EscalateThreshold float64 `json:"escalateThreshold"`
	Fallback          string  `json:"fallback"`
}

// Parse validates raw against the embedded schema and the per-type shape
// rules the schema cannot express, then returns the parsed registry.
func Parse(raw []byte) (*Registry, error) {
	if err := validateSchema(raw); err != nil {
		return nil, err
	}
	var r Registry
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

var (
	loadOnce sync.Once
	loaded   *Registry
	loadErr  error
)

// Load returns the embedded registry, validated once on first use.
func Load() (*Registry, error) {
	loadOnce.Do(func() { loaded, loadErr = Parse(registryJSON) })
	return loaded, loadErr
}

// Set returns the set registered under id.
func (r *Registry) Set(id string) (*Set, bool) {
	s, ok := r.QuestionSets[id]
	return s, ok
}

// IDs lists the registered set ids in sorted order.
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.QuestionSets))
	for id := range r.QuestionSets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Threshold returns a set's "act" or "escalate" threshold.
func (r *Registry) Threshold(id, which string) (float64, error) {
	s, ok := r.Set(id)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownSet, id)
	}
	switch which {
	case "act":
		return s.Policy.ActThreshold, nil
	case "escalate":
		return s.Policy.EscalateThreshold, nil
	}
	return 0, fmt.Errorf("registry: threshold must be act or escalate, got %q", which)
}

// ErrUnknownSet reports a question set id that is not registered.
var ErrUnknownSet = errors.New("registry: unknown question set")

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

func compiledSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
		if err != nil {
			schemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(schemaID, doc); err != nil {
			schemaErr = err
			return
		}
		schema, schemaErr = c.Compile(schemaID)
	})
	return schema, schemaErr
}

func validateSchema(raw []byte) error {
	sch, err := compiledSchema()
	if err != nil {
		return fmt.Errorf("registry: embedded schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("registry: schema: %w", err)
	}
	return nil
}

// validate enforces what the schema cannot: key/id agreement, the primary
// question and threshold ordering, and criteria shape per question type.
func (r *Registry) validate() error {
	if len(r.QuestionSets) == 0 {
		return errors.New("registry: declares no question sets")
	}
	for key, s := range r.QuestionSets {
		if s.ID != key {
			return fmt.Errorf("registry: set key %q does not match id %q", key, s.ID)
		}
		if _, ok := s.Questions[s.Policy.PrimaryQuestion]; !ok {
			return fmt.Errorf("registry: %s: primaryQuestion %q is not a question in the set", key, s.Policy.PrimaryQuestion)
		}
		if s.Policy.ActThreshold <= s.Policy.EscalateThreshold {
			return fmt.Errorf("registry: %s: actThreshold must exceed escalateThreshold", key)
		}
		for qid, q := range s.Questions {
			if err := q.validateCriteria(); err != nil {
				return fmt.Errorf("registry: %s.%s: %w", key, qid, err)
			}
		}
	}
	return nil
}

func (q Question) validateCriteria() error {
	if q.CriteriaMode != "" && q.CriteriaMode != "fixed" && q.CriteriaMode != "call-time" {
		return fmt.Errorf("invalid criteriaMode %q", q.CriteriaMode)
	}
	raw := q.Criteria
	if len(raw) == 0 || string(raw) == "null" {
		if q.Type == "choice" || q.Type == "score" {
			return fmt.Errorf("%s criteria are required", q.Type)
		}
		return nil
	}
	switch q.Type {
	case "choice":
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return errors.New("choice criteria must be an object of option id to description")
		}
		if len(m) > maxChoiceOptions {
			return fmt.Errorf("choice caps at %d options, has %d", maxChoiceOptions, len(m))
		}
		if _, empty := m[""]; empty {
			return errors.New("choice option id must be non-empty")
		}
	case "score":
		var l []string
		if err := json.Unmarshal(raw, &l); err != nil || len(l) < 2 {
			return errors.New("score criteria must be a legend array of at least two labels")
		}
	case "noul":
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return errors.New("noul criteria must be an object of true/false labels")
		}
		for k := range m {
			if k != "true" && k != "false" {
				return fmt.Errorf("noul criteria key %q is not true or false", k)
			}
		}
	}
	if q.CriteriaMode == "call-time" && q.Type != "choice" {
		return errors.New("call-time criteria require choice question")
	}
	return nil
}
