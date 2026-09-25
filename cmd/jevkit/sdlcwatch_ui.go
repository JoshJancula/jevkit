package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OWNER/jevkit/internal/redact/config"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

type watchTTYState struct {
	selected, scroll, detailScroll, unknownInput, unknownOutput int
	details, logs, all, toolView, help                          bool
	paused, canRetry, driving                                   bool
	cause                                                       string
	inputTokens, outputTokens                                   int64
	cost                                                        *float64
}

type watchAgent struct {
	meta        worker.LogMeta
	dir         string
	workDir     string
	role        string
	active      bool
	current     string
	recent      []string
	history     []watchHistory
	output      []string
	detailTitle string
	detail      []string
	toolTitle   string
	toolDetail  []string
}

type watchHistory struct {
	key         string
	started     bool
	label       string
	detailTitle string
	detail      []string
}

// sdlcTTYView produces one bounded frame. Every visible line is clipped before
// the terminal enters raw mode's CR/LF behavior, so a long log cannot move the
// cursor into the next pane or push the controls off screen.
func (a *App) sdlcTTYView(runs []ledger.Run, decisions []ledger.Decision, state watchTTYState, width, height int) string {
	if width < 32 {
		width = 32
	}
	if height < 8 {
		height = 8
	}
	width = min(width, 180)
	var lines []string
	if len(runs) == 0 {
		return ttyFrame([]string{"SDLC run unavailable"}, width, height)
	}
	root := runs[0]
	stage, outcome := "unknown", ""
	if root.Adaptive != nil {
		stage, outcome = root.Adaptive.Stage, root.Adaptive.Outcome
	}
	status := stage
	if outcome != "" {
		status += " · " + outcome
	}
	shownID := root.RunID
	if width < 75 && len(shownID) > 12 {
		shownID = "…" + shownID[len(shownID)-8:]
	}
	lines = append(lines, "SDLC  "+shownID+"  ·  "+status)
	lines = append(lines, ttyWrapLabel("Task  ", a.sdlcRedactedDisplay(root.Task), width, 2)...)
	if root.Adaptive != nil {
		used := root.Adaptive.AssignmentCount
		revisions := root.Adaptive.RevisionCount
		if root.TreeUsage != nil {
			used = root.TreeUsage.Assignments
			revisions = root.TreeUsage.Revisions
		}
		budget := fmt.Sprintf("Budget  %d/%d assignments · %d/%d revisions", used, root.Adaptive.MaxAssignments, revisions, root.Adaptive.MaxRevisions)
		if policy, _, err := a.sdlcEnrollment(); err == nil {
			if remaining, err := a.treeRemaining(root, policy); err == nil {
				budget += " · " + remaining.Round(time.Second).String() + " left"
			}
		}
		lines = append(lines, budget)
	}
	if len(runs) > 1 {
		var child []string
		for _, run := range runs[1:] {
			if run.Adaptive != nil {
				child = append(child, run.RunID+" "+run.Adaptive.Stage)
			}
		}
		lines = append(lines, "Children  "+strings.Join(child, " · "))
	}
	// Reserve space for the outcome, navigation, and controls before sizing the
	// agent pane. A short terminal must still explain a pause and how to recover.
	var bottom []string
	if len(decisions) > 0 {
		bottom = append(bottom, "DECISIONS  recorded routing evidence")
		count := 2
		if height < 22 {
			count = 1
		}
		for _, decision := range decisions[max(0, len(decisions)-count):] {
			line := "  " + decision.Kind + "  " + decision.Choice
			if decision.Outcome != "" {
				line += " · " + decision.Outcome
			}
			if state.details && decision.Detail != "" {
				line += " — " + decision.Detail
			}
			bottom = append(bottom, "  "+a.sdlcRedactedDisplay(strings.TrimSpace(line)))
		}
	}
	decisionRows := len(bottom)
	usage := fmt.Sprintf("Usage  %d in (%d unknown) · %d out (%d unknown)", state.inputTokens, state.unknownInput, state.outputTokens, state.unknownOutput)
	if state.cost != nil {
		usage += fmt.Sprintf(" · $%.4f supplied", *state.cost)
	}
	bottom = append(bottom, usage)
	if state.paused {
		bottom = append(bottom, "PAUSED  "+a.sdlcRedactedDisplay(state.cause))
	}
	if state.help && height >= 16 {
		bottom = append(bottom, watchHelpLines(state, width, height)...)
	}
	bottom = append(bottom, watchControls(state, width))
	// At small heights, omit secondary information before sacrificing the agent
	// or the pause message. The final line is always the controls.
	if decisionRows > 0 && len(lines)+len(bottom)+5 > height-1 {
		bottom = bottom[decisionRows:]
	}
	if len(lines)+len(bottom)+5 > height-1 && len(lines) > 2 {
		lines = lines[:len(lines)-1]
	}
	available := max(3, height-1-len(lines)-len(bottom)-1)

	agents := a.sdlcWatchAgents(runs)
	if len(agents) == 0 {
		lines = append(lines, ttyBox("AGENT", []string{"Waiting for the next agent invocation"}, width)...)
	} else {
		indices := []int{len(agents) - 1}
		if state.selected >= 0 {
			indices[0] = state.selected % len(agents)
		}
		if state.all {
			indices = indices[:0]
			for i := len(agents) - 1; i >= 0; i-- {
				indices = append(indices, i)
			}
		}
		for _, i := range indices {
			if available < 3 {
				break
			}
			agent := agents[i]
			title := fmt.Sprintf("AGENT %d/%d  %s · %s", i+1, len(agents), agent.meta.Agent, agent.meta.Runtime)
			if agent.role != "" {
				title += " · " + agent.role
			}
			if agent.active {
				if started, err := time.Parse(time.RFC3339, agent.meta.StartedAt); err == nil {
					title += " · working " + time.Since(started).Round(time.Second).String()
				} else {
					title += " · working"
				}
			}
			body := []string{"Now  " + agent.current}
			if !state.logs && len(agent.output) > 0 {
				body = append(body, "Result  "+agent.output[len(agent.output)-1])
			}
			body = body[:min(len(body), max(1, available-2))]
			if state.logs {
				detailTitle, detail := agent.detailTitle, agent.detail
				if len(agent.history) > 0 && available-len(body) > 4 {
					index := max(0, len(agent.history)-1-min(state.scroll, len(agent.history)-1))
					rows := min(8, max(1, available-len(body)-3))
					hasDetail := len(agent.history[index].detail) > 0 || (state.scroll == 0 && len(detail) > 0) || (state.toolView && len(agent.toolDetail) > 0)
					if hasDetail {
						rows = min(rows, max(1, (available-len(body)-4)/2))
					}
					if state.all {
						rows = 1
					}
					start := max(0, index-rows+1)
					end := min(len(agent.history), start+rows)
					body = append(body, fmt.Sprintf("History  %d/%d · ↑ older / ↓ newer · wheel", index+1, len(agent.history)))
					for h := start; h < end; h++ {
						marker := "  "
						if h == index {
							marker = "› "
						}
						body = append(body, marker+agent.history[h].label)
					}
					if state.scroll > 0 || len(agent.history[index].detail) > 0 {
						detailTitle, detail = agent.history[index].detailTitle, agent.history[index].detail
					}
				}
				if state.toolView && len(agent.toolDetail) > 0 {
					detailTitle, detail = agent.toolTitle, agent.toolDetail
				}
				if len(detail) > 0 && available-len(body)-3 > 0 {
					wrapped := watchDetailRows(detailTitle, detail, max(1, width-8), a.colorEnabled(a.Stdout))
					rows := min(len(wrapped), available-len(body)-3)
					if state.all {
						rows = min(rows, 2)
					}
					start := max(0, len(wrapped)-rows-max(0, state.detailScroll))
					end := min(len(wrapped), start+rows)
					body = append(body, fmt.Sprintf("%s  %d-%d/%d · J older / K newer", detailTitle, start+1, end, len(wrapped)))
					for _, row := range wrapped[start:end] {
						if strings.HasPrefix(row.plain, ttyPrestyled) {
							body = append(body, ttyPrestyled+"  "+strings.TrimPrefix(row.plain, ttyPrestyled))
						} else {
							body = append(body, "  "+row.plain)
						}
					}
				} else if len(agent.history) > 0 && state.scroll > 0 && !state.toolView && available-len(body) > 2 {
					body = append(body, "  No saved text for this activity")
				} else if len(agent.output) == 0 && available-len(body) > 2 {
					body = append(body, "  No text output yet; activity above updates as tools run")
				} else if len(agent.output) > 0 && available-len(body) > 2 {
					body = append(body, "Result  "+agent.output[len(agent.output)-1])
				}
			}
			box := ttyBox(title, body, width)
			lines = append(lines, box...)
			available -= len(box)
		}
	}
	lines = append(lines, "")
	lines = append(lines, bottom...)
	frame := ttyFrame(lines, width, height)
	if a.colorEnabled(a.Stdout) {
		var styled []string
		for _, line := range strings.Split(frame, "\r\n") {
			styled = append(styled, ttyStyleLine(line))
		}
		frame = strings.Join(styled, "\r\n")
	}
	return frame
}

func watchControls(state watchTTYState, width int) string {
	narrow := width < 65
	switch {
	case state.paused && state.canRetry && narrow:
		return "r auto retry  q leave  ? help"
	case state.paused && state.canRetry:
		return "r auto retry  f fresh  s resume  c compact  q leave  ? help"
	case state.paused && narrow:
		return "q leave paused  ? help"
	case state.paused:
		return "? help  ↑/↓ history  J/K text  q leave paused"
	case narrow && state.driving:
		return "? help  ↑↓ history  J/K text  n agent"
	case narrow:
		return "q detach  ↑↓ history  ? help"
	case state.driving:
		return "? help  ↑/↓ or wheel: history  J/K: text  n agent"
	default:
		return "? help  ↑/↓ or wheel: history  J/K: text  n agent  q detach"
	}
}

func watchHelpLines(state watchTTYState, width, height int) []string {
	if width < 50 {
		lines := []string{
			"HELP  ? hide · ↑/↓ activity",
			"  J/K text · n next agent",
			"  a all · l logs · t tool · d",
		}
		if state.paused && state.canRetry {
			lines = append(lines, "r:auto f:fresh s:saved c:compact")
		}
		return lines
	}
	if width < 65 || height < 22 {
		lines := []string{
			"HELP  ? hides this guide",
			"  ↑/↓ or wheel: activity; J/K: scroll message text",
			"  n agent · a all · l logs · t tool · d details",
		}
		if state.paused && state.canRetry && height >= 19 {
			lines = append(lines, "  Retry: r auto · f fresh · s saved · c compact; q leave")
		}
		return lines
	}
	lines := []string{
		"HELP  Press ? to hide this guide",
		"  ↑/↓ or wheel: older/newer activity; PgUp/PgDn: jump five",
		"  J/K: scroll message text; n: next agent; a: show all agents",
		"  l: show/hide logs; t: last tool result; d: decision detail",
	}
	if state.paused && state.canRetry {
		lines = append(lines, "  Retry: r policy session; f new; s saved; c compact then resume")
	}
	return lines
}

func ttyBox(title string, body []string, width int) []string {
	inner := max(1, width-4)
	border := "┌─ " + ttyFit(ttyClean(title), max(1, width-5))
	border += strings.Repeat("─", max(0, width-1-utf8.RuneCountInString(border))) + "┐"
	lines := []string{border}
	for _, line := range body {
		var content string
		if styled, ok := strings.CutPrefix(line, ttyPrestyled); ok {
			content = ttyFit(styled, inner)
		} else {
			content = ttyFit(ttySafeLine(line), inner)
		}
		lines = append(lines, "│ "+content+strings.Repeat(" ", max(0, inner-textWidth(content)))+" │")
	}
	lines = append(lines, "└"+strings.Repeat("─", width-2)+"┘")
	return lines
}

// ttyWrapPreserve wraps visual text without collapsing code indentation or
// diff markers. The caller scrolls the resulting visual rows.
func ttyWrapPreserve(raw string, width int) []string {
	line := ttySafeLine(raw)
	if width < 1 {
		return []string{""}
	}
	if utf8.RuneCountInString(line) <= width {
		return []string{line}
	}
	indent := ""
	for _, r := range line {
		if r != ' ' {
			break
		}
		indent += " "
	}
	if len(indent) >= width/2 {
		indent = "  "
	}
	continuation := indent
	if strings.HasPrefix(strings.TrimSpace(line), "┃ ") {
		continuation += "┃ "
	}
	if utf8.RuneCountInString(continuation) >= width {
		continuation = ""
	}
	var out []string
	for utf8.RuneCountInString(line) > width {
		runes := []rune(line)
		cut := width
		for i := width; i > width/2; i-- {
			if unicode.IsSpace(runes[i-1]) {
				cut = i
				break
			}
		}
		out = append(out, string(runes[:cut]))
		line = continuation + strings.TrimLeft(string(runes[cut:]), " ")
	}
	return append(out, line)
}

func ttyFrame(lines []string, width, height int) string {
	// Leave the last terminal row untouched to avoid an automatic scroll.
	limit := max(1, height-1)
	if len(lines) > limit {
		footer := lines[len(lines)-1]
		lines = append(append([]string{}, lines[:limit-2]...), "… more activity in saved logs", footer)
	}
	for i := range lines {
		lines[i] = ttyFit(lines[i], width)
	}
	return strings.Join(lines, "\r\n")
}

func ttyWrapLabel(label, value string, width, maxLines int) []string {
	value = ttyClean(value)
	if width <= len(label)+8 || utf8.RuneCountInString(label+value) <= width {
		return []string{label + value}
	}
	var lines []string
	for len(value) > 0 && len(lines) < maxLines {
		prefix := label
		if len(lines) > 0 {
			prefix = strings.Repeat(" ", utf8.RuneCountInString(label))
		}
		limit := width - utf8.RuneCountInString(prefix)
		runes := []rune(value)
		if len(runes) <= limit {
			lines = append(lines, prefix+value)
			break
		}
		cut := limit
		for cut > limit/2 && !unicode.IsSpace(runes[cut]) {
			cut--
		}
		if cut <= limit/2 {
			cut = limit
		}
		part := strings.TrimSpace(string(runes[:cut]))
		if len(lines) == maxLines-1 {
			lines = append(lines, prefix+ttyFit(part+"…", limit))
			break
		}
		lines = append(lines, prefix+part)
		value = strings.TrimSpace(string(runes[cut:]))
	}
	return lines
}

func ttyStyleLine(line string) string {
	style := func(code, s string) string { return code + s + ansiReset }
	if strings.HasPrefix(line, "SDLC  ") {
		if at := strings.Index(line, "  ·  "); at >= 0 {
			status := line[at+len("  ·  "):]
			code := ansiCyan
			if strings.Contains(status, "paused") || strings.Contains(status, "failed") {
				code = ansiYellow
			} else if strings.Contains(status, "done") {
				code = ansiGreen
			}
			return style(ansiBold, line[:at]) + "  ·  " + style(code, status)
		}
	}
	if strings.HasPrefix(line, "┌─ ") {
		content := strings.TrimPrefix(line, "┌─ ")
		at := strings.Index(content, " · ")
		if at < 0 {
			at = strings.Index(content, "─")
			if at < 0 {
				at = len(content)
			}
		}
		code := ansiCyan
		if strings.Contains(content, "working") {
			code = ansiGreen
		}
		return style("\x1b[2m", "┌─ ") + style(code, content[:at]) + style("\x1b[2m", content[at:])
	}
	if strings.HasPrefix(line, "└") {
		return style("\x1b[2m", line)
	}
	if strings.HasPrefix(line, "│ ") {
		content := strings.TrimPrefix(line, "│ ")
		right := ""
		if strings.HasSuffix(content, " │") {
			content = content[:len(content)-len(" │")]
			right = style("\x1b[2m", " │")
		}
		if strings.Contains(content, "\x1b[") {
			// Detail rows arrive highlighted by watchDetailRows.
			return style("\x1b[2m", "│ ") + content + right
		}
		code := ""
		trimmed := strings.TrimSpace(content)
		switch {
		case strings.HasPrefix(trimmed, "Error:"), strings.HasPrefix(trimmed, "stderr:"):
			code = ansiRed
		case strings.HasPrefix(content, "Now  "):
			code = ansiCyan
		case strings.HasPrefix(content, "History  "):
			code = ansiYellow
		case strings.HasPrefix(content, "› "):
			code = ansiCyan
		case strings.HasPrefix(content, "Recent  "):
			code = "\x1b[2m"
		case strings.HasPrefix(content, "Agent result"), strings.HasPrefix(content, "Result  "):
			code = ansiGreen
		case strings.HasPrefix(content, "Tool result"), strings.HasPrefix(content, "Files changed"), strings.HasPrefix(content, "Files being edited"), strings.HasPrefix(content, "Edit · "), strings.HasPrefix(content, "Write · "):
			code = ansiYellow
		}
		if code != "" {
			content = style(code, content)
		}
		return style("\x1b[2m", "│ ") + content + right
	}
	for _, label := range []string{"Task  ", "Budget  ", "Children  ", "DECISIONS", "Usage  "} {
		if strings.HasPrefix(line, label) {
			return style("\x1b[2m", label) + line[len(label):]
		}
	}
	if strings.HasPrefix(line, "PAUSED  ") {
		return style(ansiYellow, "PAUSED  ") + line[len("PAUSED  "):]
	}
	if strings.HasPrefix(line, "  ") && strings.Contains(line, " · ") {
		content := strings.TrimLeft(line, " ")
		kindAt := strings.Index(content, "  ")
		outcomeAt := strings.Index(content, " · ")
		if kindAt >= 0 && outcomeAt > kindAt+2 {
			choiceStart := len(line) - len(content) + kindAt + 2
			choice := content[kindAt+2 : outcomeAt]
			code := ""
			switch choice {
			case "failed":
				code = ansiRed
			case "pass":
				code = ansiGreen
			}
			if code != "" {
				return style("\x1b[2m", line[:choiceStart]) + style(code, choice) + style("\x1b[2m", line[choiceStart+len(choice):])
			}
		}
		return style("\x1b[2m", line)
	}
	return line
}

// ttyFit clips to width visible runes. SGR sequences are zero-width, so a
// pre-styled row is cut on its text and closed with a reset.
func ttyFit(s string, width int) string {
	if width < 1 {
		return ""
	}
	if textWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	styled, seen := false, 0
	for i := 0; i < len(s); {
		if loc := ansiSGR.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(s[i : i+loc[1]])
			i += loc[1]
			styled = true
			continue
		}
		if seen == width-1 {
			break
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
		seen++
	}
	b.WriteString("…")
	if styled {
		b.WriteString(ansiReset)
	}
	return b.String()
}

func ttyClean(s string) string {
	return strings.Join(strings.Fields(ttySafeLine(s)), " ")
}

func ttySafeLine(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == ']' {
			i += 2
			for i < len(s) && s[i] != '\a' && !(s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\') {
				i++
			}
			if i < len(s) && s[i] == '\x1b' {
				i += 2
			} else if i < len(s) {
				i++
			}
			continue
		}
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < '@' || s[i] > '~') {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == '\t' {
			b.WriteString("    ")
		} else if unicode.IsControl(r) {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func watchTail(path string, maxBytes int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	start := max(int64(0), info.Size()-maxBytes)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(f, maxBytes))
	if start > 0 {
		if end := strings.IndexByte(string(raw), '\n'); end >= 0 {
			return raw[end+1:]
		}
		return nil
	}
	return raw
}

func (a *App) sdlcWatchAgents(runs []ledger.Run) []watchAgent {
	var agents []watchAgent
	for _, run := range runs {
		dir := filepath.Join(a.sdlcRunsDir(), run.RunID, "logs")
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var meta worker.LogMeta
			if raw, err := os.ReadFile(filepath.Join(dir, entry.Name())); err != nil || json.Unmarshal(raw, &meta) != nil || !sdlcRunIDRE.MatchString(meta.Invocation) {
				continue
			}
			workDir := run.WorkDir
			if workDir == "" {
				workDir = a.WorkDir
			}
			agent := watchAgent{meta: meta, dir: dir, workDir: workDir, current: "Waiting for runtime activity"}
			if run.Adaptive != nil {
				for _, pending := range run.Adaptive.Pending() {
					if pending.InvocationID == meta.Invocation {
						agent.active, agent.role = true, pending.Role
					}
				}
			}
			agents = append(agents, agent)
		}
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].meta.StartedAt < agents[j].meta.StartedAt })
	cfg, err := config.Load(a.loadOptions())
	if err != nil {
		return agents
	}
	redactor, err := cfg.Redactor()
	if err != nil {
		return agents
	}
	redact := func(s string) string {
		clean, err := redactor.Apply(s)
		if err != nil {
			return "[activity unavailable]"
		}
		return ttyClean(clean.Text)
	}
	redactLine := func(s string) string {
		clean, err := redactor.Apply(s)
		if err != nil {
			return "[activity unavailable]"
		}
		return ttySafeLine(clean.Text)
	}
	for i := range agents {
		agent := &agents[i]
		raw := watchTail(filepath.Join(agent.dir, agent.meta.Invocation+".lines.jsonl"), 1<<20)
		var recent []string
		var history []watchHistory
		var toolTitle string
		var toolDetail []string
		activeTools := map[string]string{}
		var activeOrder []string
		for _, line := range strings.Split(string(raw), "\n") {
			var saved worker.LogLine
			if json.Unmarshal([]byte(line), &saved) != nil {
				continue
			}
			previewTitle, previewDetail := watchToolPreview(saved.Text)
			if len(previewDetail) == 0 {
				previewTitle, previewDetail = watchCodexToolPreview(saved.Text)
			}
			if len(previewDetail) == 0 {
				previewTitle, previewDetail = watchEditPreview(saved.Text, agent.workDir)
			}
			fileLabel, fileTitle, fileDetail := watchFileChanges(saved.Text, agent.workDir)
			if fileLabel != "" {
				previewTitle, previewDetail = fileTitle, fileDetail
			}
			if len(previewDetail) > 0 {
				toolTitle, toolDetail = previewTitle, previewDetail
			}
			activity, key, started, completed := watchActivity(saved)
			if fileLabel != "" {
				activity = fileLabel
			}
			if activity == "" {
				continue
			}
			activity = redact(activity)
			if started && key != "" {
				activeTools[key] = activity
				activeOrder = append(activeOrder, key)
			}
			if completed && key != "" {
				delete(activeTools, key)
			}
			recent = append(recent, activity)
			event := watchHistory{key: key, started: started, label: activity, detailTitle: redact(previewTitle)}
			for _, detail := range previewDetail {
				event.detail = append(event.detail, redactLine(detail))
			}
			updated := false
			if completed && key != "" {
				for h := len(history) - 1; h >= 0; h-- {
					if history[h].key == key && history[h].started {
						history[h] = event
						updated = true
						break
					}
				}
			}
			if !updated {
				history = append(history, event)
				if len(history) > 200 {
					history = history[1:]
				}
			}
		}
		if len(recent) > 3 {
			recent = recent[len(recent)-3:]
		}
		agent.recent = recent
		agent.history = history
		for j := len(activeOrder) - 1; j >= 0; j-- {
			if current, ok := activeTools[activeOrder[j]]; ok {
				agent.current = current
				break
			}
		}
		if agent.current == "Waiting for runtime activity" && len(recent) > 0 {
			agent.current = recent[len(recent)-1]
		}
		if !agent.active {
			agent.current = "Invocation finished"
		}
		var finalTitle string
		var finalDetail []string
		for _, stream := range []string{"stdout", "stderr"} {
			for _, line := range strings.Split(string(watchTail(filepath.Join(agent.dir, agent.meta.Invocation+"."+stream), 64<<10)), "\n") {
				if summary := sdlcStreamSummary(stream, line); summary != "" {
					agent.output = append(agent.output, redact(stream+": "+summary))
				}
				if stream == "stdout" {
					if title, detail := watchFinalPreview(line); len(detail) > 0 {
						finalTitle, finalDetail = title, detail
					}
				}
			}
		}
		if len(agent.output) > 2 {
			agent.output = agent.output[len(agent.output)-2:]
		}
		if !agent.active {
			for j := len(agent.output) - 1; j >= 0; j-- {
				if strings.HasPrefix(agent.output[j], "stdout: result ") {
					outcome := strings.TrimPrefix(agent.output[j], "stdout: result ")
					if end := strings.IndexByte(outcome, ':'); end >= 0 {
						agent.current = "Invocation finished: " + outcome[:end]
					}
					break
				}
				if strings.HasPrefix(agent.output[j], "stdout: reported ") {
					agent.current = "Invocation finished: " + strings.TrimPrefix(agent.output[j], "stdout: reported ")
					break
				}
			}
		}
		agent.toolTitle, agent.toolDetail = redact(toolTitle), toolDetail
		for j := range agent.toolDetail {
			agent.toolDetail[j] = redactLine(agent.toolDetail[j])
		}
		agent.detailTitle, agent.detail = agent.toolTitle, agent.toolDetail
		if len(finalDetail) > 0 {
			agent.detailTitle, agent.detail = finalTitle, finalDetail
			for j := range agent.detail {
				agent.detail[j] = redactLine(agent.detail[j])
			}
			agent.history = append(agent.history, watchHistory{label: redact(finalTitle), detailTitle: redact(finalTitle), detail: agent.detail})
		}
	}
	return agents
}

func watchActivity(saved worker.LogLine) (label, key string, started, completed bool) {
	if saved.Stream == "stderr" {
		return "stderr: " + shortProgressText(saved.Text, 120), "", false, false
	}
	var event struct {
		Type     string                     `json:"type"`
		Subtype  string                     `json:"subtype"`
		Event    string                     `json:"event"`
		CallID   string                     `json:"call_id"`
		ToolCall map[string]json.RawMessage `json:"tool_call"`
		Message  struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
		Item struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Command string `json:"command"`
			Text    string `json:"text"`
		} `json:"item"`
		Part struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State struct {
				Input watchEditInput `json:"input"`
			} `json:"state"`
		} `json:"part"`
	}
	if json.Unmarshal([]byte(saved.Text), &event) != nil {
		return shortProgressText(saved.Text, 120), "", false, false
	}
	if event.Type == "" {
		event.Type = event.Event
	}
	switch event.Type {
	case "tool_call":
		name, target := watchTool(event.ToolCall)
		key = event.CallID
		if key == "" {
			key = name + " " + target
		}
		started, completed = event.Subtype == "started", event.Subtype == "completed"
		label = name
		if target != "" {
			label += " " + target
		}
		if completed {
			label += " done"
		}
		return label, key, started, completed
	case "item.started", "item.completed":
		switch event.Item.Type {
		case "command_execution":
			label = "Running " + shortProgressText(event.Item.Command, 120)
		case "file_change":
			label = "Editing files"
		case "agent_message":
			if event.Item.Text != "" {
				return "Says: " + shortProgressText(event.Item.Text, 120), event.Item.ID, false, true
			}
			label = "Agent message"
		default:
			label = strings.ReplaceAll(event.Item.Type, "_", " ")
			if event.Item.Name != "" {
				label += " " + event.Item.Name
			}
		}
		if event.Type == "item.completed" {
			label += " done"
		}
		key = event.Item.ID
		return label, key, event.Type == "item.started", event.Type == "item.completed"
	case "assistant":
		for _, content := range event.Message.Content {
			if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
				if reply, err := worker.ParseReply([]byte(content.Text)); err == nil {
					return "Reported " + reply.Outcome, "", false, false
				}
				return "Says: " + shortProgressText(content.Text, 150), "", false, false
			}
			if content.Type == "tool_use" {
				return "Using " + content.Name, "", false, false
			}
		}
	case "tool_use":
		if event.Part.Tool != "" {
			label = "Using " + event.Part.Tool
			if path := cmp.Or(event.Part.State.Input.FilePath2, event.Part.State.Input.FilePath); path != "" {
				label += " " + filepath.Base(path)
			}
			return label, "", false, false
		}
	case "text":
		if event.Part.Text != "" {
			if reply, err := worker.ParseReply([]byte(event.Part.Text)); err == nil {
				return "Reported " + reply.Outcome, "", false, false
			}
			return "Says: " + shortProgressText(event.Part.Text, 150), "", false, false
		}
	}
	return "", "", false, false
}

func watchCodexToolPreview(raw string) (string, []string) {
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type             string `json:"type"`
			Command          string `json:"command"`
			Text             string `json:"text"`
			AggregatedOutput string `json:"aggregated_output"`
			Output           string `json:"output"`
			ExitCode         *int   `json:"exit_code"`
		} `json:"item"`
	}
	if json.Unmarshal([]byte(raw), &event) != nil || event.Type != "item.completed" {
		return "", nil
	}
	if event.Item.Type == "agent_message" && event.Item.Text != "" {
		return "Agent text", formatWatchExcerpt(event.Item.Text, 80)
	}
	if event.Item.Type != "command_execution" {
		return "", nil
	}
	content := event.Item.AggregatedOutput
	if content == "" {
		content = event.Item.Output
	}
	if content == "" && event.Item.ExitCode == nil {
		return "", nil
	}
	title := "Tool result · " + shortProgressText(event.Item.Command, 100)
	if event.Item.ExitCode != nil {
		title += fmt.Sprintf(" (exit %d)", *event.Item.ExitCode)
	}
	if content == "" {
		content = "Command finished with no text output"
	}
	return title, formatWatchExcerpt(content, 80)
}

func watchFileChanges(raw, workDir string) (label, title string, detail []string) {
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Changes []struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal([]byte(raw), &event) != nil || event.Item.Type != "file_change" || len(event.Item.Changes) == 0 {
		return "", "", nil
	}
	if event.Type != "item.started" && event.Type != "item.completed" {
		return "", "", nil
	}
	var names, inside []string
	for _, change := range event.Item.Changes {
		if change.Path == "" {
			continue
		}
		path := filepath.Clean(change.Path)
		if filepath.IsAbs(path) {
			if relative, err := filepath.Rel(workDir, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				path = relative
				inside = append(inside, path)
			} else {
				path = filepath.Base(path) + " (outside project)"
			}
		} else if path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			inside = append(inside, path)
		}
		names = append(names, path)
		if len(detail) < 20 {
			kind := change.Kind
			if kind == "" {
				kind = "changed"
			}
			detail = append(detail, kind+"  "+path)
		}
	}
	if len(names) == 0 {
		return "", "", nil
	}
	if len(names) > len(detail) {
		detail = append(detail, fmt.Sprintf("… %d more files", len(names)-len(detail)))
	}
	label = "Editing " + strings.Join(names[:min(2, len(names))], ", ")
	if len(names) > 2 {
		label += fmt.Sprintf(" +%d more", len(names)-2)
	}
	if event.Type == "item.completed" {
		if event.Item.Status == "failed" || event.Item.Status == "cancelled" {
			return label + " failed", "File change failed", detail
		}
		label += " done"
		// Codex reports paths only; show what the files now differ by.
		return label, "Files changed", append(detail, watchWorkDiff(workDir, inside)...)
	}
	return label, "Files being edited", detail
}

// watchEditInput is the edit payload shared by Claude (snake_case) and
// opencode (camelCase) file tools.
type watchEditInput struct {
	FilePath  string `json:"file_path"`
	FilePath2 string `json:"filePath"`
	OldString string `json:"old_string"`
	OldStr2   string `json:"oldString"`
	NewString string `json:"new_string"`
	NewStr2   string `json:"newString"`
	Content   string `json:"content"`
	Edits     []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`
}

// watchEditPreview turns an Edit/Write/MultiEdit tool call into diff rows.
func watchEditPreview(raw, workDir string) (string, []string) {
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type  string          `json:"type"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
		Part struct {
			Tool  string `json:"tool"`
			State struct {
				Input json.RawMessage `json:"input"`
			} `json:"state"`
		} `json:"part"`
	}
	if json.Unmarshal([]byte(raw), &event) != nil {
		return "", nil
	}
	var name string
	var input json.RawMessage
	switch event.Type {
	case "assistant":
		for _, block := range event.Message.Content {
			if block.Type == "tool_use" {
				name, input = block.Name, block.Input
			}
		}
	case "tool_use":
		name, input = event.Part.Tool, event.Part.State.Input
	}
	var in watchEditInput
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return "", nil
	}
	path := cmp.Or(in.FilePath, in.FilePath2)
	if path == "" {
		return "", nil
	}
	if relative, err := filepath.Rel(workDir, path); err == nil && filepath.IsAbs(path) && !strings.HasPrefix(relative, "..") {
		path = relative
	} else if filepath.IsAbs(path) {
		path = filepath.Base(path) + " (outside project)"
	}
	switch strings.ToLower(name) {
	case "edit":
		return "Edit · " + path, editDiffLines(path, cmp.Or(in.OldString, in.OldStr2), cmp.Or(in.NewString, in.NewStr2))
	case "multiedit":
		var lines []string
		for i, edit := range in.Edits {
			diff := editDiffLines(path, edit.OldString, edit.NewString)
			if i > 0 {
				diff = diff[2:] // one ---/+++ header per file
			}
			lines = append(lines, diff...)
		}
		return "Edit · " + path, lines
	case "write":
		return "Write · " + path, editDiffLines(path, "", in.Content)
	}
	return "", nil
}

func watchTool(calls map[string]json.RawMessage) (name, target string) {
	for _, candidate := range []string{"readToolCall", "grepToolCall", "globToolCall", "shellToolCall", "mcpToolCall", "getMcpToolsToolCall", "editToolCall", "writeToolCall", "webFetchToolCall"} {
		raw, ok := calls[candidate]
		if !ok {
			continue
		}
		var call struct {
			Args struct {
				Path     string `json:"path"`
				Pattern  string `json:"pattern"`
				Command  string `json:"command"`
				ToolName string `json:"toolName"`
				URL      string `json:"url"`
			} `json:"args"`
		}
		_ = json.Unmarshal(raw, &call)
		name = strings.TrimSuffix(candidate, "ToolCall")
		switch name {
		case "read":
			name, target = "Reading", call.Args.Path
		case "grep":
			name, target = "Searching", call.Args.Pattern
		case "glob":
			name, target = "Finding files", call.Args.Pattern
		case "shell":
			name, target = "Running", call.Args.Command
		case "mcp", "getMcpTools":
			name, target = "Using tool", call.Args.ToolName
		case "edit":
			name, target = "Editing", call.Args.Path
		case "write":
			name, target = "Writing", call.Args.Path
		case "webFetch":
			name, target = "Fetching", call.Args.URL
		}
		return name, shortProgressText(target, 120)
	}
	return "Using tool", ""
}

func watchFinalPreview(raw string) (string, []string) {
	var event struct {
		Type   string `json:"type"`
		Result string `json:"result"`
		Part   struct {
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
		return "", nil
	}
	text := event.Result
	if text == "" && event.Type == "assistant" {
		for _, part := range event.Message.Content {
			if part.Type == "text" {
				text = part.Text
			}
		}
	}
	if text == "" {
		text = event.Part.Text
	}
	if text == "" {
		return "", nil
	}
	if reply, err := worker.ParseReply([]byte(text)); err == nil {
		return "Agent result · " + reply.Outcome, formatWatchExcerpt(reply.Content, 80)
	}
	if event.Type == "assistant" || event.Part.Text != "" {
		return "Agent text", formatWatchExcerpt(text, 40)
	}
	return "", nil
}

func watchToolPreview(raw string) (string, []string) {
	var event struct {
		Type     string                     `json:"type"`
		Subtype  string                     `json:"subtype"`
		ToolCall map[string]json.RawMessage `json:"tool_call"`
	}
	if json.Unmarshal([]byte(raw), &event) != nil || event.Type != "tool_call" || event.Subtype != "completed" {
		return "", nil
	}
	name, target := watchTool(event.ToolCall)
	if _, schema := event.ToolCall["getMcpToolsToolCall"]; schema {
		return "", nil
	}
	var payload json.RawMessage
	for _, key := range []string{"readToolCall", "grepToolCall", "globToolCall", "shellToolCall", "mcpToolCall", "editToolCall", "writeToolCall", "webFetchToolCall"} {
		if raw, ok := event.ToolCall[key]; ok {
			payload = raw
			break
		}
	}
	if len(payload) == 0 {
		return "", nil
	}
	var call struct {
		Result struct {
			Success json.RawMessage `json:"success"`
			Error   json.RawMessage `json:"error"`
		} `json:"result"`
	}
	if json.Unmarshal(payload, &call) != nil {
		return "", nil
	}
	title := "Tool result · " + name
	if target != "" {
		title += " " + target
	}
	if len(call.Result.Error) > 0 {
		var message string
		if json.Unmarshal(call.Result.Error, &message) != nil {
			message = string(call.Result.Error)
		}
		return title, formatWatchExcerpt("Error: "+message, 12)
	}
	var success struct {
		Content          json.RawMessage `json:"content"`
		Stdout           string          `json:"stdout"`
		Output           string          `json:"output"`
		WorkspaceResults map[string]struct {
			Content struct {
				Matches []struct {
					File    string `json:"file"`
					Matches []struct {
						LineNumber int    `json:"lineNumber"`
						Content    string `json:"content"`
					} `json:"matches"`
				} `json:"matches"`
			} `json:"content"`
		} `json:"workspaceResults"`
	}
	if json.Unmarshal(call.Result.Success, &success) != nil {
		return "", nil
	}
	var content string
	_ = json.Unmarshal(success.Content, &content)
	if content == "" {
		content = success.Stdout
	}
	if content == "" {
		content = success.Output
	}
	if content != "" {
		return title, formatWatchExcerpt(content, 80)
	}
	var paths []string
	for path := range success.WorkspaceResults {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var matches []string
	for _, path := range paths {
		for _, group := range success.WorkspaceResults[path].Content.Matches {
			for _, match := range group.Matches {
				matches = append(matches, fmt.Sprintf("%s:%d  %s", group.File, match.LineNumber, match.Content))
				if len(matches) >= 80 {
					return title, matches
				}
			}
		}
	}
	return title, matches
}

func formatWatchExcerpt(content string, maxLines int) []string {
	if len(content) > 32<<10 {
		content = content[:32<<10]
	}
	var lines []string
	inCode := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r\t ")
		if strings.TrimSpace(line) == "" && !inCode {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			line = "┃ " + strings.TrimSpace(line)
		} else if inCode || strings.HasPrefix(line, "    ") {
			line = "┃ " + line
		}
		lines = append(lines, ttySafeLine(line))
		if len(lines) >= maxLines {
			lines = append(lines, "… more in sdlc logs")
			break
		}
	}
	return lines
}
