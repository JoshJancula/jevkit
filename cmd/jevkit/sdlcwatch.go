package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/redact/config"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *App) sdlcWatchCmd() *cobra.Command {
	return &cobra.Command{Use: "watch <run-id>", Short: "attach to a read-only run view", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return a.sdlcWatch(cmd.Context(), args[0]) }}
}

func (a *App) sdlcWatch(ctx context.Context, root string) error {
	return a.sdlcWatchLoop(ctx, root, nil, nil)
}

// sdlcWatchDrive keeps the saved, read-only view in front of an active drive.
// Recovery is an explicit user action after the drive has paused.
func (a *App) sdlcWatchDrive(ctx context.Context, root string, drive func() error, retry func(string) error) error {
	err := a.sdlcWatchLoop(ctx, root, drive, retry)
	a.sdlcFinalSummary(a.Stdout, root, err)
	return err
}

func (a *App) sdlcWatchLoop(ctx context.Context, root string, drive func() error, retry func(string) error) error {
	stdout, ok := a.Stdout.(*os.File)
	tty := false
	if ok {
		tty = term.IsTerminal(int(stdout.Fd()))
	}
	var inputBytes <-chan byte
	var inputParser watchInputParser
	if tty {
		var restore func()
		mouse := false
		if input, ok := a.Stdin.(*os.File); ok && term.IsTerminal(int(input.Fd())) {
			if old, err := term.MakeRaw(int(input.Fd())); err == nil {
				restore = func() { _ = term.Restore(int(input.Fd()), old) }
				mouse = true
			}
		}
		inputBytes = a.sdlcTTYInput()
		// Keep refreshes out of the shell's scrollback. SGR mouse tracking lets
		// a wheel over the agent pane navigate its saved activity.
		_, _ = fmt.Fprint(stdout, watchTTYEnter(mouse))
		defer func() {
			_, _ = fmt.Fprint(stdout, watchTTYLeave(mouse))
			if restore != nil {
				restore()
			}
		}()
	}
	last := ""
	selected := -1 // Follow the newest invocation until the user switches agents.
	logScroll := 0
	detailScroll := 0
	details, showLogs, allActivity, toolView, help := false, true, false, false, true
	var driveDone chan error
	startDrive := func(fn func() error) {
		driveDone = make(chan error, 1)
		go func() { driveDone <- fn() }()
	}
	if drive != nil {
		startDrive(drive)
	}
	var driveErr error
	paused := false
	canRetry := false
	finished := false
	for {
		runs, err := a.sdlcTree(root)
		if err != nil {
			return failf("%v", err)
		}
		var b strings.Builder
		if drive != nil {
			fmt.Fprintf(&b, "SDLC run %s  (n next agent, a all activity, d details, l log tail)\n", root)
		} else {
			fmt.Fprintf(&b, "SDLC run %s  (q detach, n next agent, a all activity, d details, l log tail)\n", root)
		}
		if len(runs) > 0 && runs[0].TreeUsage != nil && runs[0].Adaptive != nil {
			u := runs[0].TreeUsage
			st := runs[0].Adaptive
			fmt.Fprintf(&b, "Root budget: %d assignments left, %d revisions left, %d child runs left", max(0, st.MaxAssignments-u.Assignments), max(0, st.MaxRevisions-u.Revisions), max(0, sdlcMaxChildRuns-u.ChildRuns))
			if policy, _, err := a.sdlcEnrollment(); err == nil {
				if remaining, err := a.treeRemaining(runs[0], policy); err == nil {
					fmt.Fprintf(&b, ", %s left", remaining.Round(time.Second))
				}
			}
			fmt.Fprintln(&b)
		}
		active := false
		usage := aggregateRuntime(runs).Totals
		for _, r := range runs {
			stage := "unknown"
			outcome := ""
			assignments := 0
			budget := ""
			if r.Adaptive != nil {
				stage = r.Adaptive.Stage
				outcome = r.Adaptive.Outcome
				assignments = len(r.Adaptive.Assignments)
				budget = fmt.Sprintf("%d/%d assignments, %d/%d revisions", r.Adaptive.AssignmentCount, r.Adaptive.MaxAssignments, r.Adaptive.RevisionCount, r.Adaptive.MaxRevisions)
			}
			if stage != "done" && stage != "paused" {
				active = true
			}
			shownStage := stage
			if tty {
				shownStage = a.styled(a.Stdout, ansiCyan, stage)
			}
			fmt.Fprintf(&b, "%s%s: %s %s  %s\n", strings.Repeat("  ", r.Depth), r.RunID, shownStage, outcome, budget)
			if r.Adaptive != nil && assignments > 0 {
				for _, v := range r.Adaptive.Pending() {
					fmt.Fprintf(&b, "%s  %s via %s (%s)\n", strings.Repeat("  ", r.Depth), v.AgentID, v.Runtime, v.Role)
				}
			}
		}
		fmt.Fprintf(&b, "Measured agent tokens: %s input (%d unknown), %s output (%d unknown)\n", formatInt(int64(usage.InputTokens)), usage.UnknownInput, formatInt(int64(usage.OutputTokens)), usage.UnknownOutput)
		if usage.SuppliedCostUSD != nil {
			fmt.Fprintf(&b, "Supplied cost: $%.4f\n", *usage.SuppliedCostUSD)
		}
		var events []ledger.Event
		for _, r := range runs {
			items, err := ledger.Open(a.sdlcRunsDir(), r.RunID).ReadEvents()
			if err == nil {
				events = append(events, items...)
			}
		}
		sort.SliceStable(events, func(i, j int) bool { return events[i].At < events[j].At })
		if len(events) > 0 {
			fmt.Fprintln(&b, "Recent events:")
			start := len(events) - 3
			if start < 0 {
				start = 0
			}
			for _, e := range events[start:] {
				fmt.Fprintf(&b, "  %s %s %s", e.At, e.RunID, e.Stage)
				if e.Agent != "" {
					fmt.Fprintf(&b, " %s via %s", e.Agent, e.Runtime)
				}
				if e.Outcome != "" {
					fmt.Fprintf(&b, " (%s)", e.Outcome)
				}
				if e.Reason != "" {
					fmt.Fprintf(&b, " — %s", e.Reason)
				}
				fmt.Fprintln(&b)
			}
		}
		var decisions []ledger.Decision
		for _, r := range runs {
			items, err := ledger.Open(a.sdlcRunsDir(), r.RunID).ReadDecisions()
			if err == nil {
				decisions = append(decisions, items...)
			}
		}
		sort.SliceStable(decisions, func(i, j int) bool { return decisions[i].At < decisions[j].At })
		if len(decisions) > 0 {
			fmt.Fprintln(&b, "Decisions (recorded evidence; Jev does not provide prose reasoning):")
			start := max(0, len(decisions)-4)
			for _, d := range decisions[start:] {
				fmt.Fprintf(&b, "  %s %s: %s", d.At, d.Kind, d.Choice)
				if d.Outcome != "" {
					fmt.Fprintf(&b, " (%s)", d.Outcome)
				}
				if d.Next != "" {
					fmt.Fprintf(&b, " → %s", d.Next)
				}
				fmt.Fprintln(&b)
				if details {
					fmt.Fprintf(&b, "    trigger: %s; detail: %s\n", d.Trigger, d.Detail)
					for _, c := range d.Candidates {
						fmt.Fprintf(&b, "    candidate %s %s\n", c.ID, c.Reason)
					}
				}
			}
		}
		if !tty {
			agents := a.sdlcWatchAgents(runs)
			if len(agents) > 0 {
				latest := agents[len(agents)-1]
				if len(latest.detail) > 0 {
					fmt.Fprintf(&b, "Latest agent text (%s):\n", latest.meta.Agent)
					for _, line := range latest.detail[max(0, len(latest.detail)-12):] {
						fmt.Fprintf(&b, "  %s\n", line)
					}
				}
			}
		}
		if drive != nil && driveDone != nil {
			fmt.Fprintln(&b, "Agent run in progress. Logs and status refresh each second.")
		}
		if paused {
			if canRetry {
				fmt.Fprintln(&b, "Paused. Choose: r auto, f fresh, s resume session, c compact session, l logs, q leave paused.")
			} else {
				fmt.Fprintln(&b, "Paused. Review the cause and logs; press q to leave this run paused.")
			}
			if cause := a.sdlcPauseCause(root, driveErr); cause != "" {
				fmt.Fprintf(&b, "Cause: %s\n", a.sdlcRedactedDisplay(cause))
			}
		}
		view := b.String()
		if tty {
			width, height, err := term.GetSize(int(stdout.Fd()))
			if err != nil || width < 32 {
				width = 80
			}
			if err != nil || height < 8 {
				height = 24
			}
			view = a.sdlcTTYView(runs, decisions, watchTTYState{
				selected: selected, scroll: logScroll, detailScroll: detailScroll, details: details, logs: showLogs, all: allActivity, toolView: toolView, help: help,
				paused: paused, canRetry: canRetry, driving: driveDone != nil,
				cause:       a.sdlcPauseCause(root, driveErr),
				inputTokens: usage.InputTokens, outputTokens: usage.OutputTokens,
				unknownInput: usage.UnknownInput, unknownOutput: usage.UnknownOutput,
				cost: usage.SuppliedCostUSD,
			}, width, height)
			a.outf("\x1b[H%s\x1b[J", view)
		} else if view != last {
			a.outf("%s", view)
		}
		last = view
		if finished {
			return driveErr
		}
		if drive == nil && !tty && !active {
			return nil
		}
		select {
		case <-ctx.Done():
			if drive != nil {
				return ctx.Err()
			}
			return nil
		case err := <-driveDone:
			driveDone = nil
			driveErr = err
			fresh, readErr := ledger.Open(a.sdlcRunsDir(), root).ReadRun()
			if readErr != nil {
				return readErr
			}
			paused = fresh.Adaptive != nil && fresh.Adaptive.Stage == "paused"
			canRetry = paused && (failedPauseRole(fresh.Adaptive.Outcome) != "" || fresh.Adaptive.PendingDecision != "" || fresh.Adaptive.Outcome == "automatic-child-paused" || fresh.Adaptive.Outcome == "review-workspace-drift" || fresh.Adaptive.Outcome == "review-recovery-invalid")
			if !paused || !tty || fresh.Adaptive.Outcome == "plan-approval-required" || fresh.Adaptive.Outcome == "child-plan-approval-required" {
				finished = true
			}
		case b, open := <-inputBytes:
			if !open {
				inputBytes = nil
				continue
			}
			event, complete := inputParser.feed(b)
			if !complete {
				continue
			}
			key := event.key
			if event.mouse {
				top, bottom := watchAgentPaneRows(view)
				if event.row < top || event.row > bottom {
					continue
				}
			}
			if strategy := watchRetryStrategy(key); strategy != "" && paused && canRetry && retry != nil {
				paused = false
				startDrive(func() error { return retry(strategy) })
				continue
			}
			switch key {
			case 'q', 'Q':
				if drive == nil {
					return nil
				}
				if paused {
					return driveErr
				}
			case 'n', 'N':
				logScroll = 0
				detailScroll = 0
				if selected < 0 {
					selected = 0
				} else {
					selected++
				}
			case '?', 'h', 'H':
				help = !help
			case 'd', 'D':
				details = !details
			case 'l', 'L':
				showLogs = !showLogs
			case 'a', 'A':
				allActivity = !allActivity
				logScroll = 0
				detailScroll = 0
			case 'j':
				logScroll = min(logScroll+1, 200)
				detailScroll = 0
			case 'u':
				logScroll = min(logScroll+5, 200)
				detailScroll = 0
			case 'k':
				logScroll = max(0, logScroll-1)
				detailScroll = 0
			case 'v':
				logScroll = max(0, logScroll-5)
				detailScroll = 0
			case 'J':
				detailScroll = min(detailScroll+1, 200)
			case 'K':
				detailScroll = max(0, detailScroll-1)
			case 't', 'T':
				toolView = !toolView
				detailScroll = 0
			}
		case <-time.After(time.Second):
		}
	}
}

func watchRetryStrategy(key byte) string {
	switch key {
	case 'r', 'R':
		return "auto"
	case 'f', 'F':
		return "fresh"
	case 's', 'S':
		return "resume"
	case 'c', 'C':
		return "compact"
	default:
		return ""
	}
}

type watchInputEvent struct {
	key   byte
	row   int
	mouse bool
}

type watchInputParser struct {
	state byte
	csi   []byte
}

func (p *watchInputParser) feed(b byte) (watchInputEvent, bool) {
	switch p.state {
	case 0:
		if b == 0x1b {
			p.state = 1
			return watchInputEvent{}, false
		}
		return watchInputEvent{key: b}, true
	case 1:
		p.state = 0
		if b == '[' {
			p.state = 2
			p.csi = p.csi[:0]
		}
		return watchInputEvent{}, false
	case 2:
		if len(p.csi) >= 32 {
			p.state = 0
			return watchInputEvent{}, false
		}
		p.csi = append(p.csi, b)
		if b < '@' || b > '~' {
			return watchInputEvent{}, false
		}
		p.state = 0
		seq := string(p.csi)
		switch seq {
		case "A":
			return watchInputEvent{key: 'j'}, true
		case "B":
			return watchInputEvent{key: 'k'}, true
		case "5~":
			return watchInputEvent{key: 'u'}, true
		case "6~":
			return watchInputEvent{key: 'v'}, true
		}
		if !strings.HasPrefix(seq, "<") || !strings.HasSuffix(seq, "M") {
			return watchInputEvent{}, false
		}
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "<"), "M"), ";")
		if len(parts) != 3 {
			return watchInputEvent{}, false
		}
		button, errButton := strconv.Atoi(parts[0])
		row, errRow := strconv.Atoi(parts[2])
		if errButton != nil || errRow != nil || button&64 == 0 {
			return watchInputEvent{}, false
		}
		if button&1 == 0 {
			return watchInputEvent{key: 'j', row: row, mouse: true}, true
		}
		return watchInputEvent{key: 'k', row: row, mouse: true}, true
	}
	return watchInputEvent{}, false
}

func watchTTYEnter(mouse bool) string {
	sequence := "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J"
	if mouse {
		sequence += "\x1b[?1000h\x1b[?1006h"
	}
	return sequence
}

func watchTTYLeave(mouse bool) string {
	sequence := ""
	if mouse {
		sequence = "\x1b[?1006l\x1b[?1000l"
	}
	return sequence + "\x1b[?25h\x1b[?1049l"
}

func watchAgentPaneRows(view string) (top, bottom int) {
	for i, line := range strings.Split(view, "\r\n") {
		line = ansiSGR.ReplaceAllString(line, "")
		if strings.HasPrefix(line, "┌─ AGENT") {
			top = i + 1
		} else if top != 0 && strings.HasPrefix(line, "└") {
			bottom = i + 1
			break
		}
	}
	return top, bottom
}

func (a *App) sdlcPauseCause(root string, driveErr error) string {
	if run, err := ledger.Open(a.sdlcRunsDir(), root).ReadRun(); err == nil && run.Adaptive != nil && run.Adaptive.Stage == "paused" && run.Adaptive.PendingReason != "" {
		return run.Adaptive.PendingReason
	}
	if decisions, err := ledger.Open(a.sdlcRunsDir(), root).ReadDecisions(); err == nil {
		latestPause := -1
		for i := len(decisions) - 1; i >= 0; i-- {
			if decisions[i].Kind == "stage-transition" && decisions[i].Choice == "paused" {
				latestPause = i
				break
			}
		}
		for i := latestPause - 1; i >= 0; i-- {
			if decisions[i].Kind == "stage-transition" || decisions[i].Kind == "retry" {
				break
			}
			if decisions[i].Kind == "invocation-outcome" && decisions[i].Outcome == "paused" && decisions[i].Detail != "" {
				return decisions[i].Detail
			}
		}
	}
	if driveErr != nil {
		return driveErr.Error()
	}
	return ""
}

func (a *App) sdlcRedactedDisplay(value string) string {
	cfg, err := config.Load(a.loadOptions())
	if err != nil {
		return "saved error unavailable; inspect the run logs"
	}
	redactor, err := cfg.Redactor()
	if err != nil {
		return "saved error unavailable; inspect the run logs"
	}
	clean, err := redactor.Apply(value)
	if err != nil {
		return "saved error unavailable; inspect the run logs"
	}
	return shortProgressText(clean.Text, 300)
}

func sdlcStreamSummary(stream, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if stream == "stderr" {
		return raw
	}
	var event struct {
		Type   string `json:"type"`
		Result string `json:"result"`
		Part   struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"part"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &event) != nil {
		return raw
	}
	if event.Result != "" {
		if reply, err := worker.ParseReply([]byte(event.Result)); err == nil {
			return "result " + reply.Outcome + ": " + reply.Content
		}
		return "result: " + event.Result
	}
	if event.Type == "assistant" {
		for _, part := range event.Message.Content {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				if reply, err := worker.ParseReply([]byte(part.Text)); err == nil {
					return "reported " + reply.Outcome
				}
				return "assistant: " + part.Text
			}
		}
	}
	if event.Part.Type == "text" && strings.TrimSpace(event.Part.Text) != "" {
		if reply, err := worker.ParseReply([]byte(event.Part.Text)); err == nil {
			return "reported " + reply.Outcome
		}
		return "assistant: " + event.Part.Text
	}
	return ""
}
