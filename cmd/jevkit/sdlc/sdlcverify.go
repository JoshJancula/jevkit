package sdlc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

// sdlcEnsureVerification is the supervisor-owned verification step that runs
// after implementation (stage verifying, before assessor assignment). It runs
// authorized checks against the exact candidate worktree, stores owner-only
// receipts, and on failure returns the run to implementation with only a
// bounded failure summary plus log path for the implementer.
//
// Returns (ran, err). ran is false when verification was already valid for the
// current worktree identity or there was nothing to do.
func (a *App) sdlcEnsureVerification(ctx context.Context, store *ledger.Store, run *ledger.Run) (bool, error) {
	if run == nil || run.Adaptive == nil {
		return false, nil
	}
	st := run.Adaptive
	if st.Stage != adaptive.Verifying {
		return false, nil
	}
	if st.DiffRevision == "" {
		return false, nil
	}

	if err := a.budgetGate(run, mustPolicy(a), "work"); err != nil {
		return false, err
	}
	stop, err := a.budgetActivity(ctx, *run, run.RunID+"/verification")
	if err != nil {
		return false, err
	}
	defer stop()
	st.TreeBudget = true

	checksRaw, err := store.ReadArtifact(adaptive.ArtifactChecks)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, app.Failf("read checks: %v", err)
	}
	var file adaptive.ChecksFile
	if err := json.Unmarshal(checksRaw, &file); err != nil {
		return false, app.Failf("parse checks.json: %v", err)
	}
	checksDigest := adaptive.DigestHex(checksRaw)
	if !adaptive.HasArgvChecks(file.Checks) {
		// Persist an empty/manual skip record once so assessors see explicit state.
		if run.Verification != nil && run.Verification.Status == adaptive.VerificationStatusSkipped &&
			run.Verification.CandidateFingerprint == st.DiffRevision {
			return false, nil
		}
		rec := adaptive.VerificationRecord{
			Status:               adaptive.VerificationStatusSkipped,
			PlanDigest:           st.PlanRevision,
			ChecksDigest:         checksDigest,
			CandidateFingerprint: st.DiffRevision,
			AllPassed:            true,
			At:                   a.Clock().UTC().Format("2006-01-02T15:04:05Z"),
			Summary:              "no argv checks to run",
		}
		for _, c := range file.Checks {
			rec.Receipts = append(rec.Receipts, adaptive.CheckReceipt{
				CheckID: c.ID, Manual: c.Manual, PlanDigest: st.PlanRevision,
				ChecksDigest: checksDigest, CandidateFingerprint: st.DiffRevision,
				At: rec.At, Passed: true, Detail: "manual check; not executed by supervisor",
			})
		}
		return true, a.persistVerification(store, run, rec, "")
	}

	if argvChecksNeedAuthorization(file.Checks, run.AuthorizedChecksRevision, checksDigest) {
		st.Pause("command-authorization-required")
		st.PendingReason = "authorize planner-proposed commands before verification; use jevkit sdlc resume " + run.RunID + " --authorize-checks"
		run.Adaptive = st
		run.UpdatedAt = a.Clock().UTC().Format("2006-01-02T15:04:05Z")
		if err := store.WriteRun(*run); err != nil {
			return false, app.Failf("%v", err)
		}
		return false, app.Failf("run %s paused: authorize planner-proposed commands before verification", run.RunID)
	}

	workspace := run.WorkDir
	if workspace == "" {
		workspace = a.WorkDir
	}
	worktreeID, err := worker.CaptureWorktreeIdentity(ctx, workspace)
	if err != nil {
		return false, app.Failf("capture worktree identity: %v", err)
	}
	if run.Verification != nil && run.Verification.AllPassed && !run.Verification.Invalidated &&
		adaptive.ReceiptsMatchCandidate(run.Verification.Receipts, worktreeID, st.DiffRevision) &&
		adaptive.ReceiptsMatchCandidate(st.CheckReceipts, worktreeID, st.DiffRevision) {
		return false, nil
	}

	logDir := filepath.Join(store.Dir, "logs", "verification")
	result, err := worker.RunVerification(ctx, worker.VerifyRequest{
		Checks:               file.Checks,
		ChecksDigest:         checksDigest,
		AuthorizedDigest:     run.AuthorizedChecksRevision,
		PlanDigest:           st.PlanRevision,
		CandidateFingerprint: st.DiffRevision,
		Workspace:            workspace,
		AllowRead:            append([]string(nil), run.AllowRead...),
		LogDir:               logDir,
		OutputPathPrefix:     adaptive.VerificationLogDirRelative,
		Now:                  a.Clock(),
	})
	if err != nil && result.Record.Status != adaptive.VerificationStatusUnauthorized {
		return false, app.Failf("verification: %v", err)
	}
	if err != nil && result.Record.Status == adaptive.VerificationStatusUnauthorized {
		st.Pause("command-authorization-required")
		st.PendingReason = err.Error()
		run.Adaptive = st
		run.UpdatedAt = a.Clock().UTC().Format("2006-01-02T15:04:05Z")
		_ = store.WriteRun(*run)
		return false, app.Failf("run %s paused: %v", run.RunID, err)
	}

	summaryText := result.FailureSummary
	if summaryText == "" {
		summaryText = result.Record.Summary
	}
	return true, a.persistVerification(store, run, result.Record, summaryText)
}

func (a *App) persistVerification(store *ledger.Store, run *ledger.Run, rec adaptive.VerificationRecord, summaryText string) error {
	now := a.Clock()
	if summaryText != "" && rec.FailureSummaryPath != "" {
		if err := store.WriteArtifact(rec.FailureSummaryPath, []byte(summaryText)); err != nil {
			return app.Failf("write verification summary: %v", err)
		}
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return app.Failf("marshal verification receipts: %v", err)
	}
	if err := store.WriteArtifact(adaptive.ArtifactVerification, raw); err != nil {
		return app.Failf("write verification receipts: %v", err)
	}

	st := *run.Adaptive
	if err := adaptive.ApplyVerificationResult(&st, rec, now); err != nil {
		return app.Failf("apply verification: %v", err)
	}
	run.Adaptive = &st
	run.Verification = &rec
	run.UpdatedAt = now.UTC().Format("2006-01-02T15:04:05Z")
	if err := store.WriteRun(*run); err != nil {
		return app.Failf("store verification: %v", err)
	}
	_ = store.AppendDecision(ledger.Decision{
		RunID: run.RunID, Kind: "supervisor-verification", Stage: adaptive.Verifying,
		Choice: rec.Status, Outcome: st.Stage, Detail: rec.Summary,
		Next: map[bool]string{true: "assign assessor", false: "return to implementer with bounded failure summary"}[st.Stage == adaptive.Assessing],
	})
	if st.Stage == adaptive.Assessing || st.Stage == adaptive.Done {
		if err := a.sdlcAdvanceStageFlowAfterVerification(store, run, &st); err != nil {
			return err
		}
		st = *run.Adaptive
	}
	if st.Stage != adaptive.Assessing && st.Stage != adaptive.Done {
		a.Outf("run %s: verification %s — %s\n", run.RunID, rec.Status, truncateForOut(rec.Summary, 200))
		if rec.FailureSummaryPath != "" {
			a.Outf("  failure summary: %s\n", filepath.Join(store.Dir, "artifacts", filepath.FromSlash(rec.FailureSummaryPath)))
		}
		if st.Stage == adaptive.Paused {
			return app.Failf("run %s paused after verification %s: %s", run.RunID, rec.Status, st.PendingReason)
		}
		// A failed check within budget is the normal revision loop, not a driver
		// error: returning one here would let sdlcDrive's pause-on-error handler
		// overwrite the implementing stage with implementer-failed.
		return nil
	}
	a.Outf("run %s: verification passed for worktree %s\n", run.RunID, shortHex(rec.WorktreeIdentity))
	return nil
}

func truncateForOut(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func shortHex(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(none)"
	}
	return s
}

// sdlcVerificationFailureNote tells a retried implementer why the supervisor
// rejected its previous revision. Without it the implementer would only see
// the original task and could resubmit the same candidate.
func sdlcVerificationFailureNote(store *ledger.Store, run ledger.Run) string {
	rec := run.Verification
	if rec == nil || rec.AllPassed || run.Adaptive == nil || rec.CandidateFingerprint == "" ||
		rec.CandidateFingerprint != run.Adaptive.DiffRevision {
		return ""
	}
	summary := rec.Summary
	if rec.FailureSummaryPath != "" {
		if raw, err := store.ReadArtifact(rec.FailureSummaryPath); err == nil && len(raw) > 0 {
			summary = string(raw)
		}
	}
	logDir := filepath.Join(store.Dir, filepath.FromSlash(adaptive.VerificationLogDirRelative))
	summary = worker.BoundVerificationSummary(summary, logDir, worker.MaxVerificationSummaryBytes)
	return "Supervisor verification of your previous revision failed. Fix these failures before reporting changed again; the supervisor re-runs the same checks:\n" +
		summary + "\nFull check output: " + logDir
}
