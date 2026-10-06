package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/security"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
)

// MaxVerificationLogBytes caps each check's stored stdout+stderr locally.
const MaxVerificationLogBytes = 256 * 1024

// VerifyRequest runs authorized planner checks against one exact candidate.
type VerifyRequest struct {
	Checks               []adaptive.Check
	ChecksDigest         string
	AuthorizedDigest     string
	PlanDigest           string
	CandidateFingerprint string // patch.diff / report hash (secondary identity)
	Workspace            string
	AllowRead            []string
	// LogDir is the absolute path for diagnostic streams (typically
	// <run>/logs/verification). Receipts themselves are written by the caller.
	LogDir string
	// OutputPathPrefix is the path prefix stored on receipts (relative to the
	// run root), e.g. "logs/verification".
	OutputPathPrefix string
	Now              time.Time
	MaxOutputBytes   int
	Security         *securityconfig.Config
	// RunArgv is optional; tests inject fakes. Default uses security.RunArgv.
	RunArgv func(context.Context, security.ArgvRequest) (security.ArgvResult, error)
	// CaptureIdentity is optional; tests inject fakes. Default captures the
	// private-index worktree tree hash.
	CaptureIdentity func(context.Context, string) (string, error)
}

// VerifyResult is the supervisor-owned outcome: full receipts locally, and a
// bounded failure summary (plus log paths) suitable for the implementer.
type VerifyResult struct {
	Record         adaptive.VerificationRecord
	FailureSummary string
	AllPassed      bool
	Invalidated    bool
}

// CaptureWorktreeIdentity returns the private-index tree hash of workspace.
// This is the candidate's actual worktree identity, distinct from patch.diff's
// bounded report hash. When the workspace is not a Git worktree, a stable
// nogit fingerprint of the resolved path is returned so verification can still
// bind receipts (mutation detection is unavailable without Git).
func CaptureWorktreeIdentity(ctx context.Context, workspace string) (string, error) {
	snap, err := newWorkspaceSnapshot(ctx, workspace)
	if err != nil {
		return nogitWorktreeIdentity(workspace), nil
	}
	defer snap.close()
	id, err := snap.capture(ctx)
	if err != nil {
		return nogitWorktreeIdentity(workspace), nil
	}
	return id, nil
}

func nogitWorktreeIdentity(workspace string) string {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		abs = workspace
	}
	sum := sha256.Sum256([]byte("nogit\x00" + abs))
	return "nogit:" + fmt.Sprintf("%x", sum[:16])
}

// RunVerification executes authorized argv checks against the exact candidate
// worktree. Manual checks are recorded but not executed. If a command mutates
// the worktree, the result is invalidated and prior receipts for this pass are
// marked failed. Bounded output stays under LogDir; only FailureSummary is
// meant for the next implementer prompt.
func RunVerification(ctx context.Context, req VerifyRequest) (VerifyResult, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	at := now.UTC().Format(time.RFC3339)
	prefix := strings.TrimSpace(req.OutputPathPrefix)
	if prefix == "" {
		prefix = adaptive.VerificationLogDirRelative
	}
	capture := req.CaptureIdentity
	if capture == nil {
		capture = CaptureWorktreeIdentity
	}
	runArgv := req.RunArgv
	if runArgv == nil {
		runArgv = security.RunArgv
	}

	rec := adaptive.VerificationRecord{
		PlanDigest:           strings.TrimSpace(req.PlanDigest),
		ChecksDigest:         strings.TrimSpace(req.ChecksDigest),
		CandidateFingerprint: strings.TrimSpace(req.CandidateFingerprint),
		At:                   at,
	}

	if !adaptive.HasArgvChecks(req.Checks) {
		for _, c := range req.Checks {
			rec.Receipts = append(rec.Receipts, manualReceipt(c, req, "", at))
		}
		rec.Status = adaptive.VerificationStatusSkipped
		rec.AllPassed = true
		rec.Summary = "no argv checks to run"
		return VerifyResult{Record: rec, AllPassed: true}, nil
	}

	if err := security.RequireCommandAuthorization(req.ChecksDigest, req.AuthorizedDigest); err != nil {
		rec.Status = adaptive.VerificationStatusUnauthorized
		rec.Summary = err.Error()
		return VerifyResult{
			Record:         rec,
			FailureSummary: BoundVerificationSummary(err.Error(), "", MaxVerificationSummaryBytes),
		}, err
	}
	if strings.TrimSpace(req.Workspace) == "" {
		return VerifyResult{}, fmt.Errorf("worker: verification workspace is required")
	}

	worktreeID, err := capture(ctx, req.Workspace)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("worker: capture worktree identity: %w", err)
	}
	rec.WorktreeIdentity = worktreeID

	if req.LogDir != "" {
		if err := os.MkdirAll(req.LogDir, 0o700); err != nil {
			return VerifyResult{}, fmt.Errorf("worker: create verification log dir: %w", err)
		}
	}

	maxOut := req.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = MaxVerificationLogBytes
	}

	allPassed := true
	var failBits, envBits []string
	candidateFailures := 0
	for _, c := range req.Checks {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		if strings.TrimSpace(c.Manual) != "" {
			rec.Receipts = append(rec.Receipts, manualReceipt(c, req, worktreeID, at))
			continue
		}
		if len(c.Argv) == 0 {
			continue
		}

		before := worktreeID
		if len(rec.Receipts) > 0 {
			before, err = capture(ctx, req.Workspace)
			if err != nil {
				return VerifyResult{}, fmt.Errorf("worker: capture worktree before %q: %w", id, err)
			}
			if before != worktreeID {
				rec.Invalidated = true
				allPassed = false
				rec.Status = adaptive.VerificationStatusInvalidated
				rec.Summary = "candidate changed before check " + id
				break
			}
		}

		timeout := time.Duration(c.TimeoutSeconds) * time.Second
		result, runErr := runArgv(ctx, security.ArgvRequest{
			Argv:             append([]string(nil), c.Argv...),
			WorkingDir:       c.WorkingDir,
			Timeout:          timeout,
			Workspace:        req.Workspace,
			AllowRead:        append([]string(nil), req.AllowRead...),
			MaxOutputBytes:   maxOut,
			ChecksDigest:     req.ChecksDigest,
			AuthorizedDigest: req.AuthorizedDigest,
			Security:         req.Security,
		})

		outPath, outDigest, writeErr := writeVerificationLog(req.LogDir, prefix, id, c.Argv, result, at, req)
		if writeErr != nil {
			return VerifyResult{}, writeErr
		}

		receipt := adaptive.CheckReceipt{
			CheckID:              id,
			CommandIdentity:      commandIdentity(c.Argv),
			Argv:                 append([]string(nil), c.Argv...),
			PlanDigest:           rec.PlanDigest,
			ChecksDigest:         rec.ChecksDigest,
			CandidateFingerprint: rec.CandidateFingerprint,
			WorktreeIdentity:     worktreeID,
			At:                   at,
			ExitCode:             result.ExitCode,
			TimedOut:             result.TimedOut,
			OutputPath:           outPath,
			OutputDigest:         outDigest,
			DurationMS:           result.Duration.Milliseconds(),
		}
		switch {
		case runErr != nil && !result.TimedOut && result.ExitCode == 0:
			receipt.Passed = false
			receipt.Detail = runErr.Error()
			allPassed = false
			failBits = append(failBits, fmt.Sprintf("%s: %s (log: %s)", id, runErr.Error(), outPath))
		case result.TimedOut || result.ExitCode != 0:
			receipt.Passed = false
			if result.TimedOut {
				receipt.Detail = "timed out"
			} else {
				receipt.Detail = fmt.Sprintf("exit %d", result.ExitCode)
			}
			allPassed = false
			failBits = append(failBits, fmt.Sprintf("%s: %s (log: %s)", id, receipt.Detail, outPath))
		default:
			receipt.Passed = true
		}
		if !receipt.Passed {
			if env := classifyEnvironmentFailure(c.Argv, result, runErr); env != "" {
				envBits = append(envBits, env)
			} else {
				candidateFailures++
			}
		}

		after, capErr := capture(ctx, req.Workspace)
		if capErr != nil {
			return VerifyResult{}, fmt.Errorf("worker: capture worktree after %q: %w", id, capErr)
		}
		if after != before {
			receipt.Passed = false
			receipt.Invalidated = true
			receipt.Detail = "verification command changed the candidate worktree"
			rec.Invalidated = true
			allPassed = false
			failBits = append(failBits, fmt.Sprintf("%s: changed candidate worktree (log: %s)", id, outPath))
			rec.Receipts = append(rec.Receipts, receipt)
			for i := range rec.Receipts {
				rec.Receipts[i].Invalidated = true
				rec.Receipts[i].Passed = false
			}
			rec.Status = adaptive.VerificationStatusInvalidated
			rec.Summary = "verification command changed the candidate; receipts invalidated"
			break
		}
		rec.Receipts = append(rec.Receipts, receipt)
	}

	rec.AllPassed = allPassed && !rec.Invalidated
	if rec.Status == "" {
		if rec.AllPassed {
			rec.Status = adaptive.VerificationStatusPassed
			rec.Summary = "all authorized checks passed"
		} else {
			rec.Status = adaptive.VerificationStatusFailed
			rec.Summary = strings.Join(failBits, "; ")
			if rec.Summary == "" {
				rec.Summary = "one or more checks failed"
			}
		}
	}

	if !rec.AllPassed && !rec.Invalidated && candidateFailures == 0 && len(envBits) > 0 {
		rec.EnvironmentFailure = strings.Join(envBits, "; ")
	}

	summary := rec.Summary
	summaryPath := ""
	if !rec.AllPassed {
		summaryPath = adaptive.ArtifactVerificationSummary
		body := rec.Summary
		if len(failBits) > 0 {
			body = rec.Summary + "\n" + strings.Join(failBits, "\n")
		}
		summary = BoundVerificationSummary(body, summaryPath, MaxVerificationSummaryBytes)
	}
	rec.FailureSummaryPath = summaryPath
	return VerifyResult{
		Record:         rec,
		FailureSummary: summary,
		AllPassed:      rec.AllPassed,
		Invalidated:    rec.Invalidated,
	}, nil
}

func manualReceipt(c adaptive.Check, req VerifyRequest, worktreeID, at string) adaptive.CheckReceipt {
	return adaptive.CheckReceipt{
		CheckID:              strings.TrimSpace(c.ID),
		Manual:               strings.TrimSpace(c.Manual),
		PlanDigest:           strings.TrimSpace(req.PlanDigest),
		ChecksDigest:         strings.TrimSpace(req.ChecksDigest),
		CandidateFingerprint: strings.TrimSpace(req.CandidateFingerprint),
		WorktreeIdentity:     worktreeID,
		At:                   at,
		Passed:               true,
		Detail:               "manual check; not executed by supervisor",
	}
}

func commandIdentity(argv []string) string {
	raw, err := json.Marshal(argv)
	if err != nil {
		sum := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
		return fmt.Sprintf("%x", sum[:])
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

func writeVerificationLog(logDir, prefix, checkID string, argv []string, result security.ArgvResult, at string, req VerifyRequest) (relPath, digest string, err error) {
	relPath = filepath.ToSlash(filepath.Join(prefix, checkID+".log"))
	payload := append(append([]byte{}, result.Stdout...), result.Stderr...)
	sum := sha256.Sum256(payload)
	digest = fmt.Sprintf("%x", sum[:])
	if logDir == "" {
		return relPath, digest, nil
	}
	safe := filepath.Base(checkID)
	if safe != checkID || strings.ContainsAny(checkID, `/\\`) {
		return "", "", fmt.Errorf("worker: invalid check id %q", checkID)
	}
	meta := struct {
		CheckID              string   `json:"checkId"`
		Argv                 []string `json:"argv,omitempty"`
		At                   string   `json:"at"`
		Command              string   `json:"commandLine,omitempty"`
		ExitCode             int      `json:"exitCode"`
		TimedOut             bool     `json:"timedOut,omitempty"`
		PlanDigest           string   `json:"planDigest,omitempty"`
		ChecksDigest         string   `json:"checksDigest,omitempty"`
		CandidateFingerprint string   `json:"candidateFingerprint,omitempty"`
	}{
		CheckID: checkID, Argv: append([]string(nil), argv...), At: at,
		Command: result.CommandLine, ExitCode: result.ExitCode, TimedOut: result.TimedOut,
		PlanDigest: req.PlanDigest, ChecksDigest: req.ChecksDigest,
		CandidateFingerprint: req.CandidateFingerprint,
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(logDir, safe+".json"), metaBytes, 0o600); err != nil {
		return "", "", fmt.Errorf("worker: write verification meta: %w", err)
	}
	var body strings.Builder
	body.WriteString("=== stdout ===\n")
	body.Write(result.Stdout)
	body.WriteString("\n=== stderr ===\n")
	body.Write(result.Stderr)
	if result.Truncated {
		body.WriteString("\n[output truncated by check runner]\n")
	}
	data := []byte(body.String())
	if err := os.WriteFile(filepath.Join(logDir, safe+".log"), data, 0o600); err != nil {
		return "", "", fmt.Errorf("worker: write verification log: %w", err)
	}
	sum = sha256.Sum256(data)
	return relPath, fmt.Sprintf("%x", sum[:]), nil
}
