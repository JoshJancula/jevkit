package compact

import (
	"math"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/registry"
)

var locusOrder = []string{"throughout", "first-failure", "scattered", "tail", "head", "nowhere"}
var outcomeOrder = []string{"unknown", "interrupted", "blocked-environment", "partial-failure", "failure", "success-with-warnings", "no-op", "success"}
var kindOrder = []string{"data-query", "log-stream", "migration-schema", "deploy-infra", "other", "test-run", "build-compile", "typecheck", "lint-format", "dependency-install", "help-usage", "version-probe"}
var validOutcomes = map[string]bool{"success": true, "success-with-warnings": true, "no-op": true, "partial-failure": true, "failure": true, "blocked-environment": true, "interrupted": true, "unknown": true}
var validKinds = map[string]bool{"test-run": true, "build-compile": true, "typecheck": true, "lint-format": true, "dependency-install": true, "migration-schema": true, "deploy-infra": true, "data-query": true, "log-stream": true, "help-usage": true, "version-probe": true, "other": true}

func choiceAnswer(answers map[string]jev.Answer, key string, valid map[string]bool) (jev.ChoiceAnswer, bool) {
	a, ok := answers[key].(jev.ChoiceAnswer)
	return a, ok && valid[a.Choice] && a.Confidence >= 0 && a.Confidence <= 1
}

func cumulativeLocus(a jev.ChoiceAnswer, threshold float64) (string, bool) {
	locus, _, ok := cumulativeLocusMass(a, threshold)
	return locus, ok
}

func cumulativeLocusMass(a jev.ChoiceAnswer, threshold float64) (string, float64, bool) {
	return cumulativeOrderedChoice(a, locusOrder, threshold)
}

// An order starts with the safest label. We accumulate from the aggressive
// end and stop at the first label whose suffix reaches the confidence gate.
// That can only choose a label at least as conservative as the model's mass
// supports, even when no single top choice clears the gate.
func cumulativeOrderedChoice(a jev.ChoiceAnswer, order []string, threshold float64) (string, float64, bool) {
	if len(a.Probabilities) == 0 {
		return a.Choice, a.Confidence, a.Confidence >= threshold
	}
	valid := make(map[string]bool, len(order))
	for _, label := range order {
		valid[label] = true
	}
	for label, p := range a.Probabilities {
		if !valid[label] || math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return "", 0, false
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		label := order[i]
		mass := 0.0
		for _, candidate := range order[i:] {
			p := a.Probabilities[candidate]
			mass += p
		}
		if mass >= threshold {
			return label, mass, true
		}
	}
	return "", 0, false
}

func oneStepSafer(locus string) string {
	for i, label := range locusOrder {
		if label == locus {
			if i == 0 {
				return label
			}
			return locusOrder[i-1]
		}
	}
	return "throughout"
}

func budgetLevel(a jev.ScoreAnswer) int {
	if math.IsNaN(a.Score) || a.Score < 0 || a.Score > 1 {
		return 5
	}
	level := int(math.Ceil(a.Score*5 - 0.000001))
	if len(a.Distribution) == 0 || len(a.Legend) != 6 {
		return min(5, level+1)
	}
	best := 0
	for i, label := range a.Legend {
		if a.Distribution[label] > a.Distribution[a.Legend[best]] {
			best = i
		}
	}
	disagreement := best - level
	if disagreement < 0 {
		disagreement = -disagreement
	}
	if best > level {
		level = best
	}
	if disagreement > 1 {
		level = min(5, level+1)
	}
	return level
}

func strongNoul(answers map[string]jev.Answer, key string, threshold float64) (yes, no bool) {
	a, ok := answers[key].(jev.NoulAnswer)
	if !ok || math.IsNaN(a.Noul) {
		return false, false
	}
	return a.Noul >= threshold, a.Noul <= 1-threshold
}

func triageDisposition(answers map[string]jev.Answer, decision registry.Decision, threshold float64, exit int, authoritative, pointer bool, markerCount, middleMarkers int) (locus, outcome, kind string, level int, ok bool) {
	la, lok := choiceAnswer(answers, "evidence_locus", map[string]bool{"throughout": true, "first-failure": true, "scattered": true, "tail": true, "head": true, "nowhere": true})
	oa, ook := choiceAnswer(answers, "outcome", validOutcomes)
	ka, kok := choiceAnswer(answers, "content_kind", validKinds)
	ba, bok := answers["retention_budget"].(jev.ScoreAnswer)
	if !lok || !ook || !kok || !bok || decision.Decision == registry.Fallback {
		return "", "", "", 5, false
	}
	locus, ok = cumulativeLocus(la, threshold)
	if !ok {
		return "", "", "", 5, false
	}
	outcome, _, ok = cumulativeOrderedChoice(oa, outcomeOrder, threshold)
	if !ok {
		return "", "", "", 5, false
	}
	kind, _, ok = cumulativeOrderedChoice(ka, kindOrder, threshold)
	if !ok {
		return "", "", "", 5, false
	}
	level = budgetLevel(ba)
	if authoritative && exit != 0 && (outcome == "success" || outcome == "no-op" || outcome == "success-with-warnings") {
		return "", "", "", 5, false
	}
	if decision.Decision == registry.Gather {
		locus = oneStepSafer(locus)
		level = min(5, level+1)
		if locus == "head" || locus == "tail" || locus == "nowhere" {
			locus = "first-failure"
		}
	}
	if outcome == "partial-failure" || outcome == "blocked-environment" || outcome == "interrupted" || outcome == "unknown" {
		level = max(level, 3)
		if locus == "nowhere" {
			locus = "first-failure"
		}
	}
	if kind == "migration-schema" || kind == "deploy-infra" {
		level = max(level, 4)
		if locus == "nowhere" {
			locus = "first-failure"
		}
	}
	if kind == "data-query" || kind == "log-stream" {
		locus = "throughout"
	}
	if !pointer && locus == "nowhere" {
		locus = "tail"
	}
	if locus == "scattered" && markerCount > 0 {
		if yes, _ := strongNoul(answers, "single_failure_site", threshold); yes {
			locus = "first-failure"
		}
	}
	if locus == "tail" {
		yes, _ := strongNoul(answers, "tail_explains", threshold)
		if !yes {
			locus = "first-failure"
		}
	}
	if locus == "head" {
		yes, _ := strongNoul(answers, "head_explains", threshold)
		if !yes {
			locus = "first-failure"
		}
	}
	if locus == "tail" || locus == "head" || locus == "nowhere" {
		_, no := strongNoul(answers, "middle_omission_safe", threshold)
		if no {
			locus = "throughout"
		}
	}
	if locus == "first-failure" && markerCount == 0 {
		locus = "throughout"
	}
	return locus, outcome, kind, level, true
}
