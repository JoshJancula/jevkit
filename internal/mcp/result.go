package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/registry"
)

// compactDecision is the policy outcome an agent needs from a registry
// decision. The full record, including every answer, stays in
// decisions.jsonl; repeating the answers here only doubles the tokens a
// caller has to read.
type compactDecision struct {
	Decision           string  `json:"decision"`
	Reason             string  `json:"reason"`
	Chosen             *string `json:"chosen,omitempty"`
	Confidence         float64 `json:"confidence"`
	QuestionSetVersion int     `json:"questionSetVersion"`
	Shadow             bool    `json:"shadow,omitempty"`
}

func compact(dec registry.Decision) compactDecision {
	return compactDecision{
		Decision:           dec.Decision,
		Reason:             dec.Reason,
		Chosen:             dec.Chosen,
		Confidence:         dec.Confidence,
		QuestionSetVersion: dec.QuestionSetVersion,
		Shadow:             dec.Reason == registry.ReasonShadow,
	}
}

// guidance turns a decision into one plain instruction for the calling
// agent: what the decision lets it rely on and, for gather, what evidence to
// add before asking again. missing names optional state keys the caller left
// out, which are usually the cheapest evidence to add. It is advice, never an
// action jevkit takes.
func guidance(set *registry.Set, dec registry.Decision, missing []string) string {
	p := set.Policy
	switch {
	case dec.Reason == registry.ReasonShadow:
		return "Shadow mode is on (JEVKIT_SHADOW=1): the answer is logged but not endorsed. Use your own judgment."
	case dec.Reason == registry.ReasonOptionNotOffered:
		return "The answer named an option that was not offered; ignore it. Fallback: " + p.Fallback + "."
	case dec.Decision == registry.Act:
		return fmt.Sprintf("Confidence %.2f clears the act threshold (%.2f): you can rely on this answer as decision support. You still own the action.",
			dec.Confidence, p.ActThreshold)
	case dec.Decision == registry.Gather:
		var b strings.Builder
		fmt.Fprintf(&b, "Confidence %.2f is below the act threshold (%.2f), so treat the answer as a lean, not a decision.", dec.Confidence, p.ActThreshold)
		if dec.Chosen != nil {
			fmt.Fprintf(&b, " It leans %q.", *dec.Chosen)
		}
		if len(missing) > 0 {
			fmt.Fprintf(&b, " Not provided: state.%s.", strings.Join(missing, ", state."))
		}
		if p.GatherHint != "" {
			b.WriteString(" To firm it up: " + p.GatherHint)
		} else {
			b.WriteString(" Add more specific evidence to state and ask again, or decide on your own judgment.")
		}
		return b.String()
	default:
		return fmt.Sprintf("Confidence %.2f is too low to use. Ignore the answer and fall back to: %s.", dec.Confidence, p.Fallback)
	}
}

// rankedOption is one choice option with its probability.
type rankedOption struct {
	Option      string  `json:"option"`
	Probability float64 `json:"p"`
}

// ranked orders a choice answer's options by probability, highest first,
// dropping zero-probability options. Ties keep a stable, name-sorted order.
func ranked(a jev.Answer) []rankedOption {
	c, ok := a.(jev.ChoiceAnswer)
	if !ok || len(c.Probabilities) == 0 {
		return nil
	}
	out := make([]rankedOption, 0, len(c.Probabilities))
	for k, p := range c.Probabilities {
		if p > 0 {
			out = append(out, rankedOption{k, p})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Probability != out[j].Probability {
			return out[i].Probability > out[j].Probability
		}
		return out[i].Option < out[j].Option
	})
	return out
}

// summary is the one-line text content: the primary answer, the decision and
// the guidance, so a client that only shows text still gets what it needs.
func summary(key, id string, set *registry.Set, answers map[string]jev.Answer, dec registry.Decision, guide string) string {
	return fmt.Sprintf("%s=%s decision=%s %s=%s confidence=%.2f\n%s",
		key, id, dec.Decision, set.Policy.PrimaryQuestion, answerValue(answers[set.Policy.PrimaryQuestion]), dec.Confidence, guide)
}

func answerValue(a jev.Answer) string {
	switch v := a.(type) {
	case jev.ChoiceAnswer:
		return v.Choice
	case jev.ScoreAnswer:
		return fmt.Sprintf("%.2f", v.Score)
	case jev.NoulAnswer:
		return fmt.Sprintf("%.2f", v.Noul)
	}
	return "?"
}
