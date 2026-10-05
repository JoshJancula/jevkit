package sdlc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

// sdlcIntegrateFanout is the supervisor-owned step that collects subtask patches,
// validates scopes/overlaps/stale bases, applies in merge order (preserving
// user dirty files), and advances the aggregate candidate. Subtasks never mark
// the run complete.
func (a *App) sdlcIntegrateFanout(ctx context.Context, runID string, store *ledger.Store, run ledger.Run) error {
	if run.Fanout == nil {
		return app.Failf("fan-out schedule missing for integration")
	}
	// Idempotent: already applied candidate stays put (no double-apply).
	if run.Integration != nil && run.Integration.Status == adaptive.IntegrationStatusApplied {
		a.Outf("run %s: integration already applied (%s)\n", runID, run.Integration.CandidateFingerprint)
		return nil
	}

	if err := a.budgetGate(&run, mustPolicy(a), "work"); err != nil {
		return err
	}
	stop, err := a.budgetActivity(ctx, run, runID+"/integration")
	if err != nil {
		return err
	}
	defer stop()
	patches := map[string][]byte{}
	handoffs := map[string]string{}
	changed := map[string][]string{}
	artifacts := map[string]string{}
	for id, st := range run.Fanout.Subtasks {
		art := fanoutArtifactName(st)
		artifacts[id] = art
		if raw, err := store.ReadArtifact(art); err == nil {
			patches[id] = raw
			handoffs[id] = strings.TrimSpace(firstLine(string(raw)))
		}
		paths := changedPathsFromReport(string(patches[id]))
		if len(paths) == 0 && st.Workspace != "" && st.Workspace != a.WorkDir && st.BaseRevision != "" {
			if p, err := listWorktreeChanges(ctx, st.Workspace, st.BaseRevision); err == nil {
				paths = p
			}
		}
		changed[id] = paths
	}
	contribs := adaptive.CollectContributions(*run.Fanout, patches, handoffs, changed, func(id string) string {
		return artifacts[id]
	})

	head := run.Fanout.SourceRevision
	if rev, err := worker.ResolveSourceRevision(ctx, a.WorkDir); err == nil {
		head = rev
	}
	dirty, _ := listDirtyPaths(ctx, a.WorkDir)
	var already []string
	if run.Integration != nil {
		already = append([]string(nil), run.Integration.AppliedSubtasks...)
	}
	rec := adaptive.PlanIntegration(*run.Fanout, contribs, adaptive.ProjectSurface{
		HeadRevision: head,
		DirtyPaths:   dirty,
	}, already)

	switch rec.Status {
	case adaptive.IntegrationStatusPaused:
		return a.persistIntegration(store, runID, rec, nil, "fanout-integration-blocked")
	case adaptive.IntegrationStatusRepair, adaptive.IntegrationStatusConflict:
		return a.persistIntegration(store, runID, rec, nil, "fanout-integration-repair")
	case adaptive.IntegrationStatusPending:
		// continue to apply
	default:
		return a.persistIntegration(store, runID, rec, nil, "fanout-integration-"+rec.Status)
	}

	byID := adaptive.ContributionsByID(contribs)
	if err := a.applyIntegrationPaths(ctx, rec, byID); err != nil {
		rec.Status = adaptive.IntegrationStatusPaused
		rec.Summary = "apply failed: " + err.Error()
		rec.Decisions = append(rec.Decisions, adaptive.IntegrationDecision{
			At: a.Clock().UTC().Format(time.RFC3339), Kind: "apply-failed", Detail: err.Error(),
		})
		return a.persistIntegration(store, runID, rec, nil, "fanout-integration-apply-failed")
	}
	combined := adaptive.CombinePatches(rec.ApplyOrder, byID)
	now := a.Clock()
	if err := rec.CommitApply(combined, now); err != nil {
		return app.Failf("commit integration: %v", err)
	}
	if err := store.WriteArtifact("patch.diff", combined); err != nil {
		return app.Failf("write patch.diff: %v", err)
	}
	decisionJSON, _ := json.MarshalIndent(rec, "", "  ")
	_ = store.WriteArtifact(adaptive.ArtifactIntegration, decisionJSON)
	for _, p := range rec.Provenance {
		_ = store.AppendDecision(ledger.Decision{
			RunID: runID, Kind: "fanout-integration-provenance", Stage: adaptive.Implementing,
			Invocation: p.InvocationID, Choice: p.SubtaskID, Detail: p.Artifact,
			Outcome: p.Status, Next: "inspect or delete with parent run",
		})
	}
	_ = store.AppendDecision(ledger.Decision{
		RunID: runID, Kind: "fanout-integration", Stage: adaptive.Implementing,
		Choice: rec.Status, Outcome: rec.CandidateFingerprint, Detail: rec.Summary,
		Next: "assess integrated candidate",
	})
	return a.persistIntegration(store, runID, rec, combined, "")
}

func (a *App) persistIntegration(store *ledger.Store, runID string, rec adaptive.IntegrationRecord, combined []byte, pauseReason string) error {
	now := a.Clock()
	err := store.UpdateIntegration(now, func(run *ledger.Run, integration *adaptive.IntegrationRecord) error {
		*integration = rec
		if run.Fanout != nil {
			run.Fanout.IntegrationPending = false
			if pauseReason != "" {
				run.Fanout.Pause(pauseReason)
			}
		}
		if rec.Status == adaptive.IntegrationStatusApplied && run.Adaptive != nil && len(combined) > 0 {
			st := *run.Adaptive
			st.TreeBudget = true
			if err := adaptive.ApplyIntegratedCandidate(&st, rec.CandidateFingerprint); err != nil {
				return err
			}
			run.Adaptive = &st
		} else if pauseReason != "" && run.Adaptive != nil {
			st := *run.Adaptive
			st.Pause(pauseReason)
			st.PendingReason = rec.Summary
			run.Adaptive = &st
		}
		return nil
	})
	if err != nil {
		return app.Failf("persist integration: %v", err)
	}
	if pauseReason != "" {
		a.Outf("run %s paused: %s (%s)\n", runID, pauseReason, rec.Summary)
	} else {
		a.Outf("run %s: integrated fan-out candidate %s\n", runID, rec.CandidateFingerprint)
	}
	return nil
}

func (a *App) applyIntegrationPaths(ctx context.Context, rec adaptive.IntegrationRecord, byID map[string]adaptive.SubtaskContribution) error {
	for _, pa := range rec.PathPlan {
		if pa.Action != "apply" {
			continue
		}
		c, ok := byID[pa.Subtask]
		if !ok || c.Workspace == "" || c.Workspace == a.WorkDir {
			// No isolated workspace — treat as report-only candidate (patch.diff).
			continue
		}
		src := filepath.Join(c.Workspace, filepath.FromSlash(pa.Path))
		dst := filepath.Join(a.WorkDir, filepath.FromSlash(pa.Path))
		data, err := os.ReadFile(src)
		if err != nil {
			if os.IsNotExist(err) {
				// Deletion in worktree: remove from project only if present and not dirty-preserved.
				_ = os.Remove(dst)
				continue
			}
			return fmt.Errorf("read %s from worktree: %w", pa.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	_ = ctx
	return nil
}

func fanoutArtifactName(st adaptive.SubtaskRecord) string {
	inv := ""
	if st.Result != nil && st.Result.InvocationID != "" {
		inv = st.Result.InvocationID
	} else if st.Assignment != nil {
		inv = st.Assignment.InvocationID
	}
	if inv == "" {
		return "fanout/" + st.ID + ".txt"
	}
	return "fanout/" + st.ID + "-" + inv + ".txt"
}

// changedPathsFromReport extracts paths from an invocation change report or a
// unified diff. Unknown formats yield an empty list (driver may fill via git).
func changedPathsFromReport(content string) []string {
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.Trim(p, `"' `)
		p = strings.TrimPrefix(p, "a/")
		p = strings.TrimPrefix(p, "b/")
		if p == "" || p == "/dev/null" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") && strings.Contains(line, `"`) {
			// - modified "path" (before ..., after ...)
			if i := strings.Index(line, `"`); i >= 0 {
				rest := line[i+1:]
				if j := strings.Index(rest, `"`); j >= 0 {
					add(rest[:j])
				}
			}
			continue
		}
		if strings.HasPrefix(line, "diff --git ") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				add(fields[2])
				add(fields[3])
			}
			continue
		}
		if strings.HasPrefix(line, "+++ b/") || strings.HasPrefix(line, "--- a/") {
			add(strings.TrimPrefix(strings.TrimPrefix(line, "+++ b/"), "--- a/"))
		}
	}
	return paths
}

func listDirtyPaths(ctx context.Context, repoRoot string) ([]string, error) {
	if repoRoot == "" {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-z")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, ent := range strings.Split(string(out), "\x00") {
		if len(ent) < 4 {
			continue
		}
		// XY PATH or XY ORIG -> PATH
		path := strings.TrimSpace(ent[3:])
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func listWorktreeChanges(ctx context.Context, workspace, baseRev string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "-z", baseRev)
	cmd.Dir = workspace
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(strings.Trim(string(out), "\x00"), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
