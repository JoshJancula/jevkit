package sdlc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

type hardFailureExecutor struct{}

func (hardFailureExecutor) Execute(context.Context, worker.Request) (worker.Reply, error) {
	return worker.Reply{}, errors.New("workspace inspection required")
}

func startReviewRun(t *testing.T, a *App, executor *fakeSDLCExecutor) string {
	t.Helper()
	stageTestRoster(t, a)
	a.SdlcExecutor = executor
	code, out, errText := run(a, "", "sdlc", "start", "feature", "--task", "edit a file", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s", code, errText)
	}
	id := strings.Fields(out)[1]
	// plan → implement → verify, leaving the run in assessing for the reviewer.
	for i := 0; i < 4; i++ {
		stored, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
		if err != nil {
			t.Fatal(err)
		}
		if stored.Adaptive.Stage == adaptive.Assessing || stored.Adaptive.Stage == adaptive.Done || stored.Adaptive.Stage == adaptive.Paused {
			break
		}
		if code, _, errors := run(a, "", "sdlc", "drive", id); code != app.ExitOK {
			t.Fatalf("drive %d: %d %s", i, code, errors)
		}
	}
	stored, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Assessing {
		t.Fatalf("startReviewRun want assessing, got %+v %v", stored.Adaptive, err)
	}
	return id
}

func TestReviewWorkspaceDriftUsesChangesRequiredAndPassesPaths(t *testing.T) {
	a := newApp(t)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan"},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"},
		{Outcome: "changes-required", Content: "Fix the unrelated change", WorkspaceDrift: []string{"cmd/jevkit/usage.go"}},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+fix\n"},
	}}
	id := startReviewRun(t, a, executor)
	if code, _, errors := run(a, "", "sdlc", "drive", id); code != app.ExitOK {
		t.Fatalf("review: %d %s", code, errors)
	}
	stored, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Implementing || stored.ReviewRecovery == nil || !stored.ReviewRecovery.Applied || len(stored.ReviewRecovery.Paths) != 1 {
		t.Fatalf("review state: %+v %v", stored, err)
	}
	if code, _, errors := run(a, "", "sdlc", "drive", id); code != app.ExitOK {
		t.Fatalf("implement: %d %s", code, errors)
	}
	if task := executor.requests[3].Task; !strings.Contains(task, "Fix the unrelated change") || !strings.Contains(task, "cmd/jevkit/usage.go") || !strings.Contains(task, "editor unknown") {
		t.Fatalf("missing review handoff: %q", task)
	}
}

func TestApprovedReviewWithDriftPausesThenReassesses(t *testing.T) {
	a := newApp(t)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan"},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"},
		{Outcome: "approved", Content: "Looks good", WorkspaceDrift: []string{"README.md"}},
		{Outcome: "approved", Content: "Rechecked current workspace"},
	}}
	id := startReviewRun(t, a, executor)
	if code, _, errors := run(a, "", "sdlc", "drive", id); code != app.ExitOK {
		t.Fatalf("review: %d %s", code, errors)
	}
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if stored.Adaptive.Stage != adaptive.Paused || stored.Adaptive.Outcome != "review-workspace-drift" || stored.ReviewRecovery.Applied {
		t.Fatalf("approval drift did not pause: %+v", stored.Adaptive)
	}
	if code, _, errors := run(a, "", "sdlc", "resume", id, "--retry-failed", "--step"); code != app.ExitOK {
		t.Fatalf("reassess: %d %s", code, errors)
	}
	stored, _ = ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if stored.Adaptive.Stage != adaptive.Done || len(executor.requests) != 4 || !strings.Contains(executor.requests[3].Task, "README.md") {
		t.Fatalf("reassessment: %+v requests=%d", stored.Adaptive, len(executor.requests))
	}
}

func TestLegacyReviewStreamRecoversOnceWithVerifiedBinding(t *testing.T) {
	a := newApp(t)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan"},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"},
	}}
	id := startReviewRun(t, a, executor)
	store := ledger.Open(a.SDLCRunsDir(), id)
	invocation := "run-20260925T154556Z-b0a3accc"
	binding := "runtime:opencode:a::"
	_ = store.AppendDecision(ledger.Decision{RunID: id, Kind: "session-strategy", Stage: adaptive.Assessing, Invocation: invocation, Runtime: "opencode", Trigger: binding + "/assessor"})
	_ = store.AppendDecision(ledger.Decision{RunID: id, Kind: "invocation-outcome", Stage: adaptive.Assessing, Invocation: invocation, Runtime: "opencode", Trigger: "assessor", Choice: "failed", Detail: "worker: non-implementer changed the workspace"})
	logDir := filepath.Join(store.Dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(worker.LogMeta{Invocation: invocation, Agent: "assessor", Runtime: "opencode"})
	if err := os.WriteFile(filepath.Join(logDir, invocation+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	stream := "{\"type\":\"text\",\"part\":{\"text\":\"{\\\"outcome\\\":\\\"changes-required\\\",\\\"content\\\":\\\"Fix this\\\"}\"}}\n" +
		"{\"type\":\"step_finish\",\"part\":{\"tokens\":{\"input\":31,\"output\":5}}}\n"
	if err := os.WriteFile(filepath.Join(logDir, invocation+".stdout"), []byte(stream), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverReview(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverReview(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.ReadRun()
	if stored.Adaptive.Stage != adaptive.Implementing || len(stored.Adaptive.Assessments) != 1 || len(stored.Usage) != 3 {
		t.Fatalf("legacy recovery: %+v usage=%d", stored.Adaptive, len(stored.Usage))
	}
	if stored.ReviewRecovery == nil || !stored.ReviewRecovery.Applied || stored.ReviewRecovery.Revision != stored.Adaptive.DiffRevision {
		t.Fatalf("recovery record: %+v", stored.ReviewRecovery)
	}
	patch, _ := store.ReadArtifact("patch.diff")
	if got := stored.ReviewRecovery.Revision; got != fmt.Sprintf("%x", sha256.Sum256(patch)) {
		t.Fatalf("digest %s", got)
	}
}

func TestInvalidSavedReviewPausesAndReassessesNewPatchRevision(t *testing.T) {
	a := newApp(t)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan"},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"},
		{Outcome: "approved"},
	}}
	id := startReviewRun(t, a, executor)
	store := ledger.Open(a.SDLCRunsDir(), id)
	stored, _ := store.ReadRun()
	oldRevision := stored.Adaptive.DiffRevision
	stored.ReviewRecovery = &ledger.ReviewRecovery{Invocation: "run-20260925T154556Z-b0a3accc", Agent: "assessor",
		Binding: "runtime:opencode:a::", Runtime: "opencode", Revision: oldRevision, Outcome: "changes-required", Content: "old review"}
	if err := store.WriteRun(stored); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("patch.diff", []byte("diff --git a/a b/a\n+newer\n")); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverReview(context.Background(), id); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("expected digest error: %v", err)
	}
	stored, _ = store.ReadRun()
	if stored.Adaptive.Stage != adaptive.Paused || stored.Adaptive.Outcome != "review-recovery-invalid" {
		t.Fatalf("invalid review state: %+v", stored.Adaptive)
	}
	if code, _, errors := run(a, "", "sdlc", "resume", id, "--retry-failed", "--step"); code != app.ExitOK {
		t.Fatalf("reassess: %d %s", code, errors)
	}
	stored, _ = store.ReadRun()
	if stored.Adaptive.Stage != adaptive.Done || stored.Adaptive.DiffRevision == oldRevision || len(executor.requests) != 3 {
		t.Fatalf("reassessed: %+v requests=%d", stored.Adaptive, len(executor.requests))
	}
}

func TestSavedReviewRecoversUsageAfterCrashBeforeResult(t *testing.T) {
	a := newApp(t)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan"},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"},
	}}
	id := startReviewRun(t, a, executor)
	store := ledger.Open(a.SDLCRunsDir(), id)
	stored, _ := store.ReadRun()
	assignment := adaptive.Assignment{InvocationID: "run-20260925T154556Z-b0a3accc", AgentID: "assessor", Binding: "runtime:opencode:a::", Runtime: "opencode", Role: "assessor", Revision: stored.Adaptive.DiffRevision}
	in, out := int64(31), int64(5)
	reply := worker.Reply{Outcome: "changes-required", Content: "Fix this", WorkspaceDrift: []string{"README.md"}, InputTokens: &in, OutputTokens: &out}
	if err := a.saveReviewRecovery(store, assignment, "a", reply); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverReview(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverReview(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	stored, _ = store.ReadRun()
	count := 0
	for _, u := range stored.Usage {
		if u.Invocation == assignment.InvocationID && u.InputTokens != nil && *u.InputTokens == in {
			count++
		}
	}
	if count != 1 || stored.Adaptive.Stage != adaptive.Implementing || !stored.ReviewRecovery.Applied {
		t.Fatalf("crash recovery: usage=%d state=%+v", count, stored.Adaptive)
	}
}

func TestNonRetryableAssessorFailurePersistsPauseCause(t *testing.T) {
	a := newApp(t)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan"},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"},
	}}
	id := startReviewRun(t, a, executor)
	a.SdlcExecutor = hardFailureExecutor{}
	if code, _, errText := run(a, "", "sdlc", "drive", id, "--until-done"); code == app.ExitOK || !strings.Contains(errText, "workspace inspection required") {
		t.Fatalf("driver error: %d %s", code, errText)
	}
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if stored.Adaptive.Stage != adaptive.Paused || stored.Adaptive.Outcome != "assessor-failed" || !strings.Contains(stored.Adaptive.PendingReason, "workspace inspection required") {
		t.Fatalf("missing durable pause: %+v", stored.Adaptive)
	}
}
