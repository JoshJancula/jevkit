package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OWNER/jevkit/internal/redact/config"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"golang.org/x/term"
)

// One input pump survives repeated live views and the cooked-mode approval
// prompt. Otherwise the view's old stdin reader can consume the user's answer.
func (a *App) sdlcTTYInput() <-chan byte {
	if a.sdlcInputBytes == nil {
		a.sdlcInputBytes = make(chan byte, 128)
		go func() {
			defer close(a.sdlcInputBytes)
			var b [1]byte
			for {
				n, err := a.Stdin.Read(b[:])
				if n > 0 {
					a.sdlcInputBytes <- b[0]
				}
				if err != nil || n == 0 {
					return
				}
			}
		}()
	}
	return a.sdlcInputBytes
}

const (
	sdlcKeyUp = 256 + iota
	sdlcKeyDown
	sdlcKeyLeft
	sdlcKeyRight
	sdlcKeyEscape
)

func (a *App) sdlcReadKey(ctx context.Context) (int, error) {
	input := a.sdlcTTYInput()
	read := func(timeout time.Duration) (byte, bool, error) {
		if a.sdlcHasPending {
			a.sdlcHasPending = false
			return a.sdlcPendingByte, true, nil
		}
		var timer <-chan time.Time
		if timeout > 0 {
			t := time.NewTimer(timeout)
			defer t.Stop()
			timer = t.C
		}
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case b, ok := <-input:
			if !ok {
				return 0, false, io.EOF
			}
			return b, true, nil
		case <-timer:
			return 0, false, nil
		}
	}
	b, _, err := read(0)
	if err != nil || b != 0x1b {
		return int(b), err
	}
	lead, ok, err := read(80 * time.Millisecond)
	if err != nil {
		return 0, err
	}
	if !ok {
		return sdlcKeyEscape, nil
	}
	if lead != '[' && lead != 'O' {
		a.sdlcPendingByte = lead
		a.sdlcHasPending = true
		return sdlcKeyEscape, nil
	}
	for i := 0; i < 16; i++ {
		part, ok, err := read(80 * time.Millisecond)
		if err != nil {
			return 0, err
		}
		if !ok {
			return sdlcKeyEscape, nil
		}
		switch part {
		case 'A':
			return sdlcKeyUp, nil
		case 'B':
			return sdlcKeyDown, nil
		case 'C':
			return sdlcKeyRight, nil
		case 'D':
			return sdlcKeyLeft, nil
		}
		if part >= '@' && part <= '~' {
			break
		}
	}
	return 0, nil
}

func (a *App) sdlcApprovalTarget(rootID string) (string, ledger.Run, error) {
	run, err := ledger.Open(a.sdlcRunsDir(), rootID).ReadRun()
	if err != nil {
		return "", ledger.Run{}, err
	}
	if run.Adaptive == nil {
		return "", ledger.Run{}, failf("run %s has no adaptive state", rootID)
	}
	switch run.Adaptive.Outcome {
	case "plan-approval-required":
		return rootID, run, nil
	case "child-plan-approval-required":
		if run.StageFlow == nil || run.StageFlow.ChildRunID == "" {
			return "", ledger.Run{}, failf("run %s has no child plan", rootID)
		}
		childID := run.StageFlow.ChildRunID
		child, err := ledger.Open(a.sdlcRunsDir(), childID).ReadRun()
		return childID, child, err
	default:
		return "", ledger.Run{}, nil
	}
}

func (a *App) sdlcShowPlan(rootID string) error {
	targetID, run, err := a.sdlcApprovalTarget(rootID)
	if err != nil {
		return err
	}
	if targetID == "" || run.Adaptive == nil || run.Adaptive.PlanRevision == "" {
		return failf("run %s has no plan awaiting approval", rootID)
	}
	if run.WorkDir != "" {
		old := a.WorkDir
		a.WorkDir = run.WorkDir
		defer func() { a.WorkDir = old }()
	}
	store := ledger.Open(a.sdlcRunsDir(), targetID)
	plan, err := store.ReadArtifact("plan.md")
	if err != nil {
		return failf("read plan.md: %v", err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(plan)) != run.Adaptive.PlanRevision {
		return failf("saved plan.md no longer matches the planned revision")
	}
	cfg, err := config.Load(a.loadOptions())
	if err != nil {
		return err
	}
	redactor, err := cfg.Redactor()
	if err != nil {
		return err
	}
	clean, err := redactor.Apply(string(plan))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "\n%s\n", a.styled(a.Stdout, ansiCyan, "PLAN FOR APPROVAL"))
	fmt.Fprintf(a.Stdout, "  Run:  %s\n  File: %s/artifacts/plan.md\n\n", ttyClean(targetID), store.Dir)
	fmt.Fprintln(a.Stdout, a.styled(a.Stdout, ansiCyan, strings.Repeat("─", 72)))
	for _, line := range strings.Split(clean.Text, "\n") {
		fmt.Fprintln(a.Stdout, ttySafeLine(line))
	}
	fmt.Fprintln(a.Stdout, a.styled(a.Stdout, ansiCyan, strings.Repeat("─", 72)))
	return nil
}

func (a *App) sdlcAskPlanDecision(ctx context.Context, rootID string) (string, string, error) {
	if err := a.sdlcShowPlan(rootID); err != nil {
		return "", "", err
	}
	if input, ok := a.Stdin.(*os.File); ok && term.IsTerminal(int(input.Fd())) {
		old, err := term.MakeRaw(int(input.Fd()))
		if err != nil {
			return "", "", err
		}
		defer term.Restore(int(input.Fd()), old)
	}
	selected := 0
	fmt.Fprint(a.Stdout, "\r\n")
	a.sdlcRenderReviewMenu(selected, false)
	for {
		key, err := a.sdlcReadKey(ctx)
		if err != nil {
			return "", "", err
		}
		switch key {
		case sdlcKeyUp, sdlcKeyLeft:
			selected = (selected + 2) % 3
			a.sdlcRenderReviewMenu(selected, true)
		case sdlcKeyDown, sdlcKeyRight:
			selected = (selected + 1) % 3
			a.sdlcRenderReviewMenu(selected, true)
		case sdlcKeyEscape, 3, 4:
			a.sdlcClearReviewMenu()
			return "leave", "", nil
		case '\r', '\n':
			switch selected {
			case 0:
				a.sdlcClearReviewMenu()
				return "approve", "", nil
			case 1:
				feedback, err := a.sdlcEditPlanFeedback(ctx)
				if err != nil {
					return "", "", err
				}
				if feedback != "" {
					a.sdlcClearReviewMenu()
					return "changes", feedback, nil
				}
			case 2:
				a.sdlcClearReviewMenu()
				return "leave", "", nil
			}
		}
	}
}

func (a *App) sdlcClearReviewMenu() {
	fmt.Fprint(a.Stdout, "\r\x1b[4A\x1b[J")
}

func (a *App) sdlcRenderReviewMenu(selected int, redraw bool) {
	if redraw {
		fmt.Fprint(a.Stdout, "\r\x1b[4A\x1b[J")
	}
	fmt.Fprint(a.Stdout, ttyFit(a.styled(a.Stdout, ansiCyan, "Review the plan  ↑/↓ to move · Enter to select"), a.sdlcFeedbackBoxWidth()), "\r\n")
	labels := []string{"Approve and continue", "Request changes", "Leave paused"}
	for i, label := range labels {
		marker := "  "
		if i == selected {
			marker = "› "
			label = a.styled(a.Stdout, []string{ansiGreen, ansiYellow, ansiBold}[i], label)
		}
		fmt.Fprintf(a.Stdout, "  %s%s\r\n", marker, label)
	}
}

func (a *App) sdlcFeedbackBoxWidth() int {
	width := 70
	if out, ok := a.Stdout.(*os.File); ok && term.IsTerminal(int(out.Fd())) {
		if columns, _, err := term.GetSize(int(out.Fd())); err == nil {
			width = min(width, max(12, columns-2))
		}
	}
	return width
}

// The cursor stays inside the field; each keystroke repaints the same four rows.
func (a *App) sdlcRenderFeedbackBox(feedback string, width int, redraw, empty bool) {
	if redraw {
		fmt.Fprint(a.Stdout, "\r\x1b[1A\x1b[J")
	}
	inner := width - 4
	visible := []rune(ttySafeLine(feedback))
	if len(visible) > inner {
		visible = visible[len(visible)-inner:]
	}
	label := " Change request "
	if width-2 < len([]rune(label)) {
		label = " Feedback "
	}
	border := width - 2 - len([]rune(label))
	fmt.Fprintf(a.Stdout, "┌%s%s┐\r\n", label, strings.Repeat("─", max(0, border)))
	fmt.Fprintf(a.Stdout, "│ %s%s │\r\n", string(visible), strings.Repeat(" ", max(0, inner-len(visible))))
	fmt.Fprintf(a.Stdout, "└%s┘\r\n", strings.Repeat("─", width-2))
	hint := "Enter to send · Esc to go back"
	if empty {
		hint = "Type a change request before sending · Esc to go back"
	}
	fmt.Fprint(a.Stdout, ttyFit(a.styled(a.Stdout, ansiCyan, hint), width), "\r\n")
	fmt.Fprintf(a.Stdout, "\x1b[3A\r\x1b[%dC", 2+len(visible))
}

func (a *App) sdlcEditPlanFeedback(ctx context.Context) (string, error) {
	width := a.sdlcFeedbackBoxWidth()
	var feedback string
	a.sdlcRenderFeedbackBox(feedback, width, false, false)
	for {
		key, err := a.sdlcReadKey(ctx)
		if err != nil {
			return "", err
		}
		switch key {
		case sdlcKeyEscape, 3, 4:
			fmt.Fprint(a.Stdout, "\r\x1b[1A\x1b[J")
			return "", nil
		case '\r', '\n':
			if clean := strings.TrimSpace(feedback); clean != "" {
				fmt.Fprint(a.Stdout, "\r\x1b[1A\x1b[J")
				return clean, nil
			}
			a.sdlcRenderFeedbackBox(feedback, width, true, true)
		case 0x7f, 0x08:
			if feedback != "" {
				_, size := utf8.DecodeLastRuneInString(feedback)
				feedback = feedback[:len(feedback)-size]
				a.sdlcRenderFeedbackBox(feedback, width, true, false)
			}
		default:
			if key >= ' ' && key <= 255 && key != 0x7f {
				if len(feedback) < 16*1024 {
					feedback += string([]byte{byte(key)})
					a.sdlcRenderFeedbackBox(feedback, width, true, false)
				}
			}
		}
	}
}

func (a *App) sdlcInteractiveDrive(ctx context.Context, rootID string, drive func() error, oneStep bool) error {
	var driveErr error
	for {
		targetID, _, err := a.sdlcApprovalTarget(rootID)
		if err != nil {
			driveErr = err
			break
		}
		if targetID == "" {
			driveErr = a.sdlcWatchLoop(ctx, rootID, drive, func(strategy string) error {
				return a.sdlcDashboardRetry(ctx, rootID, strategy)
			})
			if driveErr != nil {
				break
			}
			targetID, _, err = a.sdlcApprovalTarget(rootID)
			if err != nil {
				driveErr = err
				break
			}
			if targetID == "" {
				break
			}
		}
		choice, feedback, err := a.sdlcAskPlanDecision(ctx, rootID)
		if err == io.EOF {
			break
		}
		if err != nil {
			driveErr = err
			break
		}
		switch choice {
		case "approve":
			driveErr = a.sdlcApprovePlan(rootID)
		case "changes":
			driveErr = a.sdlcRequestPlanChanges(rootID, feedback)
		case "leave":
			goto done
		}
		if driveErr != nil {
			break
		}
		if oneStep {
			drive = func() error { return a.sdlcDashboardWorker().sdlcDrive(ctx, rootID) }
		} else {
			drive = func() error { return a.sdlcDashboardWorker().sdlcDriveUntilDone(ctx, rootID) }
		}
	}
done:
	a.sdlcFinalSummary(a.Stdout, rootID, driveErr)
	return driveErr
}

func (a *App) sdlcRequestPlanChanges(rootID, feedback string) error {
	targetID, _, err := a.sdlcApprovalTarget(rootID)
	if err != nil {
		return err
	}
	if targetID == "" || strings.TrimSpace(feedback) == "" {
		return usagef("a pending plan and a change request are required")
	}
	store := ledger.Open(a.sdlcRunsDir(), targetID)
	if err := store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil || run.Adaptive.Stage != adaptive.Paused || run.Adaptive.Outcome != "plan-approval-required" || len(run.Adaptive.Assignments) != 0 {
			return usagef("run %s is not awaiting plan feedback", targetID)
		}
		plan, err := store.ReadArtifact("plan.md")
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(plan)) != run.Adaptive.PlanRevision {
			return failf("saved plan.md changed during review; start a new run")
		}
		if run.StageFlow != nil {
			planner := ""
			for i := len(run.StageFlow.Transitions) - 1; i >= 0; i-- {
				stage, ok := run.StageFlow.Workflow.StageByID(run.StageFlow.Transitions[i].Stage)
				if ok && stage.Work != nil && stage.Work.Role == "planner" {
					planner = stage.ID
					break
				}
			}
			if planner == "" {
				return failf("workflow %s has no planner stage to revise the plan", run.Workflow)
			}
			run.StageFlow.Current = planner
		}
		run.PlanFeedback = feedback
		run.ApprovedPlanRevision = ""
		st := run.Adaptive
		st.Stage, st.Outcome = adaptive.Planning, ""
		st.PendingDecision, st.PendingPhase, st.PendingFocus, st.PendingReason = "", "", "", ""
		st.SpecialistQueue, st.SpecialistReviews, st.SpecialistDecisions = nil, nil, nil
		st.AfterSpecialists = ""
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		return a.recordDecision(store, ledger.Decision{RunID: targetID, Kind: "plan-approval", Stage: adaptive.Planning,
			Trigger: st.PlanRevision, Choice: "changes-requested", Detail: feedback, Next: "revise plan"})
	}); err != nil {
		return err
	}
	if targetID == rootID {
		return nil
	}
	parentStore := ledger.Open(a.sdlcRunsDir(), rootID)
	return parentStore.WithRunLock(func() error {
		parent, err := parentStore.ReadRun()
		if err != nil {
			return err
		}
		if parent.Adaptive == nil || parent.Adaptive.Outcome != "child-plan-approval-required" || parent.StageFlow == nil || parent.StageFlow.ChildRunID != targetID {
			return failf("parent run %s changed while requesting plan changes", rootID)
		}
		parent.Adaptive.Stage, parent.Adaptive.Outcome = "spawn", ""
		parent.Adaptive.PendingDecision, parent.Adaptive.PendingPhase, parent.Adaptive.PendingReason = "", "", ""
		parent.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		return parentStore.WriteRun(parent)
	})
}
