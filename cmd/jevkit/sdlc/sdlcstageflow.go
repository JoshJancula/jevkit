package sdlc

import (
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

// stageflowRouteAfterResult decides whether an authored StageFlow should advance
// after an agent result. Fan-out, supervisor verification, and the completion
// gate live inside the current implementation (or assessment) stage; routes
// such as `changed: assess` fire only after those supervisor steps finish.
func stageflowRouteAfterResult(previousStage, newStage, resultOutcome string, st adaptive.State) (route string, advance bool) {
	if newStage == previousStage || newStage == adaptive.Paused {
		return "", false
	}
	switch {
	case previousStage == adaptive.Implementing && (newStage == adaptive.Verifying || newStage == adaptive.Specializing):
		// Stay on the implement work stage through fan-out join and checks.
		return "", false
	case previousStage == adaptive.Assessing && newStage == adaptive.Verifying:
		// Completion gate returned the run to supervisor verification.
		return "", false
	case previousStage == adaptive.Verifying || previousStage == adaptive.Specializing:
		// Supervisor / specialist transitions are applied on their own paths.
		return "", false
	case previousStage == adaptive.Assessing:
		if newStage != adaptive.Implementing && newStage != adaptive.Done {
			return "", false
		}
		route = "approved"
		for _, assessment := range st.Assessments {
			if assessment.Revision == st.DiffRevision && !assessment.Approved {
				route = "changes-required"
				break
			}
		}
		return route, true
	default:
		return resultOutcome, true
	}
}

// sdlcAdvanceStageFlowAfterVerification moves an authored implement work stage
// to its `changed` route once supervisor verification has entered assessing
// (or a finish stage). Older saved workflows keep `changed: assess` routes;
// no YAML migration is required.
func (a *App) sdlcAdvanceStageFlowAfterVerification(store *ledger.Store, run *ledger.Run, st *adaptive.State) error {
	if run == nil || run.StageFlow == nil || st == nil {
		return nil
	}
	if st.Stage != adaptive.Assessing && st.Stage != adaptive.Done {
		return nil
	}
	stage, ok := run.StageFlow.Stage()
	if !ok || stage.Work == nil || stage.Work.Role != "implementer" {
		return nil
	}
	if _, has := stage.Work.Routes["changed"]; !has {
		return nil
	}
	if err := run.StageFlow.Advance("changed", st); err != nil {
		return app.Failf("advance workflow after verification: %v", err)
	}
	policy, _, err := a.sdlcEnrollment()
	if err != nil {
		return err
	}
	if err := a.chargeTree(run, policy, "step"); err != nil {
		st.Pause("stage-step-budget-exhausted")
	}
	run.Adaptive = st
	run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
	if err := store.WriteRun(*run); err != nil {
		return app.Failf("store workflow after verification: %v", err)
	}
	_ = a.recordDecision(store, ledger.Decision{
		RunID: run.RunID, Kind: "stage-transition", Stage: stage.ID,
		Trigger: "supervisor-verification", Choice: "changed", Outcome: st.Stage,
		Next: run.StageFlow.Current, Detail: "implementation stage advanced after join/verification",
	})
	return nil
}

// hostMayNotForgeSupervisorOutcome rejects host report outcomes that attempt to
// invent Jevkit's supervisor-owned check or integration result.
func hostMayNotForgeSupervisorOutcome(outcome string) error {
	switch outcome {
	case "verified", "verification-passed", "verification-failed", "checks-passed", "checks-failed",
		"integrated", "integration-applied", "integration-failed", "fanout-complete":
		return app.Usagef("hosts may report agent results only; supervisor verification and integration are Jevkit-owned")
	default:
		return nil
	}
}
