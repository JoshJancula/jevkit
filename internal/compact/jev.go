package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
	"github.com/OWNER/jevkit/internal/jev"
)

// Asker is the slice of the jev client the compactor uses.
type Asker interface {
	Ask(ctx context.Context, req jev.Request) (*jev.Response, error)
}

// JevOptions tunes the ranked-line tier. The zero value is safe: compaction
// is disabled by default.
type JevOptions struct {
	// Enabled turns the jev tier on. Env gate: JEVKIT_COMPACT=1.
	Enabled bool
	// Shadow records would-have savings but never changes the output.
	Shadow bool
	// ThresholdBytes is the minimum combined output size before jev is asked.
	// Zero or negative selects the default (8 KiB).
	ThresholdBytes int
	// MaxLinesPerRequest caps the window passed to jev in one call. Zero or
	// negative selects the default 255.
	MaxLinesPerRequest int
	// StateDir is the directory where shadow-mode writes jev-compact.jsonl.
	// Empty disables shadow logging.
	StateDir string
}

const (
	// DefaultJevThresholdBytes matches the deterministic fallback threshold;
	// jev is only worth calling for genuinely large output.
	DefaultJevThresholdBytes = 8192
	// DefaultMaxLinesPerRequest is the 255 option cap SystemOne allows.
	DefaultMaxLinesPerRequest = 255
)

func (o JevOptions) threshold() int {
	if o.ThresholdBytes <= 0 {
		return DefaultJevThresholdBytes
	}
	return o.ThresholdBytes
}

func (o JevOptions) maxLines() int {
	if o.MaxLinesPerRequest <= 0 {
		return DefaultMaxLinesPerRequest
	}
	return o.MaxLinesPerRequest
}

// JevResult is the outcome of the ranked-line tier.
type JevResult struct {
	// Body is the assembled text. When Shadow is true or the tier aborts it is
	// the deterministic result unchanged.
	Body string
	// Used is true when jev was asked and the result was trusted.
	Used bool
	// Err records an error from the optional Jev tier. Callers which require a
	// strict fail-open policy can use it to restore the original streams.
	// Deterministic compaction is still returned as the normal fallback.
	Err error
}

// JevCompact runs the deterministic compactor first, then optionally asks jev
// to rank lines in the elided middle. It always returns a non-empty body when
// ok is true. The returned JevResult.Body is used only when it is strictly
// smaller than the input combined stream.
func JevCompact(command, stdout, stderr string, exitStatus int, asker Asker, opts JevOptions) (JevResult, Result) {
	// The jev tier is gated by opts.Enabled (equivalent to JEVKIT_COMPACT=1).
	// When disabled the output is left untouched.
	if !opts.Enabled {
		return JevResult{Body: joinStreams(stdout, stderr), Used: false}, unchanged(stdout, stderr, exitStatus, "")
	}

	// Start with the deterministic path.
	base := Compact(command, stdout, stderr, exitStatus, Options{ThresholdBytes: opts.threshold()})
	if !base.Compacted || asker == nil {
		return JevResult{Body: joinStreams(base.Stdout, base.Stderr), Used: false}, base
	}

	// Keep the deterministic result as the fail-open fallback.
	body := joinStreams(base.Stdout, base.Stderr)

	// The deterministic body is a lossy head/tail window, so it cannot be
	// used as the source for ranking. Rank the collapsed original stream and
	// retain body as the fail-open fallback.
	ranked, err := compactRanked(command, joinStreams(stdout, stderr), exitStatus, asker, opts)
	if err != nil {
		return JevResult{Body: body, Used: false, Err: err}, base
	}

	// Safety gate: ranked output must be strictly smaller than the input it
	// summarises; otherwise fall back to the deterministic result.
	combined := joinStreams(stdout, stderr)
	if len(ranked) >= len(combined) {
		return JevResult{Body: body, Used: false}, base
	}

	// Shadow mode records the savings and returns the deterministic result.
	if opts.Shadow {
		recordShadow(opts.StateDir, len(combined), len(ranked))
		return JevResult{Body: body, Used: false}, base
	}

	if !preserveGate(combined, ranked) {
		return JevResult{Body: body, Used: false}, base
	}

	return JevResult{Body: ranked, Used: true}, Result{
		Stdout: ranked, Stderr: "",
		Compacted: true, StdoutCompacted: ranked != stdout, StderrCompacted: false,
		Family: FamilyGenericLarge, Status: StatusCompacted, ExitStatus: exitStatus,
	}
}

// compactRanked tags the surviving lines of the collapsed-but-not-windowed
// original stream, asks jev to rank them, and assembles head + ranked + tail.
// Any error or untrusted response returns an error.
func compactRanked(command, original string, exit int, asker Asker, opts JevOptions) (string, error) {
	work, _ := CollapseRuns(splitLines(original))
	if len(work) <= headLines+tailLines {
		return "", fmt.Errorf("no room for ranked tier")
	}

	head := work[:headLines]
	tail := work[len(work)-tailLines:]
	middle := work[headLines : len(work)-tailLines]

	// SystemOne admits at most 255 choices. A long middle is therefore ranked
	// in consecutive windows. Tags remain global, so a selected line maps back
	// to the complete middle without ambiguity.
	windowSize := opts.maxLines()
	for start := 0; start < len(middle); start += windowSize {
		end := min(len(middle), start+windowSize)
		tagged, tagMap := tagWindow(middle[start:end], start)
		state := buildState(command, exit, head, tagged, tail)
		resp, err := asker.Ask(context.Background(), jev.Request{
			QuestionSetID: "compaction.line-relevance", State: state,
			Questions: map[string]jev.Question{
				"relevant_lines": jev.ChoiceQuestion{Instructions: "Which line of the output would a developer need in order to diagnose what happened?", Options: optionMap(tagMap)},
				"has_failure":    jev.NoulQuestion{Instructions: "Does the output contain a diagnosable failure?"},
			},
		})
		if err != nil || resp == nil || len(resp.Answers) == 0 {
			return "", fmt.Errorf("invalid jev response")
		}
		chosen, err := extractChoice(resp.Answers["relevant_lines"])
		if err != nil {
			return "", err
		}
		rankedIdx, ok := tagMap[chosen]
		if !ok {
			// This may be a selection from a later window. Continue; if no
			// window accepts it the result is untrusted and we fail open.
			continue
		}
		hasFailure := false
		if na, ok := resp.Answers["has_failure"].(jev.NoulAnswer); ok {
			hasFailure = na.Noul >= 0.5
		}
		return assembleRanked(head, middle, tail, rankedIdx, hasFailure, windowSize)
	}
	return "", fmt.Errorf("jev returned no tag in a ranking window")
}

// tagMiddle assigns L000.. labels to the first maxWindow lines of the middle
// region. It returns the labelled lines and a map from tag to original index.
func tagMiddle(middle []string, maxWindow int) ([]string, map[string]int) {
	if maxWindow <= 0 {
		maxWindow = DefaultMaxLinesPerRequest
	}
	if maxWindow > jev.MaxChoiceOptions {
		maxWindow = jev.MaxChoiceOptions
	}
	n := len(middle)
	if n > maxWindow {
		n = maxWindow
	}
	out := make([]string, n)
	idx := make(map[string]int, n)
	for i := 0; i < n; i++ {
		tag := fmt.Sprintf("L%03d", i)
		out[i] = tag + " " + middle[i]
		idx[tag] = i
	}
	return out, idx
}

func tagWindow(lines []string, offset int) ([]string, map[string]int) {
	out := make([]string, len(lines))
	idx := make(map[string]int, len(lines))
	for i, line := range lines {
		lineIndex := offset + i
		tag := fmt.Sprintf("L%03d", lineIndex)
		out[i] = tag + " " + line
		idx[tag] = lineIndex
	}
	return out, idx
}

func optionMap(tagMap map[string]int) map[string]*string {
	opts := make(map[string]*string, len(tagMap))
	for k := range tagMap {
		opts[k] = nil
	}
	return opts
}

func extractChoice(ans jev.Answer) (string, error) {
	if ans == nil {
		return "", fmt.Errorf("no relevant_lines answer")
	}
	ca, ok := ans.(jev.ChoiceAnswer)
	if !ok {
		return "", fmt.Errorf("relevant_lines not a choice")
	}
	if ca.Choice == "" {
		return "", fmt.Errorf("empty choice")
	}
	return ca.Choice, nil
}

// buildState prepares the text jev ranks. It includes the head, tagged
// middle and tail plus command and exit code for failure-aware ranking.
func buildState(command string, exit int, head, tagged, tail []string) string {
	label := strings.TrimSpace(command)
	if label == "" {
		label = "(unknown command)"
	}
	parts := []string{fmt.Sprintf("Command: %s", label), fmt.Sprintf("Exit code: %d", exit), ""}
	parts = append(parts, "HEAD:")
	parts = append(parts, head...)
	parts = append(parts, "")
	parts = append(parts, "LINES (tagged for ranking):")
	parts = append(parts, tagged...)
	parts = append(parts, "")
	parts = append(parts, "TAIL:")
	parts = append(parts, tail...)
	return strings.Join(parts, "\n")
}

// assembleRanked builds the final body: head, the chosen line (with local
// context when hasFailure is true), and tail. It preserves all original lines
// exactly so the safety-gate round trip can verify them.
func assembleRanked(head, middle, tail []string, rankedIdx int, hasFailure bool, maxWindow int) (string, error) {
	if rankedIdx < 0 || rankedIdx >= len(middle) {
		return "", fmt.Errorf("ranked index %d out of range", rankedIdx)
	}

	var chosenLines []int
	if hasFailure {
		// Keep the chosen line plus up to two neighbours on each side,
		// clamped to the window sent to jev.
		start := max(0, rankedIdx-2)
		end := min(len(middle), rankedIdx+3)
		for i := start; i < end; i++ {
			chosenLines = append(chosenLines, i)
		}
	} else {
		chosenLines = []int{rankedIdx}
	}

	body := []string{"output (ranked): " + strconv.Itoa(len(middle)) + " line(s) in elided region"}
	body = append(body, head...)
	if len(middle) > 0 {
		body = append(body, fmt.Sprintf("... (%d line(s) omitted, ranked selection below) ...", len(middle)-len(chosenLines)))
	}
	for _, i := range chosenLines {
		body = append(body, middle[i])
	}
	body = append(body, tail...)
	return strings.Join(body, "\n"), nil
}

// preserveGate verifies that every non-head, non-tail, non-marker line in the
// ranked output can be found verbatim in the original combined output. If the
// gate fails we abort to the deterministic result.
func preserveGate(original, ranked string) bool {
	origSet := make(map[string]bool)
	for _, l := range splitLines(original) {
		origSet[l] = true
	}
	for _, l := range splitLines(ranked) {
		if strings.HasPrefix(l, "output (ranked):") ||
			strings.HasPrefix(l, "... (") ||
			strings.Contains(l, " line(s) omitted") {
			continue
		}
		if !origSet[l] {
			return false
		}
	}
	return true
}

// recordShadow appends a single-line JSON record with the would-have savings.
func recordShadow(stateDir string, before, after int) {
	if stateDir == "" {
		return
	}
	rec := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"before":    before,
		"after":     after,
		"saved":     before - after,
	}
	line, _ := json.Marshal(rec)
	path := filepath.Join(stateDir, "jevkit", "jev-compact.jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	l, _ := filelock.Acquire(path + ".lock")
	if l != nil {
		defer l.Release()
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}
