package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/route"
)

var sdlcRunIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func sdlcRunRemaining(run ledger.Run, policy enrollment.Policy, now time.Time) (time.Duration, error) {
	created, err := time.Parse(time.RFC3339, run.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("run %s has invalid creation time: %w", run.RunID, err)
	}
	return created.Add(time.Duration(policy.MaxRunSeconds) * time.Second).Sub(now), nil
}

func (a *App) adaptiveCandidates(st adaptive.State, reach enrollment.Reach) ([]enrollment.Candidate, error) {
	p, r, err := a.sdlcEnrollment()
	if err != nil {
		return nil, err
	}
	q, err := p.Quorum(st.Profile)
	if err != nil {
		return nil, err
	}
	if q > st.Quorum {
		return nil, fmt.Errorf("project policy now requires quorum %d; run was started with %d", q, st.Quorum)
	}
	excluded := map[string]bool{}
	for id, b := range st.Excluded {
		excluded[id] = b
	}
	if st.Stage == adaptive.Assessing {
		for _, v := range st.Assessments {
			if v.Revision == st.DiffRevision {
				excluded[v.AgentID] = true
			}
		}
		for _, v := range st.Pending() {
			excluded[v.AgentID] = true
		}
	}
	candidates := enrollment.Eligible(p, r, reach, enrollment.Requirement{Role: st.Role(), Write: st.Stage == adaptive.Implementing, Excluded: excluded})
	withoutFailedBindings := candidates[:0]
	for _, c := range candidates {
		if !st.ExcludedBindings[c.Binding] && !st.ExcludedRuntimes[c.Agent.Runtime] {
			withoutFailedBindings = append(withoutFailedBindings, c)
		}
	}
	candidates = withoutFailedBindings
	if st.Stage == adaptive.Assessing {
		used := map[string]bool{}
		for _, v := range st.Assessments {
			if v.Revision == st.DiffRevision {
				used[v.Binding] = true
			}
		}
		for _, v := range st.Pending() {
			used[v.Binding] = true
		}
		filtered := candidates[:0]
		for _, c := range candidates {
			if !used[c.Binding] {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}
	return candidates, nil
}

func (a *App) sdlcNextCmd() *cobra.Command {
	return &cobra.Command{Use: "next <run-id>", Hidden: true, Short: "host integration: reserve the next agent assignment as JSON", Args: cobra.ExactArgs(1),
		Long: `Use next when building a host integration that executes agents itself.
It reads the run's current work stage (plan, implement, or assess), chooses an
eligible enrolled agent, and reserves an invocation. Its JSON response tells
the host which agent and task to run. It does not launch an agent.

At a custom workflow question stage, use sdlc resume RUN_ID --step to ask Jev and advance
to the selected stage.

After the host finishes that invocation, call sdlc report with the returned
invocation ID and outcome. Ordinary CLI users use sdlc run and resume;
those commands handle both assignment and execution.`,
		Example: "  jevkit sdlc next RUN_ID\n  jevkit sdlc report RUN_ID --invocation INVOCATION_ID --agent AGENT_ID --outcome planned --file plan.md",
		RunE:    func(cmd *cobra.Command, args []string) error { return a.sdlcNext(cmd.Context(), args[0]) }}
}

func (a *App) sdlcNext(ctx context.Context, runID string) error {
	assignment, err := a.sdlcAssignNext(ctx, runID)
	if err != nil {
		return err
	}
	if assignment != nil {
		data, _ := json.Marshal(assignment)
		a.outf("%s\n", data)
	}
	return nil
}

func (a *App) sdlcAssignNext(ctx context.Context, runID string) (*adaptive.Assignment, error) {
	if !sdlcRunIDRE.MatchString(runID) {
		return nil, usagef("invalid run ID")
	}
	store := ledger.Open(a.sdlcRunsDir(), runID)
	var assignment *adaptive.Assignment
	err := store.WithRunLock(func() error {
		var innerErr error
		assignment, innerErr = a.sdlcAssignNextLocked(ctx, runID, store)
		return innerErr
	})
	return assignment, err
}

func (a *App) sdlcAssignNextLocked(ctx context.Context, runID string, store *ledger.Store) (*adaptive.Assignment, error) {
	run, err := store.ReadRun()
	if err != nil {
		return nil, failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return nil, failf("run %s has an unsupported run format", runID)
	}
	if run.StageFlow != nil && run.Adaptive.Stage == "question" {
		return nil, failf("run %s is at question %s; use sdlc resume %s --step to ask Jev", runID, run.StageFlow.Current, runID)
	}
	if run.StageFlow != nil && run.Adaptive.Stage == "spawn" {
		return nil, failf("run %s is at workflow stage %s; use sdlc resume %s --step to run it", runID, run.StageFlow.Current, runID)
	}
	st := *run.Adaptive
	p, _, err := a.sdlcEnrollment()
	if err != nil {
		return nil, failf("%v", err)
	}
	if p.MaxConcurrent < st.MaxConcurrent {
		st.MaxConcurrent = p.MaxConcurrent
	}
	if p.MaxAssignments < st.MaxAssignments {
		st.MaxAssignments = p.MaxAssignments
	}
	if p.MaxRevisions < st.MaxRevisions {
		st.MaxRevisions = p.MaxRevisions
	}
	if p.MaxEstimatedCostUSD > 0 && (st.MaxEstimatedCostUSD == 0 || p.MaxEstimatedCostUSD < st.MaxEstimatedCostUSD) {
		st.MaxEstimatedCostUSD = p.MaxEstimatedCostUSD
	}
	if st.Role() == "" {
		a.outf("run %s: %s (%s)\n", runID, st.Stage, st.Outcome)
		return nil, nil
	}
	remaining, err := sdlcRunRemaining(run, p, a.now())
	if err != nil {
		return nil, failf("%v", err)
	}
	if remaining <= 0 {
		st.Pause("run-time-budget-exhausted")
		run.Adaptive = &st
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return nil, failf("%v", err)
		}
		return nil, failf("run %s paused: %s", runID, st.Outcome)
	}
	if st.Stage == adaptive.Implementing && st.DiffRevision != "" && st.RevisionCount >= st.MaxRevisions {
		st.Pause("revision-budget-exhausted")
		run.Adaptive = &st
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return nil, failf("%v", err)
		}
		return nil, failf("run %s paused: %s", runID, st.Outcome)
	}
	if st.BudgetExhausted() {
		st.Pause("assignment-budget-exhausted")
		run.Adaptive = &st
		if err := store.WriteRun(run); err != nil {
			return nil, failf("%v", err)
		}
		return nil, failf("run %s paused: %s", runID, st.Outcome)
	}
	if st.Stage == adaptive.Assessing && len(st.Assessments)+len(st.Assignments) >= st.Quorum {
		return nil, a.printPending(runID, st)
	}
	if len(st.Assignments) >= st.MaxConcurrent || st.Stage != adaptive.Assessing && len(st.Assignments) > 0 {
		return nil, a.printPending(runID, st)
	}
	candidates, err := a.adaptiveCandidates(st, a.cliReach())
	if err != nil {
		return nil, failf("%v", err)
	}
	if len(candidates) == 0 {
		st.Pause("no-eligible-" + st.Role())
		run.Adaptive = &st
		run.UpdatedAt = a.now().UTC().Format("2006-01-02T15:04:05Z")
		if err := store.WriteRun(run); err != nil {
			return nil, failf("%v", err)
		}
		return nil, failf("run %s paused: %s", runID, st.Outcome)
	}
	if st.Stage == adaptive.Assessing {
		needed := st.Quorum - len(st.Assessments) - len(st.Assignments)
		if enrollment.DistinctBindings(candidates) < needed {
			st.Pause("assessment-quorum-unavailable")
			run.Adaptive = &st
			if err := store.WriteRun(run); err != nil {
				return nil, failf("%v", err)
			}
			return nil, failf("run %s paused: assessment quorum unavailable", runID)
		}
	}
	choice := candidates[0]
	if len(candidates) > 1 {
		router, err := a.sdlcRouter()
		if err != nil {
			return nil, failf("%v", err)
		}
		state := fmt.Sprintf("task kind: %s\ntask: %s\nrole: %s\nplan revision: %s\ndiff revision: %s", st.TaskKind, run.Task, st.Role(), st.PlanRevision, st.DiffRevision)
		result, err := router.Decide(ctx, "sdlc.agent-selection", state, route.CriteriaFromRubrics(enrollment.Rubrics(candidates)))
		if err != nil {
			return nil, failf("route agent: %v", err)
		}
		if result.Decision.Decision == registry.Act || result.Decision.Decision == registry.Gather {
			for _, c := range candidates {
				if result.Decision.Chosen != nil && c.Agent.ID == *result.Decision.Chosen {
					choice = c
					break
				}
			}
		}
	}
	rule := p.Roles[st.Role()]
	assignment := adaptive.Assignment{InvocationID: a.newRunID(a.now()), AgentID: choice.Agent.ID, Binding: choice.Binding, Via: choice.Agent.Via, Runtime: choice.Agent.Runtime, Role: st.Role(), ReadOnly: !rule.Write || rule.ReadOnly || choice.Agent.ReadOnly, Isolated: rule.Isolated || choice.Agent.Isolated, ProjectWriteScopes: append([]string(nil), rule.WriteScopes...), AgentWriteScopes: append([]string(nil), choice.Agent.WriteScopes...)}
	if run.StageFlow != nil {
		stage, ok := run.StageFlow.Stage()
		if !ok || stage.Work == nil {
			return nil, failf("run %s has invalid current work stage", runID)
		}
		assignment.StageID, assignment.Objective = stage.ID, stage.Work.Objective
	}
	if st.Stage == adaptive.Assessing {
		assignment.Revision = st.DiffRevision
	} else if st.Stage == adaptive.Implementing {
		assignment.Revision = st.PlanRevision
	}
	if err := st.Assign(assignment); err != nil {
		return nil, failf("%v", err)
	}
	run.Adaptive = &st
	run.UpdatedAt = a.now().UTC().Format("2006-01-02T15:04:05Z")
	if err := store.WriteRun(run); err != nil {
		return nil, failf("store assignment: %v", err)
	}
	return &assignment, nil
}

func (a *App) printPending(runID string, st adaptive.State) error {
	for _, x := range st.Pending() {
		data, _ := json.Marshal(x)
		a.outf("%s\n", data)
	}
	return nil
}

func (a *App) sdlcReportCmd() *cobra.Command {
	var invocation, agent, outcome, revision, artifactFile string
	var cost float64
	c := &cobra.Command{Use: "report <run-id>", Hidden: true, Short: "host integration: record the outcome of a next assignment", Args: cobra.ExactArgs(1),
		Long: `After a host integration executes the agent selected by sdlc next,
report the result using its invocation and agent IDs. This advances the run
to its next stage. Ordinary CLI users can use sdlc run or resume, which report
workers' outcomes automatically.`,
		RunE: func(_ *cobra.Command, args []string) error {
			if !sdlcRunIDRE.MatchString(args[0]) {
				return usagef("invalid run ID")
			}
			store := ledger.Open(a.sdlcRunsDir(), args[0])
			run, err := store.ReadRun()
			if err != nil {
				return failf("read run: %v", err)
			}
			if run.Adaptive == nil {
				return failf("run %s has an unsupported run format", args[0])
			}
			var artifact []byte
			artifactName := ""
			if outcome == "planned" || outcome == "changed" {
				if artifactFile == "" {
					return usagef("--file is required for %s; revision is computed from its contents", outcome)
				}
				artifact, err = os.ReadFile(artifactFile)
				if err != nil {
					return failf("read worker artifact: %v", err)
				}
				actual := fmt.Sprintf("%x", sha256.Sum256(artifact))
				if revision != "" && revision != actual {
					return usagef("--revision does not match --file contents")
				}
				revision = actual
				if outcome == "planned" {
					artifactName = "plan.md"
				} else {
					artifactName = "patch.diff"
				}
			}
			return a.sdlcRecordResult(args[0], adaptive.Result{InvocationID: invocation, AgentID: agent, Outcome: outcome, Revision: revision, CostUSD: cost}, artifactName, artifact)
		}}
	c.Flags().StringVar(&invocation, "invocation", "", "independent invocation ID from sdlc next")
	c.Flags().StringVar(&agent, "agent", "", "enrolled agent ID from sdlc next")
	c.Flags().StringVar(&outcome, "outcome", "", "structured result: planned, changed, answer, no-change, approved, changes-required, auth-failed, failed, timed-out")
	c.Flags().StringVar(&revision, "revision", "", "exact plan or diff content digest")
	c.Flags().StringVar(&artifactFile, "file", "", "plan or diff artifact for planned and changed outcomes")
	c.Flags().Float64Var(&cost, "cost-usd", 0, "estimated invocation cost in USD")
	_ = c.MarkFlagRequired("invocation")
	_ = c.MarkFlagRequired("agent")
	_ = c.MarkFlagRequired("outcome")
	return c
}

func (a *App) sdlcRecordResult(runID string, result adaptive.Result, artifactName string, artifact []byte) error {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	return store.WithRunLock(func() error { return a.sdlcRecordResultLocked(runID, result, artifactName, artifact, store) })
}

func (a *App) sdlcRecordResultLocked(runID string, result adaptive.Result, artifactName string, artifact []byte, store *ledger.Store) error {
	run, err := store.ReadRun()
	if err != nil {
		return failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return failf("run %s has an unsupported run format", runID)
	}
	st := *run.Adaptive
	previousStage := st.Stage
	if st.Role() != "" && result.Outcome != "run-time-exhausted" {
		policy, _, err := a.sdlcEnrollment()
		if err != nil {
			return failf("%v", err)
		}
		remaining, err := sdlcRunRemaining(run, policy, a.now())
		if err != nil {
			return failf("%v", err)
		}
		if remaining <= 0 {
			st.Pause("run-time-budget-exhausted")
			run.Adaptive = &st
			run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
			if err := store.WriteRun(run); err != nil {
				return failf("store timeout: %v", err)
			}
			return failf("run %s paused: %s", runID, st.Outcome)
		}
	}
	if err := st.Apply(result); err != nil {
		return usagef("%v", err)
	}
	if artifactName != "" {
		if err := store.WriteArtifact(artifactName, artifact); err != nil {
			return failf("store artifact: %v", err)
		}
	}
	if result.Outcome == "auth-failed" && st.Role() != "" {
		candidates, err := a.adaptiveCandidates(st, a.cliReach())
		if err != nil {
			return failf("%v", err)
		}
		remaining := 1
		if st.Stage == adaptive.Assessing {
			remaining = st.Quorum - len(st.Assessments) - len(st.Assignments)
		}
		if enrollment.DistinctBindings(candidates) < remaining {
			st.Pause("no-eligible-" + st.Role())
		}
		if st.BudgetExhausted() {
			st.Pause("assignment-budget-exhausted")
		}
	}
	if run.StageFlow != nil && st.Stage != adaptive.Paused && st.Stage != previousStage {
		outcome := result.Outcome
		if previousStage == adaptive.Assessing {
			outcome = "approved"
			for _, assessment := range st.Assessments {
				if assessment.Revision == st.DiffRevision && !assessment.Approved {
					outcome = "changes-required"
					break
				}
			}
		}
		if err := run.StageFlow.Advance(outcome, &st); err != nil {
			return failf("advance workflow: %v", err)
		}
	}
	run.Adaptive = &st
	run.UpdatedAt = a.now().UTC().Format("2006-01-02T15:04:05Z")
	if err := store.WriteRun(run); err != nil {
		return failf("store outcome: %v", err)
	}
	a.outf("run %s: %s", runID, st.Stage)
	if st.Outcome != "" {
		a.outf(" (%s)", st.Outcome)
	}
	a.outf("\n")
	return nil
}
