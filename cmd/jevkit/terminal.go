package main

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiCyan   = "\x1b[1;36m"
	ansiGreen  = "\x1b[32m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
)

func (a *App) colorEnabled(w io.Writer) bool {
	if a.getenv("NO_COLOR") != "" || a.getenv("CLICOLOR") == "0" || a.getenv("TERM") == "dumb" {
		return false
	}
	if a.getenv("CLICOLOR_FORCE") != "" && a.getenv("CLICOLOR_FORCE") != "0" {
		return true
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *App) styled(w io.Writer, code, value string) string {
	if !a.colorEnabled(w) {
		return value
	}
	return code + value + ansiReset
}

func (a *App) heading(title string) {
	a.outf("%s\n", a.styled(a.Stdout, ansiCyan, title))
}

func (a *App) table(headers []string, rows [][]string) {
	colored := make([]string, len(headers))
	for i, header := range headers {
		colored[i] = a.styled(a.Stdout, ansiCyan, header)
	}
	writeTable(a.Stdout, colored, rows)
}

// helpText preserves Cobra's layout while making section labels and command
// names easier to scan on an interactive terminal.
func (a *App) helpText(w io.Writer, body string) string {
	if !a.colorEnabled(w) {
		return body
	}
	lines := strings.Split(body, "\n")
	commands := false
	for i, line := range lines {
		label := strings.TrimSpace(line)
		switch label {
		case "Usage:", "Examples:", "Available Commands:", "Commands:", "Flags:", "Global Flags:", "Additional help topics:", "Normal CLI flow:":
			lines[i] = a.styled(w, ansiCyan, line)
			commands = label == "Available Commands:"
			continue
		}
		if commands {
			if line == "" {
				commands = false
				continue
			}
			trimmed := strings.TrimLeft(line, " ")
			name, _, found := strings.Cut(trimmed, " ")
			if found && name != "" {
				prefix := line[:len(line)-len(trimmed)]
				lines[i] = prefix + a.styled(w, ansiBold, name) + trimmed[len(name):]
			}
		}
	}
	return strings.Join(lines, "\n")
}
