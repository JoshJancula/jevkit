package adaptive

import (
	"fmt"
	"strings"
	"time"
)

// Artifact paths for supervisor-owned verification under the run.
const (
	ArtifactVerification        = "verification/receipts.json"
	ArtifactVerificationSummary = "verification/failure-summary.txt"
	VerificationLogDirRelative  = "logs/verification"
)

// Verification outcome statuses persisted on the supervisor-owned record.
const (
	VerificationStatusPassed       = "passed"
	VerificationStatusFailed       = "failed"
	VerificationStatusInvalidated  = "invalidated"
	VerificationStatusSkipped      = "skipped"
	VerificationStatusUnauthorized = "unauthorized"
)

// OutcomeVerificationEnvironment pauses a run whose checks could not execute
// in the supervisor environment. Retrying returns to verifying, not to the
// implementer.
const OutcomeVerificationEnvironment = "verification-environment-failed"

// CheckReceipt is a supervisor verification receipt bound to one candidate.
// It records the actual worktree identity (tree hash), not only patch.diff's
// report hash. Any candidate change must clear Adaptive.CheckReceipts.
type CheckReceipt struct {
	CheckID              string   `json:"checkId"`
	CommandIdentity      string   `json:"commandIdentity,omitempty"`
	Argv                 []string `json:"argv,omitempty"`
	Manual               string   `json:"manual,omitempty"`
	PlanDigest           string   `json:"planDigest,omitempty"`
	ChecksDigest         string   `json:"checksDigest,omitempty"`
	CandidateFingerprint string   `json:"candidateFingerprint,omitempty"`
	WorktreeIdentity     string   `json:"worktreeIdentity,omitempty"`
	At                   string   `json:"at,omitempty"`
	ExitCode             int      `json:"exitCode,omitempty"`
	TimedOut             bool     `json:"timedOut,omitempty"`
	OutputPath           string   `json:"outputPath,omitempty"`
	OutputDigest         string   `json:"outputDigest,omitempty"`
	Passed               bool     `json:"passed"`
	Invalidated          bool     `json:"invalidated,omitempty"`
	DurationMS           int64    `json:"durationMs,omitempty"`
	Detail               string   `json:"detail,omitempty"`
}

// VerificationRecord is the durable supervisor-owned verification step for one
// candidate. Stored with the parent run so users can inspect or delete it
// together; diagnostic logs under logs/verification are separately prunable.
type VerificationRecord struct {
	Status               string         `json:"status"`
	PlanDigest           string         `json:"planDigest,omitempty"`
	ChecksDigest         string         `json:"checksDigest,omitempty"`
	CandidateFingerprint string         `json:"candidateFingerprint,omitempty"`
	WorktreeIdentity     string         `json:"worktreeIdentity,omitempty"`
	Receipts             []CheckReceipt `json:"receipts,omitempty"`
	FailureSummaryPath   string         `json:"failureSummaryPath,omitempty"`
	AllPassed            bool           `json:"allPassed,omitempty"`
	Invalidated          bool           `json:"invalidated,omitempty"`
	At                   string         `json:"at,omitempty"`
	Summary              string         `json:"summary,omitempty"`
	// EnvironmentFailure is set when every failed check failed for a reason the
	// implementer cannot fix from the candidate (missing command, wrong
	// toolchain on the supervisor's PATH). Such runs escalate to the operator
	// instead of spending a revision.
	EnvironmentFailure string `json:"environmentFailure,omitempty"`
}

// ReceiptsMatchCandidate reports whether existing receipts are still valid for
// the exact candidate worktree identity (and optional patch fingerprint).
func ReceiptsMatchCandidate(receipts []CheckReceipt, worktreeIdentity, candidateFingerprint string) bool {
	worktreeIdentity = strings.TrimSpace(worktreeIdentity)
	if worktreeIdentity == "" || len(receipts) == 0 {
		return false
	}
	candidateFingerprint = strings.TrimSpace(candidateFingerprint)
	for _, r := range receipts {
		if r.Invalidated || !r.Passed {
			return false
		}
		if strings.TrimSpace(r.WorktreeIdentity) != worktreeIdentity {
			return false
		}
		if candidateFingerprint != "" && strings.TrimSpace(r.CandidateFingerprint) != "" &&
			strings.TrimSpace(r.CandidateFingerprint) != candidateFingerprint {
			return false
		}
	}
	return true
}

// ChecksAllowCompletion reports whether supervisor receipts authorize Done for
// this candidate. Failed, timed-out, invalidated, or wrong-revision receipts
// block completion: Jev classification and agent prose cannot override them.
// Empty receipts are allowed when verification recorded a no-argv skip.
func ChecksAllowCompletion(receipts []CheckReceipt, candidateFingerprint string) bool {
	candidateFingerprint = strings.TrimSpace(candidateFingerprint)
	for _, r := range receipts {
		if r.Invalidated || !r.Passed || r.TimedOut {
			return false
		}
		if candidateFingerprint != "" && strings.TrimSpace(r.CandidateFingerprint) != "" &&
			strings.TrimSpace(r.CandidateFingerprint) != candidateFingerprint {
			return false
		}
	}
	return true
}

// FormatReceiptsForAssessor builds a bounded, exact-revision receipt digest for
// assessor prompts. It never invents a pass from prose.
func FormatReceiptsForAssessor(receipts []CheckReceipt, candidateFingerprint string) string {
	if len(receipts) == 0 {
		return "Supervisor verification: no argv receipts (manual/skip only) for revision " + strings.TrimSpace(candidateFingerprint)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Supervisor verification receipts for exact revision %s:\n", strings.TrimSpace(candidateFingerprint))
	for _, r := range receipts {
		status := "PASS"
		if r.Invalidated {
			status = "INVALIDATED"
		} else if r.TimedOut {
			status = "TIMED-OUT"
		} else if !r.Passed {
			status = "FAIL"
		}
		line := fmt.Sprintf("- %s: %s", r.CheckID, status)
		if r.Detail != "" {
			line += " (" + BoundPendingDetail(r.Detail, 120) + ")"
		}
		if r.OutputPath != "" {
			line += " log=" + r.OutputPath
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("These supervisor receipts are authoritative; do not treat a failed command as passed.")
	return strings.TrimRight(b.String(), "\n")
}

// BoundPendingDetail truncates a detail string for prompt injection.
func BoundPendingDetail(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ApplyVerificationResult stores receipts on adaptive state during the verifying
// stage. On failure, timeout, or invalidation it returns to implementation when
// revision/run budgets remain; exhaustion pauses with a recovery message.
// Assessors are assigned only after a successful verification for the same
// candidate (stage becomes assessing).
func ApplyVerificationResult(st *State, rec VerificationRecord, now time.Time) error {
	if st == nil {
		return fmt.Errorf("adaptive: nil state")
	}
	if st.Stage != Verifying {
		return fmt.Errorf("adaptive: verify from verifying, not %s", st.Stage)
	}
	st.CheckReceipts = append([]CheckReceipt(nil), rec.Receipts...)
	failed := rec.Invalidated || !rec.AllPassed || !ChecksAllowCompletion(rec.Receipts, st.DiffRevision)
	if failed {
		if rec.Invalidated {
			InvalidateOnCandidateChange(st)
			st.CheckReceipts = append([]CheckReceipt(nil), rec.Receipts...)
		}
		reason := boundPending(rec.Summary, rec.FailureSummaryPath)
		st.Assignments = map[string]Assignment{}
		st.Outcome = ""
		if env := strings.TrimSpace(rec.EnvironmentFailure); env != "" && !rec.Invalidated {
			st.Pause(OutcomeVerificationEnvironment)
			st.PendingReason = recoveryMessage(env, "the checks could not run in the supervisor environment, so no revision was spent; fix the environment, then retry verification")
			return nil
		}
		if st.BudgetExhausted() {
			st.Pause("assignment-budget-exhausted")
			st.PendingReason = recoveryMessage(reason, "assignment/cost budget exhausted; raise budgets or start a new run after inspecting the failure summary")
			return nil
		}
		if st.MaxRevisions > 0 && st.RevisionCount >= st.MaxRevisions {
			st.Pause("revision-budget-exhausted")
			st.PendingReason = recoveryMessage(reason, "revision budget exhausted; inspect the failure summary and start a new run or raise maxRevisions")
			return nil
		}
		st.Stage = Implementing
		st.PendingReason = reason
		_ = now
		return nil
	}
	st.Stage = Assessing
	st.PendingReason = ""
	st.Outcome = ""
	_ = now
	return nil
}

func recoveryMessage(failure, hint string) string {
	failure = strings.TrimSpace(failure)
	hint = strings.TrimSpace(hint)
	if failure == "" {
		return hint
	}
	if hint == "" {
		return failure
	}
	return failure + "; " + hint
}

func boundPending(summary, path string) string {
	summary = strings.TrimSpace(summary)
	path = strings.TrimSpace(path)
	if path == "" {
		return summary
	}
	if summary == "" {
		return "verification failed; inspect " + path
	}
	if len(summary) > 512 {
		summary = summary[:512] + "…"
	}
	return summary + " (full summary: " + path + ")"
}
