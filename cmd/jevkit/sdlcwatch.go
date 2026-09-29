package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JoshJancula/jevkit/internal/redact/config"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
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
	if err == nil {
		return a.sdlcReviewFollowup(ctx, root)
	}
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
		if input, ok := a.Stdin.(*os.File); ok && term.IsTerminal(int(input.Fd())) {
			if old, err := term.MakeRaw(int(input.Fd())); err == nil {
				restore = func() { _ = term.Restore(int(input.Fd()), old) }
			}
		}
		inputBytes = a.sdlcTTYInput()
		// Leave mouse selection to the terminal so users can copy visible text.
		_, _ = fmt.Fprint(stdout, watchTTYEnter())
		defer func() {
			_, _ = fmt.Fprint(stdout, watchTTYLeave())
			if restore != nil {
				restore()
			}
		}()
	}
	last := ""
	selected := -1 // Follow the newest invocation until the user switches agents.
	following := ""
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
	frozen := false
	canRetry := false
	retryAction := ""
	composing := false
	var draft []byte
	finished := false
	tree := newSdlcTreeWatcher(root)
	for {
		runs, err := tree.refresh(a)
		if err != nil {
			return failf("%v", err)
		}
		invocation := watchActiveInvocation(runs)
		if invocation != following {
			following = invocation
			selected, logScroll, detailScroll = -1, 0, 0
			allActivity = false
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
				fmt.Fprintf(&b, "Paused. r: %s; g: type guidance for the next agent, then retry; f/s/c: retry in a new/same/compacted session; q: leave paused.\n", retryAction)
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
				focusInvocation: invocation,
				paused:          paused, canRetry: canRetry, driving: driveDone != nil, frozen: frozen,
				retryAction: retryAction, composing: composing, draft: string(draft),
				cause:       a.sdlcPauseCause(root, driveErr),
				inputTokens: usage.InputTokens, outputTokens: usage.OutputTokens,
				unknownInput: usage.UnknownInput, unknownOutput: usage.UnknownOutput,
				cost: usage.SuppliedCostUSD,
			}, width, height)
			if view != last && (!frozen || last == "") {
				a.outf("\x1b[H%s\x1b[J", view)
				last = view
			}
		} else if view != last {
			a.outf("%s", view)
			last = view
		}
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
			canRetry = paused && (pauseRetryable(fresh.Adaptive.Outcome) || fresh.Adaptive.PendingDecision != "" || fresh.Adaptive.Outcome == "automatic-child-paused")
			retryAction = ""
			if canRetry {
				retryAction = sdlcRetryAction(fresh)
			}
			if !paused || !tty || fresh.Adaptive.Outcome == "plan-approval-required" || fresh.Adaptive.Outcome == "child-plan-approval-required" {
				finished = true
			}
		case b, open := <-inputBytes:
			if !open {
				inputBytes = nil
				if paused || drive == nil {
					return driveErr
				}
				continue
			}
			if composing {
				// The guidance box takes raw bytes so p, q and r are ordinary text.
				switch b {
				case 3: // Ctrl-C
					return context.Canceled
				case 0x1b:
					composing, draft = false, nil
				case '\r', '\n':
					text := strings.TrimSpace(string(draft))
					composing, draft = false, nil
					if text == "" || !paused || !canRetry || retry == nil {
						continue
					}
					if err := a.sdlcSetOperatorGuidance(root, text); err != nil {
						driveErr = err
						continue
					}
					paused = false
					startDrive(func() error { return retry("auto") })
				case 0x7f, 0x08:
					if len(draft) > 0 {
						_, size := utf8.DecodeLastRune(draft)
						draft = draft[:len(draft)-size]
					}
				case 0x15: // Ctrl-U
					draft = draft[:0]
				default:
					if (b >= 0x20 || b == '\t') && len(draft) < worker.MaxPlanFeedbackInlineBytes {
						if b == '\t' {
							b = ' '
						}
						draft = append(draft, b)
					}
				}
				last = ""
				continue
			}
			event, complete := inputParser.feed(b)
			if !complete {
				continue
			}
			key := event.key
			if key == 3 { // Ctrl-C is a byte while the terminal is in raw mode.
				if drive == nil || driveDone == nil {
					return nil
				}
				return context.Canceled
			}
			if strategy := watchRetryStrategy(key); strategy != "" && paused && canRetry && retry != nil {
				paused = false
				startDrive(func() error { return retry(strategy) })
				continue
			}
			if (key == 'g' || key == 'G') && paused && canRetry && retry != nil {
				composing, draft = true, nil
				frozen = false
				last = ""
				continue
			}
			switch key {
			case 'p', 'P':
				frozen = !frozen
				last = ""
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
			case watchKeyHistoryOlder, watchKeyHistoryNewer, watchKeyHistoryPageOlder, watchKeyHistoryPageNewer, 'j', 'J', 'k', 'K':
				logScroll, detailScroll = watchScrollOffsets(key, logScroll, detailScroll)
			case 't', 'T':
				toolView = !toolView
				detailScroll = 0
			}
		case <-time.After(time.Second):
		}
	}
}

// The most recent pending invocation drives the expanded agent view. Invocation
// IDs include a timestamp, and the run tree may contain concurrent assignments.
func watchActiveInvocation(runs []ledger.Run) string {
	latest := ""
	for _, run := range runs {
		if run.Adaptive == nil {
			continue
		}
		for _, assignment := range run.Adaptive.Pending() {
			if assignment.InvocationID > latest {
				latest = assignment.InvocationID
			}
		}
	}
	return latest
}

func watchScrollOffsets(key byte, history, detail int) (int, int) {
	switch key {
	case watchKeyHistoryOlder:
		return min(history+1, 200), 0
	case watchKeyHistoryNewer:
		return max(0, history-1), 0
	case watchKeyHistoryPageOlder:
		return min(history+5, 200), 0
	case watchKeyHistoryPageNewer:
		return max(0, history-5), 0
	case 'j', 'J':
		return history, min(detail+1, 200)
	case 'k', 'K':
		return history, max(0, detail-1)
	default:
		return history, detail
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
	key byte
}

const (
	watchKeyHistoryOlder byte = 1 + iota
	watchKeyHistoryNewer
	watchKeyHistoryPageOlder
	watchKeyHistoryPageNewer
)

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
			return watchInputEvent{key: watchKeyHistoryOlder}, true
		case "B":
			return watchInputEvent{key: watchKeyHistoryNewer}, true
		case "5~":
			return watchInputEvent{key: watchKeyHistoryPageOlder}, true
		case "6~":
			return watchInputEvent{key: watchKeyHistoryPageNewer}, true
		}
		return watchInputEvent{}, false
	}
	return watchInputEvent{}, false
}

func watchTTYEnter() string {
	return "\x1b[?1000l\x1b[?1006l\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J"
}

func watchTTYLeave() string {
	return "\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[?1049l"
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

// sdlcRetryAction describes what resuming a paused run does, so the operator
// is not guessing what "retry" means for this particular pause.
func sdlcRetryAction(run ledger.Run) string {
	st := run.Adaptive
	if st == nil {
		return "resume the run"
	}
	switch st.Outcome {
	case adaptive.OutcomeVerificationEnvironment:
		return "re-run verification (no revision is spent)"
	case "review-workspace-drift", "review-recovery-invalid":
		return "reassess the current workspace"
	case "automatic-child-paused":
		return "resume the parent after the child run"
	}
	if st.PendingDecision != "" {
		return "retry the pending " + st.PendingDecision + " decision"
	}
	switch role := failedPauseRole(st.Outcome); role {
	case "":
		return "resume the run"
	case "implementer":
		if run.Verification != nil && !run.Verification.AllPassed && run.Verification.CandidateFingerprint == st.DiffRevision {
			return "send the implementer back with the verification failures"
		}
		return "retry the implementer"
	default:
		return "retry the " + role
	}
}
