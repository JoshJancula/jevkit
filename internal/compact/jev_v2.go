package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/registry"
)

var budgetLines = [6]int{8, 20, 48, 140, 400, 0}
var budgetBytes = [6]int{512, 1536, 4096, 12288, 40960, 0}

func compactV2(command, stdout, stderr string, exit int, asker Asker, opts JevOptions, base Result) (JevResult, Result) {
	original := joinStreams(stdout, stderr)
	if opts.Policy != nil {
		if rule := opts.Policy.Match(command, original); rule != nil {
			opts.PolicyRuleID = rule.ID
		}
	}
	fallback := func(err error) (JevResult, Result) {
		if opts.Shadow {
			return JevResult{Body: original, Err: err}, unchanged(stdout, stderr, exit, base.Family)
		}
		return JevResult{Body: joinStreams(base.Stdout, base.Stderr), Err: err}, base
	}
	r, err := redact.New(redact.Options{})
	if err != nil {
		return fallback(err)
	}
	redacted, err := r.Apply(original)
	if err != nil {
		return fallback(err)
	}
	redactedCommand, err := r.Apply(command)
	if err != nil {
		return fallback(err)
	}
	lines := splitLines(redacted.Text)
	if len(lines) == 0 {
		return fallback(fmt.Errorf("empty output"))
	}
	reg, err := registry.Load()
	if err != nil {
		return fallback(err)
	}
	set, ok := reg.Set("compaction.triage.v2")
	if !ok {
		return fallback(fmt.Errorf("triage registry missing"))
	}
	questions, err := set.JevQuestions(nil)
	if err != nil {
		return fallback(err)
	}
	markers, firstMarker, middleMarkers := markerStats(lines)
	state := triageState(redactedCommand.Text, exit, opts, lines, markers, firstMarker, middleMarkers)
	resp, err := asker.Ask(context.Background(), jev.Request{QuestionSetID: set.ID, State: state, Questions: questions})
	if err != nil {
		return fallback(fmt.Errorf("triage unavailable: %w", err))
	}
	if resp == nil {
		return fallback(fmt.Errorf("triage returned no answer"))
	}
	primary, ok := resp.Answers["evidence_locus"].(jev.ChoiceAnswer)
	if !ok {
		return fallback(fmt.Errorf("triage locus answer missing"))
	}
	_, conservativeConfidence, ok := cumulativeLocusMass(primary, set.Policy.EscalateThreshold)
	if !ok {
		conservativeConfidence = primary.Confidence
	}
	dec, err := (&registry.Decider{Registry: reg}).DecideWithConfidence(set.ID, resp.Answers, nil, conservativeConfidence)
	if err != nil {
		return fallback(err)
	}
	family := base.Family
	logged := false
	defer func() {
		if !logged {
			baseBody := joinStreams(base.Stdout, base.Stderr)
			dec.Decision, dec.FallbackUsed, dec.Reason = registry.Fallback, true, "safety-demotion"
			logCompactDecision(opts, dec, family, len(original), len(lines), len(baseBody), len(splitLines(baseBody)))
		}
	}()
	threshold := set.Policy.ActThreshold
	if dec.Decision == registry.Gather {
		threshold = set.Policy.EscalateThreshold
	}
	locus, outcome, kind, level, trusted := triageDisposition(resp.Answers, dec, threshold, exit, opts.AuthoritativeExit, opts.RawPointer != "", markers, middleMarkers)
	if kind != "" {
		family = kind
	}
	if !trusted {
		return fallback(fmt.Errorf("triage below registered threshold"))
	}
	if level == 5 {
		return fallback(nil)
	}
	selected := make(map[int]bool)
	var salientDecision *registry.Decision
	switch locus {
	case "tail":
		selectTail(lines, selected, budgetLines[level])
		for i := 0; i < len(lines)-budgetLines[level]; i++ {
			if shouldPreserve(lines[i]) {
				locus = "first-failure"
				break
			}
		}
	case "head":
		if kind != "help-usage" && kind != "version-probe" && outcome != "success" && outcome != "no-op" {
			locus = "first-failure"
		}
		selectHead(lines, selected, budgetLines[level])
		for i := budgetLines[level]; i < len(lines); i++ {
			if shouldPreserve(lines[i]) {
				locus = "first-failure"
				break
			}
		}
	case "nowhere":
		retainedTail := min(2, budgetLines[level])
		if exit != 0 || !opts.AuthoritativeExit || (outcome != "success" && outcome != "no-op") || level > 1 || kind == "data-query" || kind == "log-stream" || kind == "help-usage" || kind == "migration-schema" || kind == "deploy-infra" {
			locus = "tail"
		} else {
			for i, line := range lines {
				if errorRE.MatchString(line) || (summaryRE.MatchString(line) && i < len(lines)-retainedTail) {
					locus = "tail"
					break
				}
			}
		}
		if locus == "nowhere" {
			selectTail(lines, selected, retainedTail)
		}
	case "scattered":
		selected, salientDecision, err = selectSalient(asker, reg, redactedCommand.Text, exit, lines, firstMarker, budgetLines[level], set.Policy, opts)
		if err != nil {
			if salientDecision != nil {
				fallbackBody := joinStreams(base.Stdout, base.Stderr)
				logCompactDecision(opts, *salientDecision, family, len(original), len(lines), len(fallbackBody), len(splitLines(fallbackBody)))
			}
			return fallback(err)
		}
	case "first-failure":
		// Populated below from the local marker scan.
	case "throughout":
		return fallback(nil)
	default:
		return fallback(fmt.Errorf("unknown locus"))
	}
	if locus == "first-failure" {
		if firstMarker < 0 {
			return fallback(nil)
		}
		selected = make(map[int]bool)
		width := min(4, max(1, (budgetLines[level]-min(4, budgetLines[level]/4)-1)/2))
		for i := max(0, firstMarker-width); i <= min(len(lines)-1, firstMarker+width); i++ {
			selected[i] = true
		}
		selectTail(lines, selected, min(4, budgetLines[level]/4))
	}
	if locus == "tail" && len(selected) == 0 {
		selectTail(lines, selected, budgetLines[level])
	}
	if locus == "scattered" {
		selected[len(lines)-1] = true
		if firstMarker >= 0 {
			selected[firstMarker] = true
		}
	}
	// A locally recognizable diagnostic line is never discarded on the model
	// path. Scattered selection can add it within the registered budget; other
	// loci fall back to the deterministic tier when their claim is falsified.
	for i, line := range lines {
		if !shouldPreserve(line) || selected[i] {
			continue
		}
		if locus != "scattered" || len(selected) >= budgetLines[level] {
			return fallback(fmt.Errorf("diagnostic marker outside retained selection"))
		}
		selected[i] = true
	}
	body, ok := renderV2(lines, selected, locus, outcome, kind, level)
	if !ok || len(body) >= len(original) {
		return fallback(fmt.Errorf("assembled output not smaller or failed verification"))
	}
	logCompactDecision(opts, dec, family, len(original), len(lines), len(body), len(splitLines(body)))
	if salientDecision != nil {
		logCompactDecision(opts, *salientDecision, family, len(original), len(lines), len(body), len(splitLines(body)))
	}
	logged = true
	if opts.Shadow {
		return JevResult{Body: original}, unchanged(stdout, stderr, exit, base.Family)
	}
	return JevResult{Body: body, Used: true}, Result{Stdout: body, Compacted: true, StdoutCompacted: true, Family: base.Family, Status: StatusCompacted, ExitStatus: exit}
}

func markerStats(lines []string) (count, first, middle int) {
	first = -1
	firstFailure := -1
	for i, line := range lines {
		if firstFailure < 0 && errorRE.MatchString(line) {
			firstFailure = i
		}
		if shouldPreserve(line) {
			count++
			if first < 0 {
				first = i
			}
			if i >= 90 && i < len(lines)-60 {
				middle++
			}
		}
	}
	if firstFailure >= 0 {
		first = firstFailure
	}
	return
}

func triageState(command string, exit int, opts JevOptions, lines []string, markers, first, middle int) string {
	distinct := map[string]bool{}
	longest, run := 0, 0
	for i, line := range lines {
		distinct[line] = true
		if i > 0 && line == lines[i-1] {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
	}
	head, tail := min(90, len(lines)), min(60, max(0, len(lines)-90))
	parts := []string{fmt.Sprintf("command: %s", command), fmt.Sprintf("exit_status: %d", exit), fmt.Sprintf("exit_authoritative: %t", opts.AuthoritativeExit), fmt.Sprintf("raw_original_retrievable: %t", opts.RawPointer != ""), fmt.Sprintf("lines: %d", len(lines)), fmt.Sprintf("bytes: %d", len(strings.Join(lines, "\n"))), fmt.Sprintf("distinct_line_ratio: %.3f", float64(len(distinct))/float64(len(lines))), fmt.Sprintf("longest_repeat_run: %d", longest), fmt.Sprintf("diagnostic_marker_lines: %d", markers), fmt.Sprintf("first_marker_at_line: %d", first), fmt.Sprintf("markers_inside_elided_region: %d", middle), "evidence:"}
	parts = append(parts, lines[:head]...)
	if len(lines) > head+tail {
		parts = append(parts, fmt.Sprintf("[jevkit] %d evidence line(s) omitted", len(lines)-head-tail))
	}
	if tail > 0 {
		parts = append(parts, lines[len(lines)-tail:]...)
	}
	return strings.Join(parts, "\n")
}

func selectTail(lines []string, selected map[int]bool, n int) {
	for i := max(0, len(lines)-n); i < len(lines); i++ {
		selected[i] = true
	}
}
func selectHead(lines []string, selected map[int]bool, n int) {
	for i := 0; i < min(n, len(lines)); i++ {
		selected[i] = true
	}
}

func renderV2(lines []string, selected map[int]bool, locus, outcome, kind string, level int) (string, bool) {
	indices := make([]int, 0, len(selected))
	for i := range selected {
		if i >= 0 && i < len(lines) {
			indices = append(indices, i)
		}
	}
	sort.Ints(indices)
	if len(indices) > budgetLines[level] {
		return "", false
	}
	var b Body
	b.AppendHeader(locus, outcome, kind, level)
	prev := -1
	for _, i := range indices {
		b.AppendMarker(i - prev - 1)
		b.AppendVerbatim(i, lines)
		prev = i
	}
	b.AppendMarker(len(lines) - prev - 1)
	if !b.Verify(lines) {
		return "", false
	}
	text := b.String()
	if len(text) > budgetBytes[level] {
		return "", false
	}
	return text, true
}

func selectSalient(asker Asker, reg *registry.Registry, command string, exit int, lines []string, firstMarker, maxSelected int, policy registry.Policy, opts JevOptions) (map[int]bool, *registry.Decision, error) {
	set, ok := reg.Set("compaction.salient-lines.v2")
	if !ok {
		return nil, nil, fmt.Errorf("salient registry missing")
	}
	candidates := candidateIndices(lines, min(254, opts.maxLines()-1))
	tagMap := make(map[string]int, len(candidates))
	callTime := make(map[string]map[string]json.RawMessage)
	roles := []string{"outcome_line", "cause_line", "tally_line", "location_line", "second_site_line", "next_step_line"}
	for _, role := range roles {
		callTime[role] = make(map[string]json.RawMessage, len(candidates))
	}
	state := []string{fmt.Sprintf("command: %s", command), fmt.Sprintf("exit_status: %d", exit), "candidate original lines:"}
	for _, idx := range candidates {
		tag := fmt.Sprintf("L%06d", idx)
		tagMap[tag] = idx
		state = append(state, tag+" "+lines[idx])
		for _, role := range roles {
			callTime[role][tag] = jev.Null()
		}
	}
	questions, err := set.JevQuestions(callTime)
	if err != nil {
		return nil, nil, err
	}
	resp, err := asker.Ask(context.Background(), jev.Request{QuestionSetID: set.ID, State: strings.Join(state, "\n"), Questions: questions})
	if err != nil {
		return nil, nil, fmt.Errorf("salient selection unavailable: %w", err)
	}
	if resp == nil {
		return nil, nil, fmt.Errorf("salient selection returned no answer")
	}
	dec, err := (&registry.Decider{Registry: reg}).DecideWith(set.ID, resp.Answers, callTime)
	if err != nil {
		return nil, nil, err
	}
	if dec.Decision == registry.Fallback {
		return nil, &dec, fmt.Errorf("salient selection below registered threshold")
	}
	selected := make(map[int]bool)
	selected[len(lines)-1] = true
	if firstMarker >= 0 {
		selected[firstMarker] = true
	}
	if len(selected) > maxSelected {
		return nil, &dec, fmt.Errorf("required diagnostic lines exceed budget")
	}
	width := 2
	if dec.Decision == registry.Gather {
		width = 4
	}
	if yes, _ := strongNoul(resp.Answers, "has_failure", policy.ActThreshold); yes {
		width = 4
	}
	if _, no := strongNoul(resp.Answers, "selection_covers", policy.ActThreshold); no {
		width = 4
	}
	picks := 0
	for _, role := range roles {
		a, ok := resp.Answers[role].(jev.ChoiceAnswer)
		if !ok || a.Choice == "NONE" || a.Confidence < set.Policy.EscalateThreshold {
			continue
		}
		idx, offered := tagMap[a.Choice]
		if !offered {
			return nil, nil, fmt.Errorf("unoffered line tag")
		}
		picks++
		for distance := 0; distance <= width; distance++ {
			for _, i := range []int{idx - distance, idx + distance} {
				if i >= 0 && i < len(lines) && len(selected) < maxSelected {
					selected[i] = true
				}
			}
		}
	}
	if picks == 0 {
		return nil, nil, fmt.Errorf("no salient lines selected")
	}
	return selected, &dec, nil
}

func candidateIndices(lines []string, cap int) []int {
	if cap <= 0 {
		cap = 1
	}
	chosen := make(map[int]bool)
	shapes := make(map[string]bool)
	for i, line := range lines {
		if shouldPreserve(line) {
			chosen[i] = true
		}
		shape := lineShape(line)
		if !shapes[shape] {
			shapes[shape], chosen[i] = true, true
		}
		if cls := classOf(line); cls != nil {
			if i == 0 || classOf(lines[i-1]) == nil || classOf(lines[i-1]).id != cls.id {
				chosen[i] = true
			}
			if i == len(lines)-1 || classOf(lines[i+1]) == nil || classOf(lines[i+1]).id != cls.id {
				chosen[i] = true
			}
		}
	}
	seeds := make([]int, 0, len(chosen))
	for i := range chosen {
		seeds = append(seeds, i)
	}
	for _, i := range seeds {
		if i > 0 {
			chosen[i-1] = true
		}
		if i+1 < len(lines) {
			chosen[i+1] = true
		}
	}
	for i := 0; i < len(lines) && len(chosen) < cap; i += max(1, len(lines)/cap) {
		chosen[i] = true
	}
	chosen[len(lines)-1] = true
	idx := make([]int, 0, len(chosen))
	for i := range chosen {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	if len(idx) <= cap {
		return idx
	}
	// Preserve temporal coverage when more marker hits exist than options.
	out := make([]int, 0, cap)
	for j := 0; j < cap; j++ {
		out = append(out, idx[j*(len(idx)-1)/max(1, cap-1)])
	}
	return out
}

var shapeNumberRE = regexp.MustCompile(`[0-9]+`)
var shapeHexRE = regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b`)
var shapePathRE = regexp.MustCompile(`(?:/[^\s:]+)+`)

func lineShape(line string) string {
	line = shapePathRE.ReplaceAllString(line, "<path>")
	line = shapeHexRE.ReplaceAllString(line, "<hex>")
	return shapeNumberRE.ReplaceAllString(line, "#")
}

func logCompactDecision(opts JevOptions, dec registry.Decision, family string, before, linesBefore, after, linesAfter int) {
	if opts.StateDir == "" {
		return
	}
	dec.Shadow = opts.Shadow
	dec.FallbackUsed = opts.Shadow || dec.FallbackUsed
	dec.CommandFamily = family
	dec.PolicyRuleID = opts.PolicyRuleID
	dec.Runtime = opts.Runtime
	dec.BytesBefore, dec.BytesAfter = before, after
	dec.LinesBefore, dec.LinesAfter = linesBefore, linesAfter
	dec.Timestamp = time.Now().UTC().Format(time.RFC3339)
	_ = registry.AppendDecision(opts.StateDir, dec)
}
