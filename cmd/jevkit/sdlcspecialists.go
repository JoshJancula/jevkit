package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/route"
)

// Specialist decisions are made only against enrolled, eligible bindings.
// The queue is persisted before another assignment can be reserved.
func (a *App) scheduleSpecialists(ctx context.Context, run ledger.Run, st *adaptive.State, kind string) {
	p, roster, err := a.sdlcEnrollment()
	if err == nil && p.SpecialistMode == "off" {
		st.PendingDecision, st.PendingPhase = "", ""
		st.PendingFocus, st.PendingReason = "", ""
		return
	}
	prior := make(map[string]string)
	for _, d := range st.SpecialistDecisions {
		prior[d.Role+"/"+d.Revision] = d.Choice + "/" + d.Reason
	}
	defer func() {
		store := ledger.Open(a.sdlcRunsDir(), run.RunID)
		for _, d := range st.SpecialistDecisions {
			if prior[d.Role+"/"+d.Revision] == d.Choice+"/"+d.Reason {
				continue
			}
			e := ledger.Decision{RunID: run.RunID, Kind: "specialist-check", Stage: kind, Trigger: d.Role + " for revision " + d.Revision, Choice: d.Choice, Outcome: d.Reason}
			confidence := d.Confidence
			if d.Choice != "" {
				e.Confidence = &confidence
			}
			if strings.Contains(d.Reason, "no eligible") {
				e.Candidates = []ledger.Candidate{{ID: d.Role, Reason: "no-eligible-specialist"}}
			}
			if strings.HasPrefix(d.Reason, "skipped") {
				e.Next = "continue without " + d.Role
			}
			_ = a.recordDecision(store, e)
		}
	}()
	roles := []string{"research"}
	next := adaptive.Implementing
	revision := st.PlanRevision
	if kind == "diff" {
		roles = []string{"qa", "security", "code-review"}
		next = adaptive.Assessing
		revision = st.DiffRevision
	}
	if err != nil {
		st.PendingDecision = kind
		st.PendingPhase = next
		st.PendingFocus = kind
		st.PendingReason = "enrollment unavailable for specialist decision"
		st.Pause("specialist-decision-unavailable")
		return
	}
	var queue []adaptive.SpecialistCheck
	for _, role := range roles {
		candidates := enrollment.Eligible(p, roster, a.cliReach(), enrollment.Requirement{Role: role, ReadOnly: true})
		if len(candidates) == 0 && p.SpecialistMode != "required" {
			recordSpecialistDecision(st, adaptive.SpecialistDecision{Role: role, Revision: revision, Reason: "skipped: no eligible specialist enrolled"})
			continue
		}
		var descriptions []string
		for _, candidate := range candidates {
			descriptions = append(descriptions, candidate.Agent.RoleRubrics[role])
			if descriptions[len(descriptions)-1] == "" {
				descriptions[len(descriptions)-1] = candidate.Agent.Rubric
			}
		}
		needDescription := strings.Join(descriptions, "; ")
		if needDescription == "" {
			needDescription = "The plan or diff needs " + role + " expertise."
		}
		criteria := map[string]string{"needed": needDescription, "skip": "This task does not need " + role + " expertise."}
		state := fmt.Sprintf("Task summary: %s\nCurrent phase: %s\nPlan revision: %s\nDiff revision: %s\nDetermine whether %s expertise is required for this exact artifact.", route.Bound(run.Task, 3000), kind, st.PlanRevision, st.DiffRevision, role)
		artifactName := "plan.md"
		if kind == "diff" {
			artifactName = "patch.diff"
		}
		if artifact, err := ledger.Open(a.sdlcRunsDir(), run.RunID).ReadArtifact(artifactName); err == nil {
			state += "\nArtifact excerpt:\n" + specialistExcerpt(string(artifact), 4000)
		}
		needed := false
		decisionFailure := ""
		choice := ""
		confidence := 0.0
		var decisionErr error
		if a.SdlcSpecialistNeed != nil {
			needed, decisionErr = a.SdlcSpecialistNeed(ctx, role, run.Task, state)
		} else {
			router, routeErr := a.sdlcRouter()
			if routeErr != nil {
				decisionErr = routeErr
				decisionFailure = "router-setup-error"
			} else {
				res, routeErr := router.Decide(ctx, "sdlc.specialist-need", state, route.CriteriaFromRubrics(criteria))
				if routeErr != nil {
					decisionErr = routeErr
					decisionFailure = "routing-error"
				} else {
					confidence = res.Decision.Confidence
					if res.Decision.Chosen != nil {
						choice = *res.Decision.Chosen
					}
					if res.Decision.Decision == registry.Act && res.Decision.Chosen != nil {
						needed = choice == "needed"
					} else {
						decisionFailure = res.Decision.Reason
						if decisionFailure == "" {
							decisionFailure = string(res.Decision.Decision)
						}
						decisionErr = fmt.Errorf("specialist decision unavailable: %s", decisionFailure)
					}
				}
			}
		}
		if decisionErr != nil {
			if decisionFailure == "" {
				decisionFailure = "decision-error"
			}
			if p.SpecialistMode != "required" {
				recordSpecialistDecision(st, adaptive.SpecialistDecision{Role: role, Revision: revision, Choice: choice, Confidence: confidence, Reason: "skipped: " + decisionFailure})
				continue
			}
			st.PendingDecision = kind
			st.PendingPhase = next
			st.PendingFocus = role
			st.PendingReason = "specialist decision unavailable: " + decisionFailure
			st.Pause("specialist-decision-unavailable")
			return
		}
		if needed {
			if len(candidates) == 0 {
				recordSpecialistDecision(st, adaptive.SpecialistDecision{Role: role, Revision: revision, Choice: "needed", Confidence: confidence, Reason: "required specialist unavailable"})
				st.PendingDecision = kind
				st.PendingPhase = next
				st.PendingFocus = criteria["needed"]
				st.PendingReason = "no policy-eligible " + role + " expert"
				st.Pause("unmet-specialist-" + role)
				return
			}
			queue = append(queue, adaptive.SpecialistCheck{Role: role, Focus: criteria["needed"], Revision: revision})
		}
		if choice == "" {
			if needed {
				choice = "needed"
			} else {
				choice = "skip"
			}
		}
		recordSpecialistDecision(st, adaptive.SpecialistDecision{Role: role, Revision: revision, Choice: choice, Confidence: confidence, Reason: "decided"})
	}
	st.PendingDecision, st.PendingPhase = "", ""
	st.PendingFocus, st.PendingReason = "", ""
	if len(queue) > 0 {
		st.SpecialistQueue = queue
		st.AfterSpecialists = next
		st.Stage = adaptive.Specializing
	}
}

func recordSpecialistDecision(st *adaptive.State, decision adaptive.SpecialistDecision) {
	for i, prior := range st.SpecialistDecisions {
		if prior.Role == decision.Role && prior.Revision == decision.Revision {
			st.SpecialistDecisions[i] = decision
			return
		}
	}
	st.SpecialistDecisions = append(st.SpecialistDecisions, decision)
}

func specialistExcerpt(artifact string, limit int) string {
	if len(artifact) <= limit {
		return artifact
	}
	head := limit / 2
	tail := limit - head
	return artifact[:head] + "\n[... middle omitted ...]\n" + artifact[len(artifact)-tail:]
}

// A result is committed before a Jev decision. If the process exits during
// that decision, drive resumes it without invoking the completed worker.
func (a *App) retryActiveSpecialistDecision(ctx context.Context, runID string) (bool, error) {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	run, err := store.ReadRun()
	if err != nil {
		return false, err
	}
	if run.Adaptive == nil || run.Adaptive.Stage == adaptive.Paused || run.Adaptive.PendingDecision != "plan" && run.Adaptive.PendingDecision != "diff" {
		return false, nil
	}
	policy, _, err := a.sdlcEnrollment()
	if err != nil {
		return true, err
	}
	remaining, err := a.treeRemaining(run, policy)
	if err != nil {
		return true, err
	}
	if remaining <= 0 {
		run.Adaptive.Pause("run-time-budget-exhausted")
		_ = store.WriteRun(run)
		return true, failf("run %s paused: %s", runID, run.Adaptive.Outcome)
	}
	err = store.WithRunLock(func() error {
		fresh, err := store.ReadRun()
		if err != nil {
			return err
		}
		st := *fresh.Adaptive
		kind := st.PendingDecision
		st.Stage = st.PendingPhase
		st.Outcome = ""
		a.scheduleSpecialists(ctx, fresh, &st, kind)
		fresh.Adaptive = &st
		return store.WriteRun(fresh)
	})
	if err != nil {
		return true, err
	}
	updated, err := store.ReadRun()
	if err != nil {
		return true, err
	}
	if updated.Adaptive.Stage == adaptive.Paused {
		return true, failf("run %s paused: %s", runID, updated.Adaptive.Outcome)
	}
	return true, nil
}
