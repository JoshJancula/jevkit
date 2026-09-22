package compact

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type patternClass struct {
	id, label string
	re        *regexp.Regexp
	minRun    int
}

// patternClasses are cross-ecosystem noise line shapes. A run of at least
// minRun consecutive lines of one class collapses to a single marker.
var patternClasses = []patternClass{
	{"progress", "progress/spinner", regexp.MustCompile(
		`(?i)(?:^\s*[|\\/\-]\s*$` +
			`|^\[[#=>.\s\-]+\]` +
			`|^\.\.\.\s*\d+%` +
			`|^(?:Downloading|Downloaded|Extracting|Resolving deltas|Building|` +
			`Compiling|Linking|Running \.\.\.|Remoting work)\b)`), 3},
	{"dependency", "dependency-resolution", regexp.MustCompile(
		`(?i)^(?:Collecting |Installing collected packages|` +
			`Requirement already satisfied|Looking in indexes|  Downloading|  Installing|` +
			`Using cached |Get:\d+ |Reading database|Unpacking |Preparing to unpack|` +
			`Selecting previously unselected|Fetched \d+ |Downloading from|` +
			`> Task :|mvn (?:\[INFO\]|\[WARNING\])|gradle |CMake Dep|` +
			`Scanning dependencies of target|\[\d+/\d+\] Building|` +
			`MSBUILD : warning )`), 3},
	{"warning", "compiler warning", regexp.MustCompile(
		`(?i)^\s*(?:warning|warn|\[warn\]|\[warning\]|caution)\b`), 5},
	{"test_pass", "passing test line", regexp.MustCompile(
		`(?i)^(?:PASSED\s|passed\b|test result: ok\b|` +
			`^\s*ok\s+\S+\s+[\d.]+s|^\s*--- PASS:)`), 4},
	{"stack_frame", "stack trace frame", regexp.MustCompile(
		`(?i)^(?:\s+at |\s+File ".*", line |\s+in |\s+#\d+ |\s+\^+|Caused by:|` +
			`Traceback \(most recent call last\))`), 3},
}

var (
	preserveRE = regexp.MustCompile(`(?i)error|fail|fatal|exception|not ok|FAILED|BUILD FAILED|ERROR:|panic!|` +
		`assertion failed|Test Run Failed|BUILD SUCCESS|tests? failed|` +
		`===+.*===+|SUMMARY|Successfully installed|Exit code|` +
		`Tests run:|FAIL\s+\[|FAIL\s+\S`)
	summaryRE = regexp.MustCompile(`(?i)(?:^(?:FAILED|ERROR|SUMMARY|BUILD|Tests run:|Test Run |` +
		`=\s*\d+\s+(?:passed|failed)|Successfully |` +
		`\d+ tests? (?:passed|failed)|FAIL\s|ok\s+\S+\s+[\d.]+s|` +
		`Total time:|Finished at:))`)
	errorRE = regexp.MustCompile(`(?i)error|fail|fatal|exception|not ok`)
)

// shouldPreserve marks lines that carry a diagnosis or a summary; collapse
// and windowing never drop them from a run and hoist them out of elided
// regions.
func shouldPreserve(line string) bool {
	return preserveRE.MatchString(line) || summaryRE.MatchString(line) || errorRE.MatchString(line)
}

func classOf(line string) *patternClass {
	for i := range patternClasses {
		if patternClasses[i].re.MatchString(line) {
			return &patternClasses[i]
		}
	}
	return nil
}

// CollapseRuns replaces runs of consecutive noise lines of one class with an
// omission marker and returns how many lines each class dropped. Preserved
// lines are never part of a run. Stack traces keep their first two frames.
func CollapseRuns(lines []string) ([]string, map[string]int) {
	var out []string
	omitted := map[string]int{}
	for i := 0; i < len(lines); {
		line := lines[i]
		if shouldPreserve(line) {
			out = append(out, line)
			i++
			continue
		}
		cls := classOf(line)
		if cls == nil {
			out = append(out, line)
			i++
			continue
		}
		start := i
		for i < len(lines) && !shouldPreserve(lines[i]) {
			if c := classOf(lines[i]); c == nil || c.id != cls.id {
				break
			}
			i++
		}
		run := i - start
		switch {
		case run < cls.minRun:
			out = append(out, lines[start:i]...)
		case cls.id == "stack_frame":
			keep := min(2, run)
			out = append(out, lines[start:start+keep]...)
			if drop := run - keep; drop > 0 {
				out = append(out, fmt.Sprintf("... (%d %s line(s) omitted) ...", drop, cls.label))
				omitted[cls.id] += drop
			}
		default:
			out = append(out, fmt.Sprintf("... (%d %s line(s) omitted) ...", run, cls.label))
			omitted[cls.id] += run
		}
	}
	return out, omitted
}

func classLabel(id string) string {
	for _, c := range patternClasses {
		if c.id == id {
			return c.label
		}
	}
	return id
}

// splitLines mirrors Python's str.splitlines for \n, \r\n and \r.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// Budget shape for the assembled output: head and tail lines kept verbatim,
// plus at most maxImportant preserved lines pulled out of the elided middle.
const (
	headLines    = 30
	tailLines    = 5
	maxImportant = 40
)

func summaryPrefix(command, text string, exit, nLines int, omitted map[string]int) string {
	label := strings.TrimSpace(command)
	if label == "" {
		label = "(unknown command)"
	}
	s := fmt.Sprintf("output (exit %d): %d line(s), %d byte(s) for: %s", exit, nLines, len(text), label)
	if len(omitted) > 0 {
		ids := make([]string, 0, len(omitted))
		for id := range omitted {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = fmt.Sprintf("%d %s", omitted[id], classLabel(id))
		}
		s += " (collapsed: " + strings.Join(parts, ", ") + ")"
	}
	return s
}

// assemble builds the head/tail window over collapsed lines. It reports false
// when there is nothing worth shortening.
func assemble(command, text string, exit int) (string, bool) {
	lines := splitLines(text)
	maxKept := headLines + tailLines
	if len(lines) <= maxKept {
		return "", false
	}
	work, omitted := CollapseRuns(lines)
	if len(work) <= maxKept && len(omitted) == 0 {
		return "", false
	}
	summary := summaryPrefix(command, text, exit, len(lines), omitted)
	if len(work) <= maxKept {
		return summary + "\n" + strings.Join(work, "\n"), true
	}
	head := work[:headLines]
	tail := work[len(work)-tailLines:]
	middle := work[headLines : len(work)-tailLines]
	var important []string
	for _, l := range middle {
		if shouldPreserve(l) {
			important = append(important, l)
		}
	}
	extra := max(0, len(important)-maxImportant)
	if len(important) > maxImportant {
		important = important[:maxImportant]
	}
	body := []string{summary}
	body = append(body, head...)
	if len(middle) > 0 {
		body = append(body, fmt.Sprintf("... (%d line(s) omitted) ...", len(middle)))
	}
	if len(important) > 0 {
		body = append(body, "important line(s) extracted from omitted region:")
		body = append(body, important...)
		if extra > 0 {
			body = append(body, fmt.Sprintf("... (%d more matching line(s) omitted) ...", extra))
		}
	}
	body = append(body, tail...)
	return strings.Join(body, "\n"), true
}
