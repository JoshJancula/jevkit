package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/redact/config"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
	"github.com/spf13/cobra"
)

type logFilters struct {
	agent, runtime, invocation, stream string
	raw, follow                        bool
}

type displayedLogLine struct {
	at    int64
	runID string
	meta  worker.LogMeta
	text  string
}

func (a *App) sdlcLogsCmd() *cobra.Command {
	var f logFilters
	c := &cobra.Command{Use: "logs <run-id>", Short: "read saved agent output", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return a.sdlcLogs(cmd.Context(), args[0], f) }}
	c.Flags().BoolVar(&f.follow, "follow", false, "follow output while the run is active")
	c.Flags().StringVar(&f.agent, "agent", "", "filter agent ID")
	c.Flags().StringVar(&f.runtime, "runtime", "", "filter runtime")
	c.Flags().StringVar(&f.invocation, "invocation", "", "filter invocation ID")
	c.Flags().StringVar(&f.stream, "stream", "", "stdout, stderr or decisions")
	c.Flags().BoolVar(&f.raw, "raw", false, "display locally saved original output")
	return c
}

func (a *App) sdlcTree(root string) ([]ledger.Run, error) {
	if !sdlcRunIDRE.MatchString(root) {
		return nil, usagef("invalid run ID")
	}
	entries, err := os.ReadDir(a.sdlcRunsDir())
	if err != nil {
		return nil, err
	}
	runs := map[string]ledger.Run{}
	for _, e := range entries {
		if !e.IsDir() || !sdlcRunIDRE.MatchString(e.Name()) {
			continue
		}
		r, err := ledger.Open(a.sdlcRunsDir(), e.Name()).ReadRun()
		if err == nil {
			runs[r.RunID] = r
		}
	}
	if _, ok := runs[root]; !ok {
		return nil, fmt.Errorf("run %s not found", root)
	}
	var out []ledger.Run
	var visit func(string)
	visit = func(id string) {
		out = append(out, runs[id])
		var children []string
		for _, r := range runs {
			if r.ParentRunID == id {
				children = append(children, r.RunID)
			}
		}
		sort.Strings(children)
		for _, id := range children {
			visit(id)
		}
	}
	visit(root)
	return out, nil
}

func (a *App) sdlcLogs(ctx context.Context, root string, f logFilters) error {
	if f.stream != "" && f.stream != "stdout" && f.stream != "stderr" && f.stream != "decisions" {
		return usagef("--stream must be stdout, stderr or decisions")
	}
	var redactor func(string) (string, error)
	if !f.raw {
		cfg, err := config.Load(a.loadOptions())
		if err != nil {
			return failf("load redaction: %v", err)
		}
		r, err := cfg.Redactor()
		if err != nil {
			return failf("load redaction: %v", err)
		}
		redactor = func(s string) (string, error) { v, err := r.Apply(s); return v.Text, err }
	}
	seen := map[string]string{}
	lineSeen := map[string]int64{}
	decisionSeen := map[string]int{}
	for {
		var combined []displayedLogLine
		runs, err := a.sdlcTree(root)
		if err != nil {
			return failf("%v", err)
		}
		for _, run := range runs {
			if f.stream == "" || f.stream == "decisions" {
				decisions, err := ledger.Open(a.sdlcRunsDir(), run.RunID).ReadDecisions()
				if err != nil {
					return err
				}
				start := decisionSeen[run.RunID]
				if start > len(decisions) {
					start = 0
				}
				decisionSeen[run.RunID] = len(decisions)
				for _, d := range decisions[start:] {
					if f.invocation != "" && d.Invocation != f.invocation {
						continue
					}
					if f.agent != "" && d.Choice != f.agent && d.Trigger != f.agent {
						continue
					}
					if f.runtime != "" && d.Runtime != f.runtime {
						continue
					}
					at, _ := time.Parse(time.RFC3339Nano, d.At)
					value := d.Kind + ": " + d.Choice
					if d.Outcome != "" {
						value += " (" + d.Outcome + ")"
					}
					if d.Detail != "" {
						value += " — " + d.Detail
					}
					if d.Next != "" {
						value += "; next: " + d.Next
					}
					if redactor != nil {
						value, err = redactor(value)
						if err != nil {
							return failf("redact decisions: %v", err)
						}
					}
					combined = append(combined, displayedLogLine{at.UnixNano(), run.RunID, worker.LogMeta{Agent: "decision", Runtime: "jevkit", Invocation: d.Invocation}, value})
				}
			}
			if f.stream == "decisions" {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(a.sdlcRunsDir(), run.RunID, "logs"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				var meta worker.LogMeta
				base := filepath.Join(a.sdlcRunsDir(), run.RunID, "logs")
				data, err := os.ReadFile(filepath.Join(base, entry.Name()))
				if err != nil || json.Unmarshal(data, &meta) != nil {
					continue
				}
				if !sdlcRunIDRE.MatchString(meta.Invocation) {
					continue
				}
				if f.agent != "" && f.agent != meta.Agent || f.runtime != "" && f.runtime != meta.Runtime || f.invocation != "" && f.invocation != meta.Invocation {
					continue
				}
				linesPath := filepath.Join(base, meta.Invocation+".lines.jsonl")
				if rawLines, err := os.ReadFile(linesPath); f.stream == "" && err == nil && len(rawLines) > 0 {
					if lineSeen[linesPath] == 0 {
						if _, err := os.Stat(linesPath + ".truncated"); err == nil {
							combined = append(combined, displayedLogLine{0, run.RunID, meta, "[earlier output truncated]"})
						}
					}
					for _, rawLine := range strings.Split(string(rawLines), "\n") {
						if rawLine == "" {
							continue
						}
						var line worker.LogLine
						if json.Unmarshal([]byte(rawLine), &line) != nil || line.At <= lineSeen[linesPath] {
							continue
						}
						if f.stream != "" && f.stream != line.Stream {
							continue
						}
						value := line.Text
						if redactor != nil {
							value, err = redactor(value)
							if err != nil {
								return failf("redact logs: %v", err)
							}
						}
						combined = append(combined, displayedLogLine{line.At, run.RunID, meta, value})
						if line.At > lineSeen[linesPath] {
							lineSeen[linesPath] = line.At
						}
					}
					continue
				}
				for _, stream := range []string{"stdout", "stderr"} {
					if f.stream != "" && f.stream != stream {
						continue
					}
					path := filepath.Join(base, meta.Invocation+"."+stream)
					data, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					key := path
					current := string(data)
					if redactor != nil {
						const marker = "[earlier output truncated]\n"
						if strings.HasPrefix(current, marker) {
							tail := current[len(marker):]
							if end := strings.IndexByte(tail, '\n'); end >= 0 {
								current = marker + tail[end+1:]
							} else {
								current = marker
							}
						}
						if f.follow {
							if end := strings.LastIndexByte(current, '\n'); end >= 0 {
								current = current[:end+1]
							} else {
								current = ""
							}
						}
						current, err = redactor(current)
						if err != nil {
							return failf("redact logs: %v", err)
						}
					}
					previous := seen[key]
					if current == previous {
						continue
					}
					value := current
					if strings.HasPrefix(current, previous) {
						value = current[len(previous):]
					} else if previous != "" && len(value) > 4096 {
						value = "[log tail rotated]\n" + value[len(value)-4096:]
					}
					seen[key] = current
					for _, line := range strings.Split(strings.TrimSuffix(value, "\n"), "\n") {
						a.outf("[%s %s %s %s] %s\n", run.RunID, meta.Agent, meta.Runtime, meta.Invocation, line)
					}
				}
			}
		}
		sort.SliceStable(combined, func(i, j int) bool { return combined[i].at < combined[j].at })
		for _, line := range combined {
			for _, textLine := range strings.Split(line.text, "\n") {
				a.outf("[%s %s %s %s] %s\n", line.runID, line.meta.Agent, line.meta.Runtime, line.meta.Invocation, textLine)
			}
		}
		if !f.follow {
			return nil
		}
		allStopped := true
		for _, r := range runs {
			if r.Adaptive != nil && r.Adaptive.Stage != "done" && r.Adaptive.Stage != "paused" {
				allStopped = false
			}
		}
		if allStopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}
