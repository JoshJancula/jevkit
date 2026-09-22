package compact

import "strings"

// DefaultThresholdBytes is the combined output size above which the size
// fallback runs.
const DefaultThresholdBytes = 8192

// Status values.
const (
	StatusCompacted    = "compacted"
	StatusNotCompacted = "not compacted"
)

// Options tunes Compact. The zero value uses the defaults.
type Options struct {
	// ThresholdBytes is the combined stdout+stderr size at or below which
	// output is returned unchanged. Zero or negative selects the default.
	ThresholdBytes int
	// Policy can further restrict or tune eligibility. It cannot override hard
	// source/binary protections, which are checked first.
	Policy *Policy
}

// Result is the outcome of Compact.
type Result struct {
	Stdout          string
	Stderr          string
	Compacted       bool
	StdoutCompacted bool
	StderrCompacted bool
	// Family is the source-output family that forced passthrough, or
	// FamilyGenericLarge when the size fallback shortened the output.
	Family     string
	Status     string
	ExitStatus int
}

func (o Options) threshold() int {
	if o.ThresholdBytes <= 0 {
		return DefaultThresholdBytes
	}
	return o.ThresholdBytes
}

func unchanged(stdout, stderr string, exit int, family string) Result {
	return Result{Stdout: stdout, Stderr: stderr, Family: family, Status: StatusNotCompacted, ExitStatus: exit}
}

// Compact shortens oversized command output deterministically.
//
//   - Output containing a NUL byte is binary and passes through.
//   - Source-output families (git diff/show/log, grep/rg, find, ls, tree) pass
//     through verbatim, by command or, when the command is empty, by output
//     shape. Nothing re-enables compaction of them.
//   - Source-output commands in general (cat, sed, jq, ...), && chains,
//     $() and background jobs are also left alone.
//   - Otherwise, output over the byte threshold has runs of repeated noise
//     lines collapsed and is windowed to head and tail lines plus the
//     error/summary lines from the elided middle. The result is used only if
//     it is strictly smaller than the input.
func Compact(command, stdout, stderr string, exitStatus int, opts Options) Result {
	if strings.ContainsRune(stdout, 0) || strings.ContainsRune(stderr, 0) {
		return unchanged(stdout, stderr, exitStatus, "")
	}
	combined := joinStreams(stdout, stderr)

	family := Classify(command)
	if family == "" && strings.TrimSpace(command) == "" {
		family = detectSourceShape(combined)
	}
	if IsSourceFamily(family) {
		return unchanged(stdout, stderr, exitStatus, family)
	}
	if rule := opts.Policy.Match(command, combined); rule != nil {
		if rule.Action == ActionNever {
			return unchanged(stdout, stderr, exitStatus, "")
		}
		if rule.Threshold > 0 {
			opts.ThresholdBytes = rule.Threshold
		}
	}
	if len(combined) <= opts.threshold() || !allowsGenericFallback(command) {
		return unchanged(stdout, stderr, exitStatus, "")
	}
	body, ok := assemble(command, combined, exitStatus)
	if !ok {
		return unchanged(stdout, stderr, exitStatus, "")
	}
	outOut, outErr := body, ""
	if strings.TrimSpace(stdout) == "" {
		outOut, outErr = "", body
	}
	if len(joinStreams(outOut, outErr)) >= len(combined) {
		return unchanged(stdout, stderr, exitStatus, "")
	}
	r := Result{
		Stdout: outOut, Stderr: outErr,
		StdoutCompacted: outOut != stdout, StderrCompacted: outErr != stderr,
		Family: FamilyGenericLarge, Status: StatusCompacted, ExitStatus: exitStatus,
	}
	r.Compacted = r.StdoutCompacted || r.StderrCompacted
	return r
}

func joinStreams(stdout, stderr string) string {
	if stdout != "" && stderr != "" {
		return strings.TrimRight(stdout, "\n") + "\n" + stderr
	}
	if stdout != "" {
		return stdout
	}
	return stderr
}
