package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
	"github.com/OWNER/jevkit/internal/jev"
)

// Decisions.
const (
	Act      = "act"
	Gather   = "gather"
	Fallback = "fallback"
)

// Reasons attached to a decision.
const (
	ReasonOptionNotOffered = "option-not-offered"
	ReasonShadow           = "shadow"
)

// ErrNoDecision reports answers that cannot be decided: the set declares no
// usable primary question, the primary answer is missing, or its confidence
// is not a finite number.
var ErrNoDecision = errors.New("registry: no decision possible")

// Decision is the policy outcome for one set of answers and one decisions.jsonl line.
//
// In shadow mode the value returned to the caller has Decision "fallback",
// while the logged record keeps the would-have decision.
type Decision struct {
	Timestamp          string                 `json:"timestamp"`
	Decision           string                 `json:"decision"`
	Chosen             *string                `json:"chosen"`
	Confidence         float64                `json:"confidence"`
	QuestionSetID      string                 `json:"questionSetId"`
	QuestionSetVersion int                    `json:"questionSetVersion"`
	Surface            string                 `json:"surface"`
	Reason             string                 `json:"reason"`
	Shadow             bool                   `json:"shadow,omitempty"`
	FallbackUsed       bool                   `json:"fallbackUsed"`
	Answers            map[string]interface{} `json:"answers,omitempty"`
	RegistryVersion    string                 `json:"registryVersion"`
}

// Decider applies a registry's policy and logs each decision.
type Decider struct {
	Registry *Registry
	// StateDir holds jevkit/decisions.jsonl. Empty disables logging.
	StateDir string
	// Getenv reads JEVKIT_SHADOW; nil means os.Getenv.
	Getenv func(string) string
	// Now stamps records; nil means time.Now.
	Now func() time.Time
}

// DecisionsPath is <stateDir>/jevkit/decisions.jsonl.
func DecisionsPath(stateDir string) string {
	return filepath.Join(stateDir, "jevkit", "decisions.jsonl")
}

// Classify applies the registered thresholds to a confidence value:
// >= act is act, >= escalate is gather, anything lower is fallback.
func (p Policy) Classify(confidence float64) string {
	switch {
	case confidence >= p.ActThreshold:
		return Act
	case confidence >= p.EscalateThreshold:
		return Gather
	}
	return Fallback
}

// Decide decides answers (question id to answer) against the set id.
//
// The primary question's answer supplies the confidence: choice and score
// answers carry one, a noul answer's value is its confidence. A choice that is
// not among a non-empty criteria set is a fallback. With JEVKIT_SHADOW=1 the
// would-have decision is logged and the returned decision is fallback.
func (d *Decider) Decide(id string, answers map[string]jev.Answer) (Decision, error) {
	set, ok := d.Registry.Set(id)
	if !ok {
		return Decision{}, fmt.Errorf("%w: %q", ErrUnknownSet, id)
	}
	ans, ok := answers[set.Policy.PrimaryQuestion]
	if !ok || ans == nil {
		return Decision{}, fmt.Errorf("%w: no answer for primary question %q", ErrNoDecision, set.Policy.PrimaryQuestion)
	}
	conf, chosen, hasChoice := primary(ans)
	if math.IsNaN(conf) || math.IsInf(conf, 0) {
		return Decision{}, fmt.Errorf("%w: confidence is not a finite number", ErrNoDecision)
	}

	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	dec := Decision{
		Timestamp:          now().UTC().Format(time.RFC3339),
		Decision:           set.Policy.Classify(conf),
		Chosen:             chosen,
		Confidence:         conf,
		QuestionSetID:      id,
		QuestionSetVersion: set.Version,
		Surface:            set.Surface,
		RegistryVersion:    d.Registry.RegistryVersion,
	}
	dec.Reason = dec.Decision
	if hasChoice && chosen != nil && !offered(set.Questions[set.Policy.PrimaryQuestion], *chosen) {
		dec.Decision, dec.Reason = Fallback, ReasonOptionNotOffered
	}
	dec.FallbackUsed = dec.Decision == Fallback

	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv("JEVKIT_SHADOW") != "1" {
		d.log(dec)
		return dec, nil
	}
	shadow := dec
	shadow.Shadow, shadow.FallbackUsed = true, true
	shadow.Answers = plainAnswers(answers)
	d.log(shadow)
	returned := shadow
	returned.Decision, returned.Reason, returned.Answers = Fallback, ReasonShadow, nil
	return returned, nil
}

// primary extracts confidence and (for choice answers) the chosen option.
func primary(a jev.Answer) (conf float64, chosen *string, hasChoice bool) {
	switch v := a.(type) {
	case jev.NoulAnswer:
		return v.Noul, nil, false
	case jev.ChoiceAnswer:
		c := v.Choice
		return v.Confidence, &c, true
	case jev.ScoreAnswer:
		return v.Confidence, nil, false
	}
	return math.NaN(), nil, false
}

// offered reports whether choice is a declared option. A question with no
// declared options takes them at call time, so anything is offered.
func offered(q Question, choice string) bool {
	var m map[string]string
	if err := json.Unmarshal(q.Criteria, &m); err != nil || len(m) == 0 {
		return true
	}
	_, ok := m[choice]
	return ok
}

func plainAnswers(answers map[string]jev.Answer) map[string]interface{} {
	out := make(map[string]interface{}, len(answers))
	for k, a := range answers {
		switch v := a.(type) {
		case jev.NoulAnswer:
			out[k] = map[string]interface{}{"type": "noul", "noul": v.Noul}
		case jev.ChoiceAnswer:
			out[k] = map[string]interface{}{"type": "choice", "choice": v.Choice, "confidence": v.Confidence}
		case jev.ScoreAnswer:
			out[k] = map[string]interface{}{"type": "score", "score": v.Score, "confidence": v.Confidence}
		}
	}
	return out
}

// log appends dec to decisions.jsonl under a lock. It is best effort: a
// logging failure never changes the decision.
func (d *Decider) log(dec Decision) {
	if d.StateDir != "" {
		_ = AppendDecision(d.StateDir, dec)
	}
}

// AppendDecision writes dec as one line of <stateDir>/jevkit/decisions.jsonl.
func AppendDecision(stateDir string, dec Decision) error {
	line, err := json.Marshal(dec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	path := DecisionsPath(stateDir)
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
