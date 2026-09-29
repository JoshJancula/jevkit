package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
)

// sdlcPrunedSuffix marks a saved invocation whose diagnostic streams were
// removed by `sdlc prune --logs-only`. The invocation's <id>.json metadata is
// kept, so it still shows up in `sdlc show`/`sdlc runs` — as pruned, never as
// an invocation that was never captured.
const sdlcPrunedSuffix = ".pruned"

// sdlcRunActive reports whether r is still in progress: a run with no
// adaptive state yet has not reached a resting point, and a run whose stage
// is neither Done nor Paused is mid-work. Only Done and Paused runs are safe
// to delete or prune.
func sdlcRunActive(r ledger.Run) bool {
	if r.Adaptive == nil {
		return true
	}
	return r.Adaptive.Stage != adaptive.Done && r.Adaptive.Stage != adaptive.Paused
}

// sdlcDeleteRunSummary is one run's row in a delete/prune preview or report.
type sdlcDeleteRunSummary struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes"`
	Active bool   `json:"active"`
}

// sdlcDeletePlan describes what deleting the run tree rooted at scope[0]
// would do: which runs it covers, their sizes, and which ones (if any) block
// the deletion because they are still active.
func (a *App) sdlcDeletePlan(scope []ledger.Run) (rows []sdlcDeleteRunSummary, activeIDs []string, totalBytes int64) {
	for _, r := range scope {
		dir := filepath.Join(a.sdlcRunsDir(), r.RunID)
		size, _ := dirSize(dir)
		totalBytes += size
		active := sdlcRunActive(r)
		if active {
			activeIDs = append(activeIDs, r.RunID)
		}
		rows = append(rows, sdlcDeleteRunSummary{RunID: r.RunID, Status: sdlcStatusText(r), Bytes: size, Active: active})
	}
	return rows, activeIDs, totalBytes
}

// sdlcDeleteApply removes every run directory in scope (children first, then
// their parent) and then drops the attributable usage records those runs
// owned. Each run is re-checked, under its own update lock, immediately
// before removal: a run that started running again since the plan was built,
// or whose directory turned out to be a symlink, aborts the whole operation
// without deleting anything further. Runs already removed by a prior call
// (idempotence) are treated as already done, not an error.
func (a *App) sdlcDeleteApply(scope []ledger.Run) (deleted []string, usageRemoved int, err error) {
	ordered := append([]ledger.Run(nil), scope...)
	for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	}
	for _, r := range ordered {
		store := ledger.Open(a.sdlcRunsDir(), r.RunID)
		lockErr := store.WithRunLock(func() error {
			fresh, readErr := store.ReadRun()
			if os.IsNotExist(readErr) {
				return nil // already deleted: idempotent no-op
			}
			if readErr != nil {
				return readErr
			}
			if sdlcRunActive(fresh) {
				return fmt.Errorf("run %s is active, refusing to delete", fresh.RunID)
			}
			if fi, statErr := os.Lstat(store.Dir); statErr == nil && fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to delete symlinked run directory %q", store.Dir)
			}
			return os.RemoveAll(store.Dir)
		})
		if lockErr != nil {
			return deleted, 0, fmt.Errorf("deleting run %s: %w (already deleted: %s)", r.RunID, lockErr, strings.Join(deleted, ", "))
		}
		deleted = append(deleted, r.RunID)
	}
	ids := make(map[string]bool, len(deleted))
	for _, id := range deleted {
		ids[id] = true
	}
	removed, err := usage.RemoveRuns(a.stateHome(), ids)
	if err != nil {
		return deleted, 0, fmt.Errorf("deleted runs but failed to remove attributable usage records: %w", err)
	}
	return deleted, removed, nil
}

func (a *App) sdlcRenderDeletePlan(rows []sdlcDeleteRunSummary, activeIDs []string, totalBytes int64, usageCount int, applied bool, usageRemoved int) {
	if applied {
		a.heading("DELETED")
	} else {
		a.heading("WOULD DELETE (preview; pass --apply to delete)")
	}
	if len(rows) == 0 {
		a.outf("  none\n")
		return
	}
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		status := row.Status
		if row.Active {
			status = a.styled(a.Stdout, ansiRed, status+" (blocks delete)")
		}
		out = append(out, []string{row.RunID, status, formatBytes(row.Bytes)})
	}
	a.table([]string{"RUN ID", "STATUS", "SIZE"}, out)
	a.outf("\nTotal: %s across %d run(s)\n", formatBytes(totalBytes), len(rows))
	if usageCount > 0 {
		verb := "would remove"
		if applied {
			verb = "removed"
		}
		a.outf("Attributable usage records: %s %d (%d)\n", verb, usageCount, usageRemoved)
	}
	if len(activeIDs) > 0 {
		a.outf("\nRefusing: %d active run(s) block deletion: %s\n", len(activeIDs), strings.Join(activeIDs, ", "))
	}
	a.outf("\nUnrecognized entries under the runs directory and the shared redaction-audit log are never touched by this command.\n")
	a.outf("jevkit cannot erase any session state the agent runtime itself keeps (e.g. a Claude/Codex session store); it only removes what it saved locally.\n")
}

// sdlcDeleteCmd deletes one run and its full descendant tree: preview by
// default, --apply to actually remove. Active runs anywhere in the tree
// refuse the whole operation.
func (a *App) sdlcDeleteCmd() *cobra.Command {
	var apply bool
	var format string
	c := &cobra.Command{
		Use:     "delete <run-id>",
		Short:   "delete a saved run and its child runs (preview by default; --apply to delete)",
		Example: "  jevkit sdlc delete RUN_ID\n  jevkit sdlc delete RUN_ID --apply",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return usagef("unknown --format %q (want text or json)", format)
			}
			scope, err := a.sdlcTree(args[0])
			if err != nil {
				return failf("%v", err)
			}
			rows, activeIDs, totalBytes := a.sdlcDeletePlan(scope)
			ids := make(map[string]bool, len(scope))
			for _, r := range scope {
				ids[r.RunID] = true
			}
			usageCount, err := usage.CountRuns(a.stateHome(), ids)
			if err != nil {
				return failf("count attributable usage: %v", err)
			}

			if !apply {
				if format == "json" {
					return a.sdlcEncodeDeleteReport(rows, activeIDs, totalBytes, usageCount, false, nil, 0)
				}
				a.sdlcRenderDeletePlan(rows, activeIDs, totalBytes, usageCount, false, 0)
				return nil
			}
			if len(activeIDs) > 0 {
				return failf("refusing to delete: %d active run(s) in scope: %s", len(activeIDs), strings.Join(activeIDs, ", "))
			}
			deletedIDs, usageRemoved, err := a.sdlcDeleteApply(scope)
			if err != nil {
				return failf("%v", err)
			}
			if format == "json" {
				return a.sdlcEncodeDeleteReport(rows, activeIDs, totalBytes, usageCount, true, deletedIDs, usageRemoved)
			}
			a.sdlcRenderDeletePlan(rows, activeIDs, totalBytes, usageCount, true, usageRemoved)
			return nil
		},
	}
	c.Flags().BoolVar(&apply, "apply", false, "actually delete (default is preview only)")
	c.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return c
}

func (a *App) sdlcEncodeDeleteReport(rows []sdlcDeleteRunSummary, activeIDs []string, totalBytes int64, usageCount int, applied bool, deletedIDs []string, usageRemoved int) error {
	out := struct {
		Runs                []sdlcDeleteRunSummary `json:"runs"`
		ActiveRunIDs        []string               `json:"activeRunIds,omitempty"`
		TotalBytes          int64                  `json:"totalBytes"`
		AttributableUsage   int                    `json:"attributableUsageRecords"`
		Applied             bool                   `json:"applied"`
		DeletedRunIDs       []string               `json:"deletedRunIds,omitempty"`
		UsageRecordsRemoved int                    `json:"usageRecordsRemoved,omitempty"`
		Note                string                 `json:"note"`
	}{
		Runs: rows, ActiveRunIDs: activeIDs, TotalBytes: totalBytes, AttributableUsage: usageCount,
		Applied: applied, DeletedRunIDs: deletedIDs, UsageRecordsRemoved: usageRemoved,
		Note: "jevkit cannot erase session state kept by the agent runtime itself; it only removes what it saved locally",
	}
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// sdlcPruneLogs removes saved diagnostic streams (stdout/stderr/lines.jsonl,
// plus any truncation marker) for every invocation under runDir/logs that
// isn't already pruned, leaving the invocation's <id>.json metadata and the
// rest of the run (run.json, artifacts) untouched. It marks each invocation
// pruned so run inventory reports it accurately, and returns the bytes freed.
func sdlcPruneLogs(runDir string) (freedBytes int64, prunedInvocations []string, err error) {
	base := filepath.Join(runDir, "logs")
	entries, readErr := os.ReadDir(base)
	if os.IsNotExist(readErr) {
		return 0, nil, nil
	}
	if readErr != nil {
		return 0, nil, readErr
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, readFileErr := os.ReadFile(filepath.Join(base, e.Name()))
		if readFileErr != nil {
			continue
		}
		var meta struct {
			Invocation string `json:"invocation"`
		}
		if json.Unmarshal(data, &meta) != nil || !sdlcRunIDRE.MatchString(meta.Invocation) {
			continue
		}
		markerPath := filepath.Join(base, meta.Invocation+sdlcPrunedSuffix)
		if _, statErr := os.Stat(markerPath); statErr == nil {
			continue // already pruned
		}
		var removedAny bool
		for _, name := range []string{
			meta.Invocation + ".stdout", meta.Invocation + ".stderr",
			meta.Invocation + ".lines.jsonl", meta.Invocation + ".lines.jsonl.truncated",
		} {
			path := filepath.Join(base, name)
			if fi, statErr := os.Stat(path); statErr == nil {
				freedBytes += fi.Size()
				if rmErr := os.Remove(path); rmErr == nil {
					removedAny = true
				}
			}
		}
		if removedAny {
			_ = os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
			prunedInvocations = append(prunedInvocations, meta.Invocation)
		}
	}
	vFreed, vPruned, vErr := sdlcPruneVerificationLogs(runDir)
	freedBytes += vFreed
	prunedInvocations = append(prunedInvocations, vPruned...)
	if vErr != nil {
		err = vErr
	}
	sort.Strings(prunedInvocations)
	return freedBytes, prunedInvocations, err
}

// sdlcPruneVerificationLogs removes diagnostic check output under
// logs/verification while keeping artifacts/verification receipts.
func sdlcPruneVerificationLogs(runDir string) (freedBytes int64, pruned []string, err error) {
	base := filepath.Join(runDir, "logs", "verification")
	entries, readErr := os.ReadDir(base)
	if os.IsNotExist(readErr) {
		return 0, nil, nil
	}
	if readErr != nil {
		return 0, nil, readErr
	}
	markerPath := filepath.Join(base, "verification"+sdlcPrunedSuffix)
	if _, statErr := os.Stat(markerPath); statErr == nil {
		return 0, nil, nil
	}
	var removedAny bool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, sdlcPrunedSuffix) || strings.HasSuffix(name, ".json") {
			continue // keep meta JSON; strip .log streams only
		}
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".stdout") && !strings.HasSuffix(name, ".stderr") {
			continue
		}
		path := filepath.Join(base, name)
		if fi, statErr := os.Stat(path); statErr == nil {
			freedBytes += fi.Size()
			if rmErr := os.Remove(path); rmErr == nil {
				removedAny = true
				pruned = append(pruned, "verification/"+strings.TrimSuffix(name, filepath.Ext(name)))
			}
		}
	}
	if removedAny {
		_ = os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
	}
	sort.Strings(pruned)
	return freedBytes, pruned, nil
}

// sdlcPruneSelection is one root run (and its descendant tree) that matched
// a prune selector.
type sdlcPruneSelection struct {
	Root  ledger.Run
	Scope []ledger.Run
}

// sdlcSelectPruneCandidates walks every root-level saved run (ParentRunID ==
// "") and keeps the ones whose whole descendant tree is inactive and old
// enough / matching the requested status. A tree with any active member is
// skipped entirely: deleting or log-pruning a parent while a child (or vice
// versa) is still running would leave the run tree inconsistent.
func (a *App) sdlcSelectPruneCandidates(olderThan time.Duration, status string, now time.Time) ([]sdlcPruneSelection, error) {
	infos, _, err := a.sdlcInventory()
	if err != nil {
		return nil, err
	}
	var roots []string
	for id, info := range infos {
		if info.Run.ParentRunID == "" {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)

	var out []sdlcPruneSelection
	for _, id := range roots {
		root := infos[id].Run
		if status != "" && sdlcStatusStage(root) != status {
			continue
		}
		if olderThan > 0 {
			updated, parseErr := time.Parse(time.RFC3339, root.UpdatedAt)
			if parseErr != nil || now.Sub(updated) < olderThan {
				continue
			}
		}
		scope, treeErr := a.sdlcTree(id)
		if treeErr != nil {
			continue
		}
		if _, activeIDs, _ := a.sdlcDeletePlan(scope); len(activeIDs) > 0 {
			continue
		}
		out = append(out, sdlcPruneSelection{Root: root, Scope: scope})
	}
	return out, nil
}

func sdlcStatusStage(r ledger.Run) string {
	if r.Adaptive == nil {
		return ""
	}
	return r.Adaptive.Stage
}

// sdlcPruneCmd previews or applies pruning of inactive run trees by age and
// status, and (with --logs-only) can instead strip only saved diagnostic
// streams from selected runs while keeping plans, receipts, status, and
// usage. Nothing is ever removed unless --apply is passed, and this command
// is never invoked automatically by jevkit itself.
func (a *App) sdlcPruneCmd() *cobra.Command {
	var apply, logsOnly bool
	var olderThanStr, status, format string
	c := &cobra.Command{
		Use:   "prune",
		Short: "prune inactive saved runs by age/status (preview by default; --apply to delete)",
		Example: "  jevkit sdlc prune --older-than 720h\n" +
			"  jevkit sdlc prune --older-than 720h --status done --apply\n" +
			"  jevkit sdlc prune --logs-only --older-than 168h --apply",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if format != "text" && format != "json" {
				return usagef("unknown --format %q (want text or json)", format)
			}
			if status != "" && status != adaptive.Done && status != adaptive.Paused {
				return usagef("--status must be %q or %q", adaptive.Done, adaptive.Paused)
			}
			var olderThan time.Duration
			if olderThanStr != "" {
				var parseErr error
				olderThan, parseErr = time.ParseDuration(olderThanStr)
				if parseErr != nil {
					return usagef("invalid --older-than %q: %v", olderThanStr, parseErr)
				}
			}
			selections, err := a.sdlcSelectPruneCandidates(olderThan, status, a.now())
			if err != nil {
				return failf("select prune candidates: %v", err)
			}

			if logsOnly {
				return a.sdlcRunPruneLogsOnly(selections, apply, format)
			}
			return a.sdlcRunPruneDelete(selections, apply, format)
		},
	}
	c.Flags().BoolVar(&apply, "apply", false, "actually prune (default is preview only)")
	c.Flags().BoolVar(&logsOnly, "logs-only", false, "remove only saved diagnostic streams; keep plans, receipts, status, and usage")
	c.Flags().StringVar(&olderThanStr, "older-than", "", "only prune runs whose last update is older than this (e.g. 720h)")
	c.Flags().StringVar(&status, "status", "", "only prune runs in this status: done or paused (default: both)")
	c.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return c
}

func (a *App) sdlcRunPruneDelete(selections []sdlcPruneSelection, apply bool, format string) error {
	var allRows []sdlcDeleteRunSummary
	var allScope []ledger.Run
	for _, sel := range selections {
		rows, _, _ := a.sdlcDeletePlan(sel.Scope)
		allRows = append(allRows, rows...)
		allScope = append(allScope, sel.Scope...)
	}
	ids := make(map[string]bool, len(allScope))
	for _, r := range allScope {
		ids[r.RunID] = true
	}
	usageCount, err := usage.CountRuns(a.stateHome(), ids)
	if err != nil {
		return failf("count attributable usage: %v", err)
	}
	var totalBytes int64
	for _, row := range allRows {
		totalBytes += row.Bytes
	}

	if !apply {
		if format == "json" {
			return a.sdlcEncodeDeleteReport(allRows, nil, totalBytes, usageCount, false, nil, 0)
		}
		a.sdlcRenderDeletePlan(allRows, nil, totalBytes, usageCount, false, 0)
		return nil
	}
	var deletedIDs []string
	var usageRemoved int
	for _, sel := range selections {
		ids, removed, delErr := a.sdlcDeleteApply(sel.Scope)
		deletedIDs = append(deletedIDs, ids...)
		usageRemoved += removed
		if delErr != nil {
			return failf("%v", delErr)
		}
	}
	if format == "json" {
		return a.sdlcEncodeDeleteReport(allRows, nil, totalBytes, usageCount, true, deletedIDs, usageRemoved)
	}
	a.sdlcRenderDeletePlan(allRows, nil, totalBytes, usageCount, true, usageRemoved)
	return nil
}

func (a *App) sdlcRunPruneLogsOnly(selections []sdlcPruneSelection, apply bool, format string) error {
	type row struct {
		RunID             string
		EstimatedLogBytes int64
	}
	var rows []row
	var totalLogBytes int64
	for _, sel := range selections {
		for _, r := range sel.Scope {
			dir := filepath.Join(a.sdlcRunsDir(), r.RunID)
			logSize, _ := dirSize(filepath.Join(dir, "logs"))
			if logSize == 0 {
				continue
			}
			rows = append(rows, row{RunID: r.RunID, EstimatedLogBytes: logSize})
			totalLogBytes += logSize
		}
	}

	if !apply {
		if format == "json" {
			out := struct {
				Runs                []row `json:"runs"`
				EstimatedTotalBytes int64 `json:"estimatedTotalBytes"`
				Applied             bool  `json:"applied"`
			}{Runs: rows, EstimatedTotalBytes: totalLogBytes}
			enc := json.NewEncoder(a.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		a.heading("WOULD PRUNE LOGS (preview; pass --apply to prune)")
		if len(rows) == 0 {
			a.outf("  none\n")
			return nil
		}
		tbl := make([][]string, 0, len(rows))
		for _, rr := range rows {
			tbl = append(tbl, []string{rr.RunID, formatBytes(rr.EstimatedLogBytes)})
		}
		a.table([]string{"RUN ID", "ESTIMATED LOG SIZE"}, tbl)
		a.outf("\nTotal: %s of saved diagnostic streams across %d run(s)\n", formatBytes(totalLogBytes), len(rows))
		a.outf("Plans, receipts, status, and usage records are kept; only stdout/stderr/lines and verification logs are removed.\n")
		return nil
	}

	var freedTotal int64
	pruned := map[string][]string{}
	for _, sel := range selections {
		for _, r := range sel.Scope {
			dir := filepath.Join(a.sdlcRunsDir(), r.RunID)
			freed, invocations, err := sdlcPruneLogs(dir)
			if err != nil {
				return failf("pruning logs for run %s: %v", r.RunID, err)
			}
			if len(invocations) > 0 {
				freedTotal += freed
				pruned[r.RunID] = invocations
			}
		}
	}
	if format == "json" {
		out := struct {
			PrunedInvocationsByRun map[string][]string `json:"prunedInvocationsByRun"`
			FreedBytes             int64               `json:"freedBytes"`
			Applied                bool                `json:"applied"`
		}{PrunedInvocationsByRun: pruned, FreedBytes: freedTotal, Applied: true}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	a.heading("PRUNED LOGS")
	if len(pruned) == 0 {
		a.outf("  none\n")
		return nil
	}
	ids := make([]string, 0, len(pruned))
	for id := range pruned {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a.outf("  %s: %s\n", id, strings.Join(pruned[id], ", "))
	}
	a.outf("\nFreed %s of saved diagnostic streams. Plans, receipts, status, and usage records are kept.\n", formatBytes(freedTotal))
	return nil
}
