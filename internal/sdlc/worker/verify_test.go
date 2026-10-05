package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/security"
)

func TestRunVerificationRequiresAuthorization(t *testing.T) {
	dir := t.TempDir()
	_, err := RunVerification(context.Background(), VerifyRequest{
		Checks:           []adaptive.Check{{ID: "c1", Argv: []string{"true"}}},
		ChecksDigest:     "planned",
		AuthorizedDigest: "",
		Workspace:        dir,
		CaptureIdentity:  func(context.Context, string) (string, error) { return "tree-a", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected authorization error, got %v", err)
	}
}

func TestRunVerificationRecordsWorktreeIdentityAndLogs(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs", "verification")
	var captured []string
	result, err := RunVerification(context.Background(), VerifyRequest{
		Checks: []adaptive.Check{
			{ID: "unit", Argv: []string{"true"}},
			{ID: "manual", Manual: "eyeball the UI"},
		},
		ChecksDigest:         "digest-1",
		AuthorizedDigest:     "digest-1",
		PlanDigest:           "plan-1",
		CandidateFingerprint: "patch-hash",
		Workspace:            dir,
		LogDir:               logDir,
		OutputPathPrefix:     "logs/verification",
		Now:                  time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC),
		CaptureIdentity: func(_ context.Context, _ string) (string, error) {
			captured = append(captured, "tree-live")
			return "tree-live", nil
		},
		RunArgv: func(_ context.Context, req security.ArgvRequest) (security.ArgvResult, error) {
			if req.ChecksDigest != "digest-1" || req.AuthorizedDigest != "digest-1" {
				t.Fatalf("unexpected digests: %+v", req)
			}
			return security.ArgvResult{ExitCode: 0, Stdout: []byte("ok\n"), CommandLine: "true", Duration: time.Millisecond}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AllPassed || result.Record.WorktreeIdentity != "tree-live" {
		t.Fatalf("unexpected result: %+v", result.Record)
	}
	if len(result.Record.Receipts) != 2 {
		t.Fatalf("want 2 receipts, got %d", len(result.Record.Receipts))
	}
	argvRec := result.Record.Receipts[0]
	if argvRec.WorktreeIdentity != "tree-live" || argvRec.CandidateFingerprint != "patch-hash" {
		t.Fatalf("receipt must bind worktree identity, not only patch hash: %+v", argvRec)
	}
	if argvRec.CommandIdentity == "" || argvRec.PlanDigest != "plan-1" || argvRec.OutputPath == "" || argvRec.OutputDigest == "" {
		t.Fatalf("receipt missing identity fields: %+v", argvRec)
	}
	if argvRec.OutputPath != "logs/verification/unit.log" {
		t.Fatalf("output path: %s", argvRec.OutputPath)
	}
	if _, err := os.Stat(filepath.Join(logDir, "unit.log")); err != nil {
		t.Fatalf("expected verification log on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "unit.json")); err != nil {
		t.Fatalf("expected verification meta on disk: %v", err)
	}
	if len(captured) < 1 {
		t.Fatal("expected worktree capture")
	}
}

func TestRunVerificationFailureSummaryIsBoundedWithLogPath(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "vlogs")
	result, err := RunVerification(context.Background(), VerifyRequest{
		Checks:           []adaptive.Check{{ID: "fail", Argv: []string{"false"}}},
		ChecksDigest:     "d",
		AuthorizedDigest: "d",
		Workspace:        dir,
		LogDir:           logDir,
		CaptureIdentity:  func(context.Context, string) (string, error) { return "t1", nil },
		RunArgv: func(context.Context, security.ArgvRequest) (security.ArgvResult, error) {
			return security.ArgvResult{ExitCode: 1, Stderr: []byte(strings.Repeat("e", 8*1024)), CommandLine: "false"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AllPassed || result.Record.Status != adaptive.VerificationStatusFailed {
		t.Fatalf("expected failure: %+v", result.Record)
	}
	if result.Record.FailureSummaryPath != adaptive.ArtifactVerificationSummary {
		t.Fatalf("summary path: %s", result.Record.FailureSummaryPath)
	}
	if !strings.Contains(result.FailureSummary, "fail.log") {
		t.Fatalf("failure summary must mention log path: %q", result.FailureSummary)
	}
}

func TestRunVerificationInvalidatesWhenCommandMutatesCandidate(t *testing.T) {
	dir := t.TempDir()
	trees := []string{"tree-before", "tree-after"}
	idx := 0
	result, err := RunVerification(context.Background(), VerifyRequest{
		Checks:           []adaptive.Check{{ID: "mutate", Argv: []string{"sh", "-c", "touch x"}}},
		ChecksDigest:     "d",
		AuthorizedDigest: "d",
		Workspace:        dir,
		CaptureIdentity: func(context.Context, string) (string, error) {
			if idx >= len(trees) {
				return trees[len(trees)-1], nil
			}
			v := trees[idx]
			idx++
			return v, nil
		},
		RunArgv: func(context.Context, security.ArgvRequest) (security.ArgvResult, error) {
			return security.ArgvResult{ExitCode: 0, CommandLine: "touch"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Invalidated || result.AllPassed {
		t.Fatalf("expected invalidation: %+v", result.Record)
	}
	if result.Record.Status != adaptive.VerificationStatusInvalidated {
		t.Fatalf("status: %s", result.Record.Status)
	}
	for _, r := range result.Record.Receipts {
		if !r.Invalidated || r.Passed {
			t.Fatalf("every receipt must be invalidated: %+v", r)
		}
	}
}

func TestReceiptsMatchCandidateRequiresWorktreeIdentity(t *testing.T) {
	recs := []adaptive.CheckReceipt{{
		CheckID: "c", Passed: true, WorktreeIdentity: "wt-1", CandidateFingerprint: "patch-1",
	}}
	if adaptive.ReceiptsMatchCandidate(recs, "", "patch-1") {
		t.Fatal("empty worktree must not match")
	}
	if !adaptive.ReceiptsMatchCandidate(recs, "wt-1", "patch-1") {
		t.Fatal("exact worktree should match")
	}
	if adaptive.ReceiptsMatchCandidate(recs, "wt-2", "patch-1") {
		t.Fatal("different worktree must not match")
	}
}

func TestApplyVerificationResultReturnsToImplementingOnFailure(t *testing.T) {
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = adaptive.Verifying
	st.DiffRevision = "cand"
	st.RevisionCount = 1
	rec := adaptive.VerificationRecord{
		Status: adaptive.VerificationStatusFailed, AllPassed: false,
		Receipts: []adaptive.CheckReceipt{{CheckID: "c", Passed: false, WorktreeIdentity: "wt"}},
		Summary:  "boom", FailureSummaryPath: adaptive.ArtifactVerificationSummary,
	}
	if err := adaptive.ApplyVerificationResult(&st, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Stage != adaptive.Implementing {
		t.Fatalf("stage: %s", st.Stage)
	}
	if !strings.Contains(st.PendingReason, adaptive.ArtifactVerificationSummary) {
		t.Fatalf("pending reason should cite summary path: %q", st.PendingReason)
	}
	if len(st.CheckReceipts) != 1 {
		t.Fatalf("receipts: %d", len(st.CheckReceipts))
	}
}
