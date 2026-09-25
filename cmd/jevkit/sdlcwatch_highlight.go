package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// ttyPrestyled marks a pane row that already carries this file's ANSI styling.
// A NUL can never survive ttySafeLine or ttyClean, so runtime text cannot forge
// the marker; ttyBox trusts only rows that start with it.
const ttyPrestyled = "\x00"

const (
	hlKeyword  = "\x1b[35m"
	hlString   = "\x1b[32m"
	hlNumber   = "\x1b[33m"
	hlFunc     = "\x1b[34m"
	hlType     = "\x1b[36m"
	hlComment  = "\x1b[90m"
	hlFgReset  = "\x1b[39m"
	hlDim      = "\x1b[2m"
	hlUndim    = "\x1b[22m"
	hlAddBg    = "\x1b[48;5;22m"
	hlDelBg    = "\x1b[48;5;52m"
	hlAddSign  = "\x1b[1;92m"
	hlDelSign  = "\x1b[1;91m"
	hlHunk     = "\x1b[36m"
	hlFileHead = "\x1b[1;33m"
	hlPath     = "\x1b[35m"
	hlLineNo   = "\x1b[32m"
)

type hlLang struct {
	keywords, types, constants map[string]bool
	lineComment                []string
	blockComment               bool
	backtick                   bool
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var (
	cConsts = words("true false null nil None True False undefined NaN")
	hlCLike = hlLang{keywords: words("break case catch class const continue default do else enum export extends finally for function if import in instanceof let new return static super switch this throw try typeof var void while yield async await of interface type implements package private protected public readonly abstract declare namespace as from struct union sizeof typedef extern goto volatile unsigned signed inline template typename virtual override final fn impl trait mut pub use mod match loop where crate self Self dyn ref move unsafe"), types: words("int long short char float double bool boolean void string number any unknown never object byte i8 i16 i32 i64 u8 u16 u32 u64 f32 f64 usize isize str String Vec Option Result"), constants: cConsts, lineComment: []string{"//"}, blockComment: true, backtick: true}
	hlLangs = map[string]hlLang{
		"go":     {keywords: words("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var"), types: words("bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr any comparable"), constants: words("true false nil iota"), lineComment: []string{"//"}, blockComment: true, backtick: true},
		"python": {keywords: words("and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield match case self"), types: words("int float str bool list dict set tuple bytes object type"), constants: words("True False None"), lineComment: []string{"#"}},
		"shell":  {keywords: words("if then else elif fi for while until do done case esac in function return local export readonly set unset shift exit echo cd source"), constants: words("true false"), lineComment: []string{"#"}},
		"ruby":   {keywords: words("def end if elsif else unless while until for in do return class module require include extend yield begin rescue ensure then case when self"), constants: words("true false nil"), lineComment: []string{"#"}},
		"yaml":   {constants: words("true false null yes no on off ~"), lineComment: []string{"#"}},
		"toml":   {constants: words("true false"), lineComment: []string{"#"}},
		"json":   {constants: words("true false null")},
		"sql":    {keywords: words("select from where insert into values update set delete create table drop alter index join left right inner outer on group by order having limit offset and or not null as distinct union primary key references SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE DROP ALTER INDEX JOIN LEFT RIGHT INNER OUTER ON GROUP BY ORDER HAVING LIMIT OFFSET AND OR NOT NULL AS DISTINCT UNION PRIMARY KEY REFERENCES"), lineComment: []string{"--"}, blockComment: true},
		"clike":  hlCLike,
	}
	hlExtLang = map[string]string{
		".go": "go", ".py": "python", ".sh": "shell", ".bash": "shell", ".zsh": "shell", ".ps1": "shell", ".rb": "ruby",
		".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".json": "json", ".jsonl": "json", ".sql": "sql", ".md": "markdown",
		".js": "clike", ".jsx": "clike", ".ts": "clike", ".tsx": "clike", ".mjs": "clike", ".cjs": "clike", ".rs": "clike",
		".c": "clike", ".h": "clike", ".cc": "clike", ".cpp": "clike", ".hpp": "clike", ".java": "clike", ".kt": "clike",
		".swift": "clike", ".cs": "clike", ".scala": "clike", ".php": "clike", ".dart": "clike",
	}
	hlFenceLang = map[string]string{"go": "go", "golang": "go", "python": "python", "py": "python", "bash": "shell", "sh": "shell", "shell": "shell", "zsh": "shell", "console": "shell", "yaml": "yaml", "yml": "yaml", "toml": "toml", "json": "json", "sql": "sql", "ruby": "ruby", "markdown": "markdown", "md": "markdown", "js": "clike", "javascript": "clike", "ts": "clike", "typescript": "clike", "tsx": "clike", "jsx": "clike", "rust": "clike", "c": "clike", "cpp": "clike", "java": "clike"}
	// path:line:code (rg/grep -n) or Cursor's "path:line  code".
	hlGrepLine = regexp.MustCompile(`^([^\s:]+\.[A-Za-z0-9]+):(\d+)([:-]|  )`)
	// "12: code", "  12→code" or "12\tcode" as printed by read tools and cat -n.
	hlNumbered = regexp.MustCompile(`^(\s*\d+)(: |→|\t| {2,})`)
)

func hlLangForPath(path string) string {
	path = strings.Trim(path, `"'()[]{}<>,;`)
	return hlExtLang[strings.ToLower(filepath.Ext(path))]
}

// hlLangForTitle picks the language named by a detail title, such as a read
// path or a command's file arguments. Mixed languages yield no guess.
func hlLangForTitle(title string) string {
	found := ""
	for _, field := range strings.FieldsFunc(title, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(`"'=,;|&()`, r) }) {
		if lang := hlLangForPath(field); lang != "" {
			if found != "" && found != lang {
				return ""
			}
			found = lang
		}
	}
	return found
}

// highlightCode colors one line of source. It emits only foreground changes so
// a diff row's background tint survives every token.
func highlightCode(line, lang string) string {
	if lang == "markdown" {
		return highlightMarkdown(line)
	}
	spec, ok := hlLangs[lang]
	if !ok || line == "" {
		return line
	}
	var b strings.Builder
	paint := func(code, s string) { b.WriteString(code + s + hlFgReset) }
	runes := []rune(line)
	for i := 0; i < len(runes); {
		rest := string(runes[i:])
		comment := false
		for _, marker := range spec.lineComment {
			if strings.HasPrefix(rest, marker) && (marker != "#" || i == 0 || unicode.IsSpace(runes[i-1])) {
				comment = true
			}
		}
		if comment {
			paint(hlComment, rest)
			break
		}
		if spec.blockComment && strings.HasPrefix(rest, "/*") {
			end := strings.Index(rest[2:], "*/")
			if end < 0 {
				paint(hlComment, rest)
				break
			}
			seg := rest[:end+4]
			paint(hlComment, seg)
			i += utf8.RuneCountInString(seg)
			continue
		}
		r := runes[i]
		if r == '"' || r == '\'' || (r == '`' && spec.backtick) {
			j := i + 1
			for j < len(runes) && runes[j] != r {
				if runes[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(runes))
			code := hlString
			if lang == "json" || lang == "yaml" {
				if k := hlSkipSpace(runes, j); k < len(runes) && runes[k] == ':' {
					code = hlFunc
				}
			}
			paint(code, string(runes[i:j]))
			i = j
			continue
		}
		if unicode.IsDigit(r) {
			j := i
			for j < len(runes) && (unicode.IsDigit(runes[j]) || unicode.IsLetter(runes[j]) || runes[j] == '.' || runes[j] == '_') {
				j++
			}
			paint(hlNumber, string(runes[i:j]))
			i = j
			continue
		}
		if unicode.IsLetter(r) || r == '_' || (r == '$' && lang == "shell") {
			j := i + 1
			for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
				j++
			}
			word := string(runes[i:j])
			switch {
			case spec.keywords[word]:
				paint(hlKeyword, word)
			case spec.constants[word]:
				paint(hlNumber, word)
			case spec.types[word]:
				paint(hlType, word)
			case r == '$':
				paint(hlType, word)
			case lang == "yaml" && i == hlSkipSpace(runes, 0) && j < len(runes) && runes[j] == ':':
				paint(hlFunc, word)
			case j < len(runes) && runes[j] == '(' && lang != "yaml" && lang != "json":
				paint(hlFunc, word)
			default:
				b.WriteString(word)
			}
			i = j
			continue
		}
		b.WriteRune(r)
		i++
	}
	return b.String()
}

func hlSkipSpace(runes []rune, i int) int {
	for i < len(runes) && runes[i] == ' ' {
		i++
	}
	return i
}

func highlightMarkdown(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(trimmed)]
	switch {
	case strings.HasPrefix(trimmed, "#"):
		return "\x1b[1;36m" + line + hlFgReset + "\x1b[22m"
	case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
		return indent + hlKeyword + trimmed[:1] + hlFgReset + highlightInlineCode(trimmed[1:])
	case strings.HasPrefix(trimmed, "> "):
		return hlComment + line + hlFgReset
	}
	return highlightInlineCode(line)
}

func highlightInlineCode(s string) string {
	parts := strings.Split(s, "`")
	if len(parts) < 3 {
		return s
	}
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			b.WriteString(hlNumber + "`" + part + "`" + hlFgReset)
		} else {
			if i%2 == 1 {
				b.WriteString("`")
			}
			b.WriteString(part)
		}
	}
	return b.String()
}

// watchRowKind is how a detail row renders: prose, source, grep hit, or diff.
type watchRowKind int

const (
	rowText watchRowKind = iota
	rowCode
	rowGrep
	rowNumbered
	rowDiffAdd
	rowDiffDel
	rowDiffContext
	rowDiffHunk
	rowDiffFile
	rowDiffMeta
	rowError
	rowDim
)

type watchRow struct {
	kind  watchRowKind
	lang  string
	plain string
}

// watchDetailRows wraps detail text to width and, with color, returns rows
// prefixed with ttyPrestyled. Classification runs on whole source lines so a
// wrapped continuation keeps its diff tint and language.
func watchDetailRows(title string, detail []string, width int, color bool) []watchRow {
	lang := hlLangForTitle(title)
	fence := ""
	inDiff := strings.Contains(title, "diff")
	var rows []watchRow
	for i, raw := range detail {
		line := ttySafeLine(raw)
		kind, rowLang := rowText, lang
		body := line
		code := strings.HasPrefix(line, "┃ ")
		if code {
			body = strings.TrimPrefix(line, "┃ ")
			if fenceBody := strings.TrimSpace(body); strings.HasPrefix(fenceBody, "```") {
				if fence == "" {
					fence = hlFenceLang[strings.ToLower(strings.TrimPrefix(fenceBody, "```"))]
					if fence == "" {
						fence = "-"
					}
					if strings.TrimPrefix(fenceBody, "```") == "diff" {
						inDiff = true
					}
				} else {
					fence = ""
				}
				rows = append(rows, watchRow{kind: rowDim, plain: line})
				continue
			}
			if fence != "" && fence != "-" {
				rowLang = fence
			}
		}
		switch {
		case strings.HasPrefix(body, "diff --git "):
			inDiff, kind = true, rowDiffFile
			if fields := strings.Fields(body); len(fields) >= 4 {
				lang = hlLangForPath(fields[len(fields)-1])
			}
		case strings.HasPrefix(body, "--- ") && i+1 < len(detail) && strings.HasPrefix(strings.TrimPrefix(ttySafeLine(detail[i+1]), "┃ "), "+++ "):
			inDiff, kind = true, rowDiffFile
		case inDiff && strings.HasPrefix(body, "+++ "):
			kind = rowDiffFile
			if l := hlLangForPath(strings.TrimPrefix(body, "+++ ")); l != "" {
				lang = l
			}
		case strings.HasPrefix(body, "@@ ") || body == "@@":
			inDiff, kind = true, rowDiffHunk
		case inDiff && strings.HasPrefix(body, "+"):
			kind = rowDiffAdd
		case inDiff && strings.HasPrefix(body, "-"):
			kind = rowDiffDel
		case inDiff && strings.HasPrefix(body, " "):
			kind = rowDiffContext
		case inDiff && (strings.HasPrefix(body, "index ") || strings.HasPrefix(body, "new file") || strings.HasPrefix(body, "deleted file") || strings.HasPrefix(body, `\ `) || strings.HasPrefix(body, "similarity ") || strings.HasPrefix(body, "rename ")):
			kind = rowDiffMeta
		case strings.HasPrefix(body, "Error:"), strings.HasPrefix(body, "stderr:"), strings.HasPrefix(body, "error:"), strings.HasPrefix(body, "fatal:"):
			kind = rowError
		case strings.HasPrefix(body, "… "):
			kind = rowDim
		case hlGrepLine.MatchString(body):
			kind = rowGrep
		case rowLang != "" && hlNumbered.MatchString(body):
			kind = rowNumbered
		case code || rowLang != "":
			kind = rowCode
		}
		if kind == rowDiffAdd || kind == rowDiffDel || kind == rowDiffContext {
			rowLang = lang
		}
		if kind == rowText && !inDiff {
			rowLang = "markdown"
		}
		for _, piece := range watchWrapHanging(line, width, kind) {
			rows = append(rows, watchRow{kind: kind, lang: rowLang, plain: piece})
		}
	}
	if !color {
		return rows
	}
	for i := range rows {
		rows[i].plain = ttyPrestyled + styleWatchRow(rows, i, width)
	}
	return rows
}

// watchWrapHanging indents the continuation of a wrapped grep hit or diff
// line so it cannot be mistaken for the next source line.
func watchWrapHanging(line string, width int, kind watchRowKind) []string {
	hang := ""
	switch kind {
	case rowGrep, rowNumbered:
		hang = "    "
	case rowDiffAdd, rowDiffDel, rowDiffContext:
		hang = " "
	}
	pieces := ttyWrapPreserve(line, width)
	if hang == "" || len(pieces) < 2 || width <= 2*len(hang) {
		return pieces
	}
	bar := ""
	if strings.HasPrefix(line, "┃ ") {
		bar = "┃ "
	}
	rest := strings.TrimPrefix(line, pieces[0])
	if rest == line {
		return pieces
	}
	rest = strings.TrimLeft(rest, " ")
	if rest == "" {
		return pieces[:1]
	}
	// ttyWrapPreserve repeats a line's leading indent on every continuation.
	more := ttyWrapPreserve(bar+hang+rest, width)
	return append(pieces[:1], more...)
}

func styleWatchRow(rows []watchRow, i, width int) string {
	row := rows[i]
	line := row.plain
	continuation := i > 0 && rows[i-1].kind == row.kind && row.kind != rowText && !hlStartsRow(row)
	bar := ""
	if strings.HasPrefix(line, "┃ ") {
		bar, line = hlDim+"┃"+hlUndim+" ", strings.TrimPrefix(line, "┃ ")
	}
	pad := func(s string) string {
		return s + strings.Repeat(" ", max(0, width-textWidth(bar+s)))
	}
	switch row.kind {
	case rowDiffAdd, rowDiffDel:
		bg, signCode := hlAddBg, hlAddSign
		if row.kind == rowDiffDel {
			bg, signCode = hlDelBg, hlDelSign
		}
		styled := highlightCode(line, row.lang)
		if !continuation && line != "" {
			styled = signCode + line[:1] + "\x1b[22m" + hlFgReset + highlightCode(line[1:], row.lang)
		}
		return bar + bg + pad(styled) + ansiReset
	case rowDiffContext:
		if !continuation && line != "" {
			return bar + hlDim + line[:1] + hlUndim + highlightCode(line[1:], row.lang) + ansiReset
		}
		return bar + highlightCode(line, row.lang) + ansiReset
	case rowDiffHunk:
		return bar + hlHunk + line + ansiReset
	case rowDiffFile:
		return bar + hlFileHead + line + ansiReset
	case rowDiffMeta, rowDim:
		return bar + hlDim + line + ansiReset
	case rowError:
		return bar + ansiRed + line + ansiReset
	case rowGrep:
		if m := hlGrepLine.FindStringSubmatchIndex(line); m != nil && !continuation {
			path, num, sep := line[m[2]:m[3]], line[m[4]:m[5]], line[m[6]:m[7]]
			return bar + hlPath + path + hlFgReset + hlDim + ":" + hlUndim + hlLineNo + num + hlFgReset + hlDim + sep + hlUndim + highlightCode(line[m[1]:], hlLangForPath(path)) + ansiReset
		}
		return bar + highlightCode(line, hlLangForPath(hlGrepPath(rows, i))) + ansiReset
	case rowNumbered:
		if m := hlNumbered.FindStringSubmatchIndex(line); m != nil && !continuation {
			return bar + hlDim + line[:m[1]] + hlUndim + highlightCode(line[m[1]:], row.lang) + ansiReset
		}
		return bar + highlightCode(line, row.lang) + ansiReset
	case rowCode:
		return bar + highlightCode(line, row.lang) + ansiReset
	}
	return bar + highlightCode(line, row.lang) + ansiReset
}

// hlStartsRow reports whether a wrapped piece begins a new source line rather
// than continuing the previous one.
func hlStartsRow(row watchRow) bool {
	body := strings.TrimPrefix(row.plain, "┃ ")
	switch row.kind {
	case rowDiffAdd:
		return strings.HasPrefix(body, "+")
	case rowDiffDel:
		return strings.HasPrefix(body, "-")
	case rowGrep:
		return hlGrepLine.MatchString(body)
	case rowNumbered:
		return hlNumbered.MatchString(body)
	}
	return true
}

func hlGrepPath(rows []watchRow, i int) string {
	for ; i >= 0; i-- {
		if m := hlGrepLine.FindStringSubmatch(strings.TrimPrefix(rows[i].plain, "┃ ")); m != nil {
			return m[1]
		}
	}
	return ""
}

// editDiffLines renders an Edit/Write tool call as a unified-style diff. The
// common head and tail stay as context so the changed span reads in place.
func editDiffLines(path, oldText, newText string) []string {
	a, b := splitLines(oldText), splitLines(newText)
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	out := []string{"--- " + path, "+++ " + path, "@@"}
	for _, l := range a[max(0, head-diffContext):head] {
		out = append(out, " "+l)
	}
	for _, l := range a[head : len(a)-tail] {
		out = append(out, "-"+l)
	}
	for _, l := range b[head : len(b)-tail] {
		out = append(out, "+"+l)
	}
	for _, l := range a[len(a)-tail : min(len(a), len(a)-tail+diffContext)] {
		out = append(out, " "+l)
	}
	if len(out) > 400 {
		out = append(out[:400], "… more in sdlc logs")
	}
	return out
}

type watchDiffKey struct {
	dir, path string
	mod       time.Time
	size      int64
}

var watchDiffCache = struct {
	sync.Mutex
	m map[watchDiffKey][]string
}{m: map[watchDiffKey][]string{}}

const maxWatchDiffLines = 300

// watchWorkDiff shows each changed file's current diff against HEAD. Runtimes
// such as Codex report only paths for an edit, so the watcher asks Git. The
// result is cached per file state because the view redraws every second.
func watchWorkDiff(workDir string, paths []string) []string {
	var out []string
	for _, path := range paths {
		if len(out) >= maxWatchDiffLines*2 {
			out = append(out, "… more changes in the workspace")
			break
		}
		key := watchDiffKey{dir: workDir, path: path}
		if info, err := os.Stat(filepath.Join(workDir, path)); err == nil {
			key.mod, key.size = info.ModTime(), info.Size()
		}
		watchDiffCache.Lock()
		cached, ok := watchDiffCache.m[key]
		watchDiffCache.Unlock()
		if !ok {
			cached = gitFileDiff(workDir, path, key.size > 0 || !key.mod.IsZero())
			watchDiffCache.Lock()
			if len(watchDiffCache.m) > 256 {
				clear(watchDiffCache.m)
			}
			watchDiffCache.m[key] = cached
			watchDiffCache.Unlock()
		}
		out = append(out, cached...)
	}
	return out
}

func gitFileDiff(workDir, path string, exists bool) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off", "--no-pager"}, args...)...)
		cmd.Dir = workDir
		return cmd.Output()
	}
	raw, err := git("diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=3", "HEAD", "--", path)
	if err != nil {
		return nil
	}
	if len(raw) == 0 && exists {
		if _, err := git("ls-files", "--error-unmatch", "--", path); err != nil {
			// Untracked: --no-index exits 1 whenever the files differ.
			raw, _ = git("diff", "--no-index", "--no-color", "--unified=3", "--", os.DevNull, path)
		}
	}
	if len(raw) > 64<<10 {
		raw = raw[:64<<10]
	}
	lines := splitLines(string(raw))
	if len(lines) > maxWatchDiffLines {
		lines = append(lines[:maxWatchDiffLines], "… more of "+path+" in the workspace")
	}
	return lines
}
