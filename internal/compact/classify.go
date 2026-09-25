package compact

import (
	"regexp"
	"strings"
)

// Family ids. Only source-output families are recognized; they exist so the
// hard passthrough can name what it protected.
const (
	FamilyGitDiff = "git_diff"
	FamilyGitShow = "git_show"
	FamilyGitLog  = "git_log"
	FamilyGrep    = "grep"
	FamilyFind    = "find"
	FamilyLS      = "ls"
	FamilyTree    = "tree"
	// FamilyGenericLarge labels output shortened by the size fallback.
	FamilyGenericLarge = "generic_large"
)

// sourceFamilies return content the caller asked for (paths, matches, diffs,
// logs) rather than progress noise. They are never rewritten; there is no
// option to re-enable compaction of them.
var sourceFamilies = map[string]bool{
	FamilyGitDiff: true, FamilyGitShow: true, FamilyGitLog: true,
	FamilyGrep: true, FamilyFind: true, FamilyLS: true, FamilyTree: true,
}

// IsSourceFamily reports whether a family id is passthrough-only.
func IsSourceFamily(family string) bool { return sourceFamilies[family] }

var (
	compoundRE = regexp.MustCompile("(?:&&|\\|\\||;|\\$\\(|\\$\\{|<\\(|>\\(|`)")
	funcDefRE  = regexp.MustCompile(`(?:^\s*function\s+\w+|^\s*\w+\s*\(\s*\)\s*\{)`)
	envAssign  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	cdPrefixRE = regexp.MustCompile(`(?i)^\s*cd\s+(?:[^\s;&|]+|"[^"]*"|'[^']*')\s*&&\s*`)
	contRE     = regexp.MustCompile(`\\\s*\n\s*`)
	trailRedir = regexp.MustCompile(`(?:\s(?:\d{1,2})?>\s*/dev/null|\s(?:\d{1,2})?>>\s*/dev/null|\s&>\s*/dev/null|\s2>&1|\s>\s*&\s*2|\s\d?>&\s*\d?)+\s*$`)
)

var reservedFirst = map[string]bool{
	"alias": true, "builtin": true, "case": true, "coproc": true, "eval": true,
	"exec": true, "for": true, "function": true, "if": true, "select": true,
	"source": true, "time": true, "until": true, "while": true,
}

var gitGlobalWithValue = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true}
var gitGlobalInline = map[string]bool{"--git-dir": true, "--work-tree": true}

// Commands whose stdout is the answer. Generic size truncation must not
// rewrite them even when no family matched. test and [ exit non-zero as a
// normal boolean answer.
var sourceBinaries = map[string]bool{
	"cat": true, "sed": true, "head": true, "tail": true, "less": true,
	"more": true, "bat": true, "jq": true, "awk": true, "cut": true,
	"diff": true, "grep": true, "rg": true, "ag": true, "find": true,
	"ls": true, "tree": true, "test": true, "[": true,
}
var sourceGitSubs = map[string]bool{"diff": true, "show": true, "log": true}
var prefixWrappers = map[string]bool{"sudo": true, "time": true, "nice": true}

// Classify returns the source-output family of a simple command, or "" when
// the command is unknown, compound, or not a source-output command.
func Classify(command string) string {
	cmd := unwrapNativeShell(command)
	if strings.TrimSpace(cmd) == "" {
		return ""
	}
	cmd = strings.TrimSpace(joinContinuations(strings.TrimSpace(cmd)))
	if shouldBail(cmd) {
		return ""
	}
	// Pipelines are never classified to a source family; classification is for
	// simple commands only.  Pipelines of source-output commands are still
	// protected by sourceOutputDenies / allowsGenericFallback.
	if strings.Contains(cmd, "|") && !strings.Contains(cmd, "||") {
		return ""
	}

	cmd = stripTrailingRedirects(cmd)
	if cmd == "" || hasUnsafeRedirect(cmd) {
		return ""
	}
	toks, ok := shlexSplit(cmd)
	if !ok || len(toks) == 0 {
		return ""
	}
	toks = normalizeTokens(toks)
	if len(toks) == 0 || reservedFirst[basename(toks[0])] || strings.ContainsAny(toks[0], "()") {
		return ""
	}
	base := basename(toks[0])
	if base == "git" && len(toks) >= 2 {
		switch toks[1] {
		case "diff":
			return FamilyGitDiff
		case "show":
			return FamilyGitShow
		case "log":
			return FamilyGitLog
		}
		return ""
	}
	switch base {
	case "grep", "rg", "egrep", "fgrep":
		return FamilyGrep
	case "find":
		return FamilyFind
	case "ls":
		return FamilyLS
	case "tree":
		return FamilyTree
	}
	return ""
}

var nativeShellRE = regexp.MustCompile(`(?s)native-shell-wrapper\.sh\b.*\b--command\s+(['"])(.+?)['"]`)

func unwrapNativeShell(command string) string {
	cmd := strings.TrimSpace(command)
	if m := nativeShellRE.FindStringSubmatch(cmd); m != nil {
		return m[2]
	}
	return cmd
}

func joinContinuations(s string) string { return contRE.ReplaceAllString(s, " ") }

func basename(tok string) string {
	if i := strings.LastIndex(tok, "/"); i >= 0 {
		return tok[i+1:]
	}
	return tok
}

// shouldBail is true for compound commands, background jobs and function
// definitions, none of which are classified.
func shouldBail(cmd string) bool {
	return compoundRE.MatchString(cmd) || hasBackground(cmd) || funcDefRE.MatchString(cmd)
}

// hasBackground finds a bare & that is not part of &&, &> or an fd dup.
func hasBackground(cmd string) bool {
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '&' {
			continue
		}
		if i > 0 && cmd[i-1] == '&' {
			continue
		}
		if i+1 < len(cmd) && (cmd[i+1] == '&' || cmd[i+1] == '>') {
			continue
		}
		return true
	}
	return false
}

func stripTrailingRedirects(cmd string) string {
	cur := strings.TrimRight(cmd, " \t\r\n")
	for {
		next := strings.TrimRight(trailRedir.ReplaceAllString(cur, ""), " \t\r\n")
		if next == cur {
			return cur
		}
		cur = next
	}
}

var redirOps = []string{"&>>", "&>", ">>", "<<", "<>", ">", "<"}

// hasUnsafeRedirect finds a redirect operator (optionally after an fd number)
// at the start or after whitespace, excluding process substitution `>(` and
// `>=`.
func hasUnsafeRedirect(cmd string) bool {
	for i := 0; i <= len(cmd); i++ {
		if i > 0 && !isSpace(cmd[i-1]) {
			continue
		}
		for d := 0; d <= 2; d++ {
			j := i + d
			if j > len(cmd) || (d > 0 && !isDigit(cmd[j-1])) {
				break
			}
			for _, op := range redirOps {
				if !strings.HasPrefix(cmd[j:], op) {
					continue
				}
				k := j + len(op)
				if k < len(cmd) && (cmd[k] == '(' || cmd[k] == '=') {
					continue
				}
				return true
			}
		}
	}
	return false
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// normalizeTokens drops leading env assignments, command/env/noglob wrappers,
// poetry run / bundle exec, and git global options so the real command leads.
func normalizeTokens(toks []string) []string {
	i := 0
	for i < len(toks) && envAssign.MatchString(toks[i]) {
		i++
	}
	toks = toks[i:]
	i = 0
loop:
	for i < len(toks) {
		switch b := basename(toks[i]); {
		case b == "command" || b == "noglob":
			i++
		case b == "env":
			i++
			for i < len(toks) && envAssign.MatchString(toks[i]) {
				i++
			}
		case b == "poetry" && i+1 < len(toks) && toks[i+1] == "run":
			i += 2
		case b == "bundle" && i+1 < len(toks) && toks[i+1] == "exec":
			i += 2
		default:
			break loop
		}
	}
	toks = toks[i:]
	if len(toks) == 0 || basename(toks[0]) != "git" {
		return toks
	}
	j := 1
	for j < len(toks) {
		opt, _, hasEq := strings.Cut(toks[j], "=")
		if !gitGlobalWithValue[opt] {
			break
		}
		if hasEq || gitGlobalInline[opt] {
			j++
			continue
		}
		if j+1 >= len(toks) {
			break
		}
		j += 2
	}
	return append([]string{toks[0]}, toks[j:]...)
}

// shlexSplit is a POSIX-mode shell word splitter (quotes and backslashes).
func shlexSplit(s string) ([]string, bool) {
	var (
		toks  []string
		cur   strings.Builder
		have  bool
		quote byte
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0:
				i++
				cur.WriteByte(s[i])
			default:
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, have = c, true
		case c == '\\':
			if i+1 >= len(s) {
				return nil, false
			}
			i++
			cur.WriteByte(s[i])
			have = true
		case isSpace(c):
			if have || cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
				have = false
			}
		case c == '#' && !have && cur.Len() == 0:
			i = len(s)
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if have || cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks, true
}

// pipelineStages splits on a bare | (leaving || alone).
func pipelineStages(cmd string) []string {
	var stages []string
	start := 0
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '|' {
			continue
		}
		if (i > 0 && cmd[i-1] == '|') || (i+1 < len(cmd) && cmd[i+1] == '|') {
			continue
		}
		if s := strings.TrimSpace(cmd[start:i]); s != "" {
			stages = append(stages, s)
		}
		start = i + 1
	}
	if s := strings.TrimSpace(cmd[start:]); s != "" {
		stages = append(stages, s)
	}
	return stages
}

func stageTokens(stage string) []string {
	toks, ok := shlexSplit(strings.TrimSpace(stage))
	if !ok || len(toks) == 0 {
		return nil
	}
	toks = normalizeTokens(toks)
	for len(toks) > 0 && prefixWrappers[basename(toks[0])] {
		toks = normalizeTokens(toks[1:])
	}
	return toks
}

func isSourceOutputTokens(toks []string) bool {
	if len(toks) == 0 {
		return false
	}
	if sourceBinaries[basename(toks[0])] {
		return true
	}
	return basename(toks[0]) == "git" && len(toks) >= 2 && sourceGitSubs[toks[1]]
}

// sourceOutputDenies is true when the first or last pipeline stage is a
// source-output command.
func sourceOutputDenies(cmd string) bool {
	stages := pipelineStages(cmd)
	if len(stages) == 0 {
		return false
	}
	return isSourceOutputTokens(stageTokens(stages[0])) || isSourceOutputTokens(stageTokens(stages[len(stages)-1]))
}

// isPurePipeline is true when the only compound operator is a bare |.
func isPurePipeline(cmd string) bool {
	if len(pipelineStages(cmd)) < 2 {
		return false
	}
	var b strings.Builder
	for i := 0; i < len(cmd); i++ {
		if cmd[i] == '|' && (i == 0 || cmd[i-1] != '|') && (i+1 == len(cmd) || cmd[i+1] != '|') {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(cmd[i])
	}
	return !shouldBail(b.String())
}

// allowsGenericFallback reports whether the size fallback may shorten output
// from this command. Empty provenance and unknown commands are eligible;
// source-output commands (cat, sed, grep, git diff/show/log, ...) are not,
// nor are && chains, $() or background jobs.
func allowsGenericFallback(command string) bool {
	cmd := strings.TrimSpace(unwrapNativeShell(command))
	if cmd == "" {
		return true
	}
	// Compound commands, background jobs and function definitions are never
	// compacted, even if they begin with a cd prefix.
	if shouldBail(cmd) {
		return false
	}
	insp := cmd
	for {
		m := cdPrefixRE.FindString(insp)
		if m == "" {
			break
		}
		insp = insp[len(m):]
	}
	insp = strings.TrimSpace(joinContinuations(insp))
	if insp == "" {
		return true
	}
	if sourceOutputDenies(insp) {
		return false
	}
	if isPurePipeline(insp) {
		return true
	}
	return !shouldBail(insp)
}
