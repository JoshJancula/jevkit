package app

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	ANSIReset  = "\x1b[0m"
	ANSIBold   = "\x1b[1m"
	ANSICyan   = "\x1b[1;36m"
	ANSIGreen  = "\x1b[32m"
	ANSIRed    = "\x1b[31m"
	ANSIYellow = "\x1b[33m"
)

func (a *App) ColorEnabled(w io.Writer) bool {
	if a.Getenv("NO_COLOR") != "" || a.Getenv("CLICOLOR") == "0" || a.Getenv("TERM") == "dumb" {
		return false
	}
	if a.Getenv("CLICOLOR_FORCE") != "" && a.Getenv("CLICOLOR_FORCE") != "0" {
		return true
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *App) Styled(w io.Writer, code, value string) string {
	if !a.ColorEnabled(w) {
		return value
	}
	return code + value + ANSIReset
}

func (a *App) Heading(title string) {
	a.Outf("%s\n", a.Styled(a.Stdout, ANSICyan, title))
}

func (a *App) Table(headers []string, rows [][]string) {
	colored := make([]string, len(headers))
	for i, header := range headers {
		colored[i] = a.Styled(a.Stdout, ANSICyan, header)
	}
	WriteTable(a.Stdout, colored, rows)
}

// helpText preserves Cobra's layout while making section labels and command
// names easier to scan on an interactive terminal.
func (a *App) HelpText(w io.Writer, body string) string {
	if !a.ColorEnabled(w) {
		return body
	}
	lines := strings.Split(body, "\n")
	commands := false
	for i, line := range lines {
		label := strings.TrimSpace(line)
		switch label {
		case "Usage:", "Examples:", "Available Commands:", "Commands:", "Flags:", "Global Flags:", "Additional help topics:", "Normal CLI flow:":
			lines[i] = a.Styled(w, ANSICyan, line)
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
				lines[i] = prefix + a.Styled(w, ANSIBold, name) + trimmed[len(name):]
			}
		}
	}
	return strings.Join(lines, "\n")
}
