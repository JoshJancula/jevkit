package sdlc

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

// defaultSDLCStorageQuotaBytes is the total SDLC-state budget: `sdlc runs`
// displays usage against it, and sdlcQuotaCheck (see sdlcquota.go) pauses
// further work once it's exceeded. Reaching it never deletes anything on its
// own; a human always chooses what to prune.
const defaultSDLCStorageQuotaBytes int64 = 5 << 30 // 5 GiB

func (a *App) sdlcStorageQuotaBytes() int64 {
	if v := a.Getenv("JEVKIT_SDLC_STORAGE_QUOTA_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultSDLCStorageQuotaBytes
}

// sdlcInvocationLogInfo summarizes one invocation's saved output without
// reading the (possibly large) stream content itself.
type sdlcInvocationLogInfo struct {
	Invocation string
	Agent      string
	Runtime    string
	HasStdout  bool
	HasStderr  bool
	Truncated  bool
	// TruncatedOmittedBytes is the exact number of combined lines.jsonl bytes
	// dropped by tail rollover, read from the sidecar marker file worker
	// writes alongside it. Zero when Truncated is false.
	TruncatedOmittedBytes int64
	// Pruned is true once `sdlc prune --logs-only` has removed this
	// invocation's saved streams; the meta file (and this record) stays so
	// the invocation is still shown, never mistaken for one that was never
	// captured in the first place.
	Pruned bool
}

// sdlcRunInfo is the read-only inventory view of one saved run: everything
// `sdlc runs` and `sdlc show` need, gathered once per run directory.
type sdlcRunInfo struct {
	Run       ledger.Run
	Children  []string
	Bytes     int64
	Artifacts []string
	Logs      []sdlcInvocationLogInfo
	Path      string
}

// sdlcStatusText renders a run's lifecycle status the same way
// sdlcFinalSummary does, so `sdlc runs`/`sdlc show` agree with the summary
// printed at the end of a drive.
func sdlcStatusText(r ledger.Run) string {
	stage, outcome := "unknown", ""
	if r.Adaptive != nil {
		stage, outcome = r.Adaptive.Stage, r.Adaptive.Outcome
	}
	if outcome != "" {
		return stage + " (" + outcome + ")"
	}
	return stage
}

// dirSize sums the apparent size of every regular file under root. Symlinks
// are skipped rather than followed, so a planted symlink inside a run
// directory can't make this walk read or count bytes outside root. A
// missing directory (a run with no artifacts/logs yet) is zero, not an
// error.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil && os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

// sdlcArtifactNames lists every artifact stored under a run's artifacts
// directory, by the same slash-joined relative name WriteArtifact/
// ReadArtifact address it by (artifacts may nest, e.g. "docs/design.md").
func sdlcArtifactNames(runDir string) []string {
	base := filepath.Join(runDir, "artifacts")
	var names []string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if rel, relErr := filepath.Rel(base, path); relErr == nil {
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(names)
	return names
}

// sdlcInvocationLogs lists the invocations that saved output under a run's
// logs directory (see internal/sdlc/worker's log.go for the file layout:
// <id>.json metadata, <id>.stdout/<id>.stderr streams, and an
// <id>.lines.jsonl.truncated marker once the combined log exceeds
// worker.MaxLogTail). It reports availability and truncation, not content.
func sdlcInvocationLogs(runDir string) []sdlcInvocationLogInfo {
	base := filepath.Join(runDir, "logs")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []sdlcInvocationLogInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, e.Name()))
		if err != nil {
			continue
		}
		var meta worker.LogMeta
		if json.Unmarshal(data, &meta) != nil || !app.RunIDPattern.MatchString(meta.Invocation) {
			continue
		}
		info := sdlcInvocationLogInfo{Invocation: meta.Invocation, Agent: meta.Agent, Runtime: meta.Runtime}
		if _, err := os.Stat(filepath.Join(base, meta.Invocation+sdlcPrunedSuffix)); err == nil {
			info.Pruned = true
			out = append(out, info)
			continue
		}
		if fi, err := os.Stat(filepath.Join(base, meta.Invocation+".stdout")); err == nil && fi.Size() > 0 {
			info.HasStdout = true
		}
		if fi, err := os.Stat(filepath.Join(base, meta.Invocation+".stderr")); err == nil && fi.Size() > 0 {
			info.HasStderr = true
		}
		if raw, err := os.ReadFile(filepath.Join(base, meta.Invocation+".lines.jsonl.truncated")); err == nil {
			info.Truncated = true
			info.TruncatedOmittedBytes, _ = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Invocation < out[j].Invocation })
	return out
}

// sdlcInventory reads every saved run once, keyed by run ID, along with the
// list of runs-directory entries it refused: names that don't match a valid
// run ID, or that are symlinks (a symlink planted at <runs-dir>/<name>
// before this scan would otherwise get treated as a real run directory).
// ledger.Open itself refuses a symlinked run directory found *after* Open,
// but a top-level symlink entry never gets that far because os.ReadDir's
// DirEntry type reflects the entry itself, not what it points to.
func (a *App) sdlcInventory() (map[string]sdlcRunInfo, []string, error) {
	entries, err := os.ReadDir(a.SDLCRunsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]sdlcRunInfo{}, nil, nil
		}
		return nil, nil, err
	}
	runs := map[string]ledger.Run{}
	var refused []string
	for _, e := range entries {
		if !app.RunIDPattern.MatchString(e.Name()) {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			refused = append(refused, e.Name())
			continue
		}
		if !e.IsDir() {
			continue
		}
		r, err := ledger.Open(a.SDLCRunsDir(), e.Name()).ReadRun()
		if err != nil {
			refused = append(refused, e.Name())
			continue
		}
		runs[r.RunID] = r
	}
	out := make(map[string]sdlcRunInfo, len(runs))
	for id, r := range runs {
		dir := filepath.Join(a.SDLCRunsDir(), id)
		var children []string
		for _, other := range runs {
			if other.ParentRunID == id {
				children = append(children, other.RunID)
			}
		}
		sort.Strings(children)
		size, _ := dirSize(dir)
		out[id] = sdlcRunInfo{
			Run:       r,
			Children:  children,
			Bytes:     size,
			Artifacts: sdlcArtifactNames(dir),
			Logs:      sdlcInvocationLogs(dir),
			Path:      dir,
		}
	}
	sort.Strings(refused)
	return out, refused, nil
}

// sdlcRunsCmd is a read-only inventory of every saved run: status, size, and
// aggregate SDLC storage use against the informational quota, so a user can
// see what's safe to prune. It never deletes anything itself.
func (a *App) sdlcRunsCmd() *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:     "runs",
		Short:   "list saved SDLC runs with status, size, and storage use (read-only)",
		Example: "  jevkit sdlc runs",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if format != "text" && format != "json" {
				return app.Usagef("unknown --format %q (want text or json)", format)
			}
			infos, refused, err := a.sdlcInventory()
			if err != nil {
				return app.Failf("read saved runs: %v", err)
			}
			ids := make([]string, 0, len(infos))
			for id := range infos {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return infos[ids[i]].Run.CreatedAt < infos[ids[j]].Run.CreatedAt })
			var total int64
			for _, id := range ids {
				total += infos[id].Bytes
			}
			quota := a.sdlcStorageQuotaBytes()
			largest := append([]string(nil), ids...)
			sort.Slice(largest, func(i, j int) bool { return infos[largest[i]].Bytes > infos[largest[j]].Bytes })
			if len(largest) > 5 {
				largest = largest[:5]
			}

			if format == "json" {
				return a.sdlcRunsJSON(infos, ids, refused, largest, total, quota)
			}

			a.Heading("SDLC RUNS")
			if len(ids) == 0 {
				a.Outf("  none\n")
			} else {
				rows := make([][]string, 0, len(ids))
				for _, id := range ids {
					info := infos[id]
					rows = append(rows, []string{
						info.Run.RunID, sdlcStatusText(info.Run), info.Run.WorkDir,
						info.Run.CreatedAt, info.Run.UpdatedAt, formatBytes(info.Bytes),
						strconv.Itoa(len(info.Children)),
					})
				}
				a.Table([]string{"RUN ID", "STATUS", "WORKDIR", "CREATED", "UPDATED", "SIZE", "CHILDREN"}, rows)
			}
			if len(refused) > 0 {
				a.Outf("\nRefused entries (invalid ID or symlinked run directory): %s\n", strings.Join(refused, ", "))
			}
			a.Outf("\nStorage: %s used of %s quota", formatBytes(total), formatBytes(quota))
			if total > quota {
				a.Outf(" (%s)\n", a.Styled(a.Stdout, app.ANSIRed, "over quota"))
			} else {
				a.Outf("\n")
			}
			if len(largest) > 0 {
				a.Outf("\n")
				a.Heading("LARGEST RUNS (candidates to prune)")
				rows := make([][]string, 0, len(largest))
				for _, id := range largest {
					rows = append(rows, []string{id, formatBytes(infos[id].Bytes), infos[id].Path})
				}
				a.Table([]string{"RUN ID", "SIZE", "PATH"}, rows)
				a.Outf("  jevkit does not delete saved runs on its own; use `jevkit sdlc prune` or `jevkit sdlc delete RUN_ID --apply`.\n")
			}
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return c
}

func (a *App) sdlcRunsJSON(infos map[string]sdlcRunInfo, ids, refused, largest []string, total, quota int64) error {
	type jsonRun struct {
		RunID     string   `json:"runId"`
		Status    string   `json:"status"`
		Workflow  string   `json:"workflow,omitempty"`
		WorkDir   string   `json:"workDir,omitempty"`
		CreatedAt string   `json:"createdAt"`
		UpdatedAt string   `json:"updatedAt"`
		Bytes     int64    `json:"bytes"`
		Children  []string `json:"childRuns,omitempty"`
		Artifacts []string `json:"artifacts,omitempty"`
		HasLogs   bool     `json:"hasLogs"`
		Path      string   `json:"path"`
	}
	out := struct {
		Runs           []jsonRun `json:"runs"`
		RefusedEntries []string  `json:"refusedEntries,omitempty"`
		LargestRunIDs  []string  `json:"largestRunIds,omitempty"`
		TotalBytes     int64     `json:"totalBytes"`
		QuotaBytes     int64     `json:"quotaBytes"`
		OverQuota      bool      `json:"overQuota"`
	}{RefusedEntries: refused, LargestRunIDs: largest, TotalBytes: total, QuotaBytes: quota, OverQuota: total > quota}
	for _, id := range ids {
		info := infos[id]
		out.Runs = append(out.Runs, jsonRun{
			RunID: info.Run.RunID, Status: sdlcStatusText(info.Run), Workflow: info.Run.Workflow,
			WorkDir: info.Run.WorkDir, CreatedAt: info.Run.CreatedAt, UpdatedAt: info.Run.UpdatedAt,
			Bytes: info.Bytes, Children: info.Children, Artifacts: info.Artifacts,
			HasLogs: len(info.Logs) > 0, Path: info.Path,
		})
	}
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// sdlcShowCmd is a read-only, single-run detail view: status, size,
// artifacts, and saved log availability, with the run's task text redacted
// by default (--raw shows it unredacted, the same opt-in shape sdlc logs
// uses for saved output).
func (a *App) sdlcShowCmd() *cobra.Command {
	var format string
	var raw bool
	c := &cobra.Command{
		Use:     "show <run-id>",
		Short:   "show one saved run's status, storage, artifacts, and logs (read-only)",
		Example: "  jevkit sdlc show RUN_ID",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return app.Usagef("unknown --format %q (want text or json)", format)
			}
			runID := args[0]
			if !app.RunIDPattern.MatchString(runID) {
				return app.Usagef("invalid run ID")
			}
			r, err := ledger.Open(a.SDLCRunsDir(), runID).ReadRun()
			if err != nil {
				return app.Failf("%v", err)
			}
			dir := filepath.Join(a.SDLCRunsDir(), runID)
			infos, _, err := a.sdlcInventory()
			if err != nil {
				return app.Failf("read saved runs: %v", err)
			}
			info, ok := infos[runID]
			if !ok {
				info = sdlcRunInfo{Run: r, Path: dir, Artifacts: sdlcArtifactNames(dir), Logs: sdlcInvocationLogs(dir)}
				info.Bytes, _ = dirSize(dir)
			}
			task := r.Task
			if !raw && task != "" {
				task = a.sdlcRedactedDisplay(task)
			}

			if format == "json" {
				return a.sdlcShowJSON(info, task, raw)
			}

			a.Heading("RUN " + r.RunID)
			a.Outf("  Status:   %s\n", sdlcStatusText(r))
			if r.Workflow != "" {
				a.Outf("  Workflow: %s\n", r.Workflow)
			}
			if r.WorkDir != "" {
				a.Outf("  Workdir:  %s\n", r.WorkDir)
			}
			if r.ParentRunID != "" {
				a.Outf("  Parent:   %s\n", r.ParentRunID)
			}
			a.Outf("  Created:  %s\n", r.CreatedAt)
			a.Outf("  Updated:  %s\n", r.UpdatedAt)
			a.Outf("  Size:     %s\n", formatBytes(info.Bytes))
			a.Outf("  Path:     %s\n", info.Path)
			if len(info.Children) > 0 {
				a.Outf("  Children: %s\n", strings.Join(info.Children, ", "))
			}
			if task != "" {
				a.Outf("  Task:     %s\n", task)
			}
			a.Outf("\n")
			a.Heading("ARTIFACTS")
			if len(info.Artifacts) == 0 {
				a.Outf("  none\n")
			} else {
				for _, name := range info.Artifacts {
					a.Outf("  %s\n", name)
				}
			}
			a.Outf("\n")
			a.Heading("LOGS")
			if len(info.Logs) == 0 {
				a.Outf("  none saved\n")
			} else {
				rows := make([][]string, 0, len(info.Logs))
				for _, l := range info.Logs {
					rows = append(rows, []string{l.Invocation, l.Agent, l.Runtime, sdlcLogAvailabilityText(l)})
				}
				a.Table([]string{"INVOCATION", "AGENT", "RUNTIME", "AVAILABLE"}, rows)
			}
			if !raw {
				a.Outf("\nTask text is redacted; use --raw to show it unredacted. Pattern redaction is best-effort and does not guarantee a secret-free log.\n")
			}
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", "text", "output format: text or json")
	c.Flags().BoolVar(&raw, "raw", false, "show unredacted task text")
	return c
}

// sdlcLogAvailabilityText renders one invocation's saved-stream availability.
// A pruned invocation says so explicitly ("pruned") rather than "none", so it
// is never mistaken for output that was never captured.
func sdlcLogAvailabilityText(l sdlcInvocationLogInfo) string {
	if l.Pruned {
		return "pruned"
	}
	var avail []string
	if l.HasStdout {
		avail = append(avail, "stdout")
	}
	if l.HasStderr {
		avail = append(avail, "stderr")
	}
	availText := strings.Join(avail, "+")
	if availText == "" {
		availText = "none"
	}
	if l.Truncated {
		availText += fmt.Sprintf(" (truncated: %d bytes omitted)", l.TruncatedOmittedBytes)
	}
	return availText
}

func (a *App) sdlcShowJSON(info sdlcRunInfo, task string, raw bool) error {
	type jsonLog struct {
		Invocation            string `json:"invocation"`
		Agent                 string `json:"agent"`
		Runtime               string `json:"runtime"`
		HasStdout             bool   `json:"hasStdout"`
		HasStderr             bool   `json:"hasStderr"`
		Truncated             bool   `json:"truncated"`
		TruncatedOmittedBytes int64  `json:"truncatedOmittedBytes,omitempty"`
		Pruned                bool   `json:"pruned"`
	}
	out := struct {
		RunID     string    `json:"runId"`
		Status    string    `json:"status"`
		Workflow  string    `json:"workflow,omitempty"`
		WorkDir   string    `json:"workDir,omitempty"`
		ParentID  string    `json:"parentRunId,omitempty"`
		CreatedAt string    `json:"createdAt"`
		UpdatedAt string    `json:"updatedAt"`
		Bytes     int64     `json:"bytes"`
		Path      string    `json:"path"`
		Children  []string  `json:"childRuns,omitempty"`
		Artifacts []string  `json:"artifacts,omitempty"`
		Logs      []jsonLog `json:"logs,omitempty"`
		Task      string    `json:"task,omitempty"`
		TaskRaw   bool      `json:"taskRaw"`
	}{
		RunID: info.Run.RunID, Status: sdlcStatusText(info.Run), Workflow: info.Run.Workflow,
		WorkDir: info.Run.WorkDir, ParentID: info.Run.ParentRunID, CreatedAt: info.Run.CreatedAt,
		UpdatedAt: info.Run.UpdatedAt, Bytes: info.Bytes, Path: info.Path, Children: info.Children,
		Artifacts: info.Artifacts, Task: task, TaskRaw: raw,
	}
	for _, l := range info.Logs {
		out.Logs = append(out.Logs, jsonLog(l))
	}
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// formatBytes renders n as a human-scaled binary size (e.g. "4.2 MiB").
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "iB"
}
