// Package route wraps registry.Decider with everything internal/sdlc/engine
// needs to turn an AskJev effect into a JevDecided event: call-time criteria
// assembly for a select node's candidates and for top-level workflow
// selection (one assembler serving both, so their criteria have identical
// shape), bounded and redacted state, and Jev-unavailable/shadow-mode
// handling that always yields a usable decision — "a run never stalls on the
// classifier". route never decides thresholds itself; every decision is
// registry.Decider's, exactly as internal/mcp already reuses it.
package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/registry"
)

// Asker is the slice of the jev client route needs.
type Asker interface {
	Ask(ctx context.Context, req jev.Request) (*jev.Response, error)
}

// Config wires a Router. Everything except Now is required.
type Config struct {
	// Decider supplies the registry and applies its thresholds; its own
	// Getenv controls JEVKIT_SHADOW, exactly as internal/mcp uses it.
	Decider *registry.Decider
	// Client asks Jev.
	Client Asker
	// Redact scrubs state and call-time rubrics before transport; an error
	// rejects the call rather than sending unredacted text.
	Redact func(string) (string, []redact.Hit, error)
	// Unavailable returns "" when Jev can be used, else a short reason (no
	// key, breaker open). It must not return or log the key.
	Unavailable func(ctx context.Context) string
	// Now stamps a synthesized (Jev-unavailable) decision; nil means
	// time.Now.
	Now func() time.Time
}

// Router asks Jev's registered question sets and applies their policy.
type Router struct{ cfg Config }

// New validates cfg and returns a Router.
func New(cfg Config) (*Router, error) {
	switch {
	case cfg.Decider == nil || cfg.Decider.Registry == nil:
		return nil, errors.New("route: a decider with a registry is required")
	case cfg.Client == nil:
		return nil, errors.New("route: a client is required")
	case cfg.Redact == nil:
		return nil, errors.New("route: a redactor is required")
	case cfg.Unavailable == nil:
		return nil, errors.New("route: an availability check is required")
	}
	return &Router{cfg: cfg}, nil
}

// Result is what a caller turns into an engine.JevDecided event.
type Result struct {
	Decision  registry.Decision
	Available bool
}

// Decide asks question set setID against state (already bounded by the
// caller via Bound; Decide redacts it before it reaches the wire) and
// returns the registry policy's decision.
//
// criteria supplies call-time options for a choice question the registry
// declares with none (sdlc.agent-selection's candidates, or
// sdlc.workflow-selection's available workflows) — id to rubric, the same
// shape for both, which is what makes them "one implementation of call-time
// criteria assembly" per the plan. Pass nil for any other question set,
// whose criteria (if any) are already fixed in the registry.
//
// Decide never returns an error for a reason internal to Jev itself
// (unavailable, transport failure, an unusable response, or a decide
// failure): each of those instead yields Available=false and a synthesized
// fallback Decision, so a caller can always proceed. An error return means
// the call was malformed (an unknown question set, criteria supplied where
// none was needed or omitted where it was, or state that failed redaction).
func (r *Router) Decide(ctx context.Context, setID, state string, criteria map[string]string) (Result, error) {
	set, ok := r.cfg.Decider.Registry.Set(setID)
	if !ok {
		return Result{}, fmt.Errorf("route: unknown question set %q", setID)
	}

	if reason := r.cfg.Unavailable(ctx); reason != "" {
		return Result{Decision: r.synthesizeFallback(set, reason), Available: false}, nil
	}

	redactedCriteria := make(map[string]string, len(criteria))
	for id, rubric := range criteria {
		clean, _, err := r.cfg.Redact(rubric)
		if err != nil {
			return Result{}, fmt.Errorf("route: rubric %q rejected before transport: %w", id, err)
		}
		redactedCriteria[id] = clean
	}
	questions, err := buildQuestions(set, redactedCriteria)
	if err != nil {
		return Result{}, err
	}
	redacted, _, err := r.cfg.Redact(state)
	if err != nil {
		return Result{}, fmt.Errorf("route: state rejected before transport (redaction or size): %w", err)
	}

	resp, err := r.cfg.Client.Ask(ctx, jev.Request{QuestionSetID: setID, State: redacted, Questions: questions})
	if err != nil || resp == nil || len(resp.Answers) == 0 {
		return Result{Decision: r.synthesizeFallback(set, "transport-error"), Available: false}, nil
	}

	dec, err := r.cfg.Decider.Decide(setID, resp.Answers)
	if err != nil {
		return Result{Decision: r.synthesizeFallback(set, "decide-error"), Available: false}, nil
	}
	dec = enforceCallTimeOffer(dec, criteria)
	return Result{Decision: dec, Available: true}, nil
}

// enforceCallTimeOffer extends "a chosen option not among what was offered
// is a fallback" to a call-time criteria set. registry.Decider already
// enforces this for a set with fixed registry criteria, but by design (see
// registry.Policy's own test, "a call-time choice set accepts any option")
// it cannot for one whose registry criteria is empty and filled in at call
// time — it never receives the call-time options at all. route is the one
// place that does, so it closes that gap itself rather than trusting an
// unoffered choice.
func enforceCallTimeOffer(dec registry.Decision, criteria map[string]string) registry.Decision {
	if len(criteria) == 0 || dec.Chosen == nil || dec.Decision == registry.Fallback {
		return dec
	}
	if _, offered := criteria[*dec.Chosen]; !offered {
		dec.Decision = registry.Fallback
		dec.Reason = registry.ReasonOptionNotOffered
		dec.FallbackUsed = true
	}
	return dec
}

func (r *Router) now() time.Time {
	if r.cfg.Now != nil {
		return r.cfg.Now()
	}
	return time.Now()
}

// synthesizeFallback builds the Decision a caller sees when Jev could not be
// asked at all: always "fallback", so routing always has somewhere to go.
func (r *Router) synthesizeFallback(set *registry.Set, reason string) registry.Decision {
	return registry.Decision{
		Timestamp:          r.now().UTC().Format(time.RFC3339),
		Decision:           registry.Fallback,
		FallbackUsed:       true,
		Reason:             reason,
		QuestionSetID:      set.ID,
		QuestionSetVersion: set.Version,
		Surface:            set.Surface,
		RegistryVersion:    r.cfg.Decider.Registry.RegistryVersion,
	}
}

// maxRubricBytes caps one candidate's rubric text before it becomes a
// criteria entry: a single runaway rubric must not be able to dominate a
// selection call's size or crowd out the others.
const maxRubricBytes = 4000

// CriteriaFromRubrics is the one call-time criteria assembler both
// sdlc.agent-selection (id to each candidate's catalog rubric) and
// sdlc.workflow-selection (name to workflow description) use, so their
// criteria have identical shape by construction rather than by two call
// sites happening to agree: an empty id or rubric is dropped (Jev cannot use
// it either way), and each rubric is bounded the same way state is.
func CriteriaFromRubrics(rubrics map[string]string) map[string]string {
	out := make(map[string]string, len(rubrics))
	for id, rubric := range rubrics {
		if id == "" || rubric == "" {
			continue
		}
		out[id] = Bound(rubric, maxRubricBytes)
	}
	return out
}

// emptyChoiceCriteria reports whether q declares a choice question with no
// fixed options — the shape that needs criteria filled in at call time
// (mirrors internal/mcp/tools.go's needsOptions/emptyCriteria).
func emptyChoiceCriteria(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) != nil || len(m) == 0
}

// needsCallTimeCriteria reports whether any question in set is a choice with
// no fixed options.
func needsCallTimeCriteria(set *registry.Set) bool {
	for _, q := range set.Questions {
		if q.Type == "choice" && emptyChoiceCriteria(q.Criteria) {
			return true
		}
	}
	return false
}

// buildQuestions turns a registry set into wire questions. A set with a
// choice question that declares no fixed criteria requires call-time
// criteria; every other set (fixed choice, noul, score) rejects it, so a
// caller can never silently override an authored rubric or pass criteria a
// non-choice question has no use for.
func buildQuestions(set *registry.Set, criteria map[string]string) (map[string]jev.Question, error) {
	switch needs := needsCallTimeCriteria(set); {
	case needs && len(criteria) == 0:
		return nil, fmt.Errorf("route: question set %s needs call-time criteria (none was given)", set.ID)
	case !needs && len(criteria) > 0:
		return nil, fmt.Errorf("route: question set %s has fixed criteria; call-time criteria is not accepted", set.ID)
	}

	out := make(map[string]jev.Question, len(set.Questions))
	for id, q := range set.Questions {
		switch q.Type {
		case "choice":
			c := map[string]json.RawMessage{}
			if emptyChoiceCriteria(q.Criteria) {
				for optID, rubric := range criteria {
					c[optID] = jev.Str(rubric)
				}
			} else if err := json.Unmarshal(q.Criteria, &c); err != nil {
				return nil, fmt.Errorf("route: %s: %w", set.ID, err)
			}
			out[id] = jev.ChoiceQuestion{Instructions: q.Instructions, Criteria: c}
		case "noul":
			crit := map[string]json.RawMessage{}
			if len(q.Criteria) > 0 {
				if err := json.Unmarshal(q.Criteria, &crit); err != nil {
					return nil, fmt.Errorf("route: %s: %w", set.ID, err)
				}
			}
			out[id] = jev.NoulQuestion{Instructions: q.Instructions, Criteria: crit}
		case "score":
			var levels []json.RawMessage
			if err := json.Unmarshal(q.Criteria, &levels); err != nil {
				return nil, fmt.Errorf("route: %s: %w", set.ID, err)
			}
			out[id] = jev.ScoreQuestion{Instructions: q.Instructions, Criteria: levels}
		default:
			return nil, fmt.Errorf("route: %s: unknown question type %q", set.ID, q.Type)
		}
	}
	return out, nil
}
