package main

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

// defaultSDLCRunTreeQuotaBytes bounds one root run's whole descendant tree:
// its own artifacts/logs plus every child run's. Each invocation's saved
// stdout/stderr/lines.jsonl is independently capped at worker.MaxLogTail
// (1 MiB each by default, ~3 MiB per invocation including all three), and
// this codebase's own fixture invocations run a few KB to a few hundred KB,
// so a tree with dozens of planner/implementer/assessor/specialist/child-run
// invocations still lands well under this default; it is reached only by an
// unusually long-running or heavily delegated tree.
const defaultSDLCRunTreeQuotaBytes int64 = 256 << 20 // 256 MiB

// sdlcLogTailBytes is the per-invocation stdout/stderr/lines.jsonl retention
// bound (see worker.MaxLogTail's doc comment for the reasoning behind its
// default), overridable per deployment.
func (a *App) sdlcLogTailBytes() int {
	if v := a.getenv("JEVKIT_SDLC_LOG_TAIL_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return worker.MaxLogTail
}

func (a *App) sdlcRunTreeQuotaBytes() int64 {
	if v := a.getenv("JEVKIT_SDLC_RUN_TREE_QUOTA_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultSDLCRunTreeQuotaBytes
}

// sdlcRunTreeRoot walks ParentRunID links up from runID to its topmost
// ancestor, so run-tree quota accounting covers a whole delegated tree
// (planner/implementer/assessor/specialist and every child run), not just
// the one run currently being driven.
func (a *App) sdlcRunTreeRoot(runID string) (string, error) {
	id := runID
	for {
		r, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
		if err != nil {
			return "", err
		}
		if r.ParentRunID == "" {
			return id, nil
		}
		id = r.ParentRunID
	}
}

// sdlcQuotaCheck refuses to start another invocation once either the run
// tree containing runID, or total saved SDLC state, has reached its quota.
// It never deletes or truncates anything on its own; the returned error
// names the current usage, the limit, and the exact prune/inspect commands
// a human can run to free space before retrying — never a claim that older
// runs were silently removed.
func (a *App) sdlcQuotaCheck(runID string) error {
	root, err := a.sdlcRunTreeRoot(runID)
	if err != nil {
		return fmt.Errorf("resolve run tree root: %w", err)
	}
	treeRuns, err := a.sdlcTree(root)
	if err != nil {
		return fmt.Errorf("size run tree: %w", err)
	}
	var treeBytes int64
	for _, r := range treeRuns {
		size, _ := dirSize(filepath.Join(a.sdlcRunsDir(), r.RunID))
		treeBytes += size
	}
	if treeQuota := a.sdlcRunTreeQuotaBytes(); treeBytes > treeQuota {
		return fmt.Errorf("run tree %s has reached its storage quota: %s used of %s (JEVKIT_SDLC_RUN_TREE_QUOTA_BYTES); "+
			"inspect with `jevkit sdlc show %s`, free space with `jevkit sdlc prune --logs-only --older-than 168h --apply` "+
			"or `jevkit sdlc delete <finished-run-id> --apply`, then retry with `jevkit sdlc resume %s --retry-failed`",
			root, formatBytes(treeBytes), formatBytes(treeQuota), root, runID)
	}
	totalBytes, err := dirSize(a.sdlcRunsDir())
	if err != nil {
		return fmt.Errorf("size SDLC state: %w", err)
	}
	if totalQuota := a.sdlcStorageQuotaBytes(); totalBytes > totalQuota {
		return fmt.Errorf("SDLC state has reached its total storage quota: %s used of %s (JEVKIT_SDLC_STORAGE_QUOTA_BYTES); "+
			"inspect with `jevkit sdlc runs`, free space with `jevkit sdlc prune --older-than 720h --apply` "+
			"or `jevkit sdlc prune --logs-only --older-than 168h --apply`, then retry with `jevkit sdlc resume %s --retry-failed`",
			formatBytes(totalBytes), formatBytes(totalQuota), runID)
	}
	return nil
}
