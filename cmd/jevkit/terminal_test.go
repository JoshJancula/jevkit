package main

import (
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
)

func TestHelpColorRespectsTerminalSettings(t *testing.T) {
	a := newApp(t)
	if code, out, err := run(a, "", "--help"); code != app.ExitOK || strings.Contains(out, "\x1b[") {
		t.Fatalf("plain help: %d %q %q", code, out, err)
	}
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	if code, out, err := run(a, "", "sdlc", "--help"); code != app.ExitOK || !strings.Contains(out, app.ANSICyan+"Available Commands:"+app.ANSIReset) || !strings.Contains(out, app.ANSIBold+"agents"+app.ANSIReset) || strings.Contains(out, app.ANSICyan+"agents"+app.ANSIReset) {
		t.Fatalf("colored help: %d %q %q", code, out, err)
	}
	a.Environ = append(a.Environ, "NO_COLOR=1")
	if code, out, err := run(a, "", "sdlc", "--help"); code != app.ExitOK || strings.Contains(out, "\x1b[") {
		t.Fatalf("NO_COLOR help: %d %q %q", code, out, err)
	}
}

func TestRedactSubcommandHelpHasUsageAndGoesToStdout(t *testing.T) {
	a := newApp(t)
	for _, name := range []string{"list", "add", "audit"} {
		code, out, err := run(a, "", "redact", name, "--help")
		if code != app.ExitOK || err != "" || !strings.Contains(out, "Usage:\n  jevkit redact "+name) {
			t.Fatalf("redact %s help: %d %q %q", name, code, out, err)
		}
	}
}

func TestColoredAgentTableStaysAligned(t *testing.T) {
	a := newApp(t)
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	code, out, err := run(a, "", "sdlc", "agents")
	if code != app.ExitOK || !strings.Contains(out, app.ANSICyan+"YOUR AGENTS"+app.ANSIReset) {
		t.Fatalf("agents: %d %q %q", code, out, err)
	}
	width := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│") {
			continue
		}
		if width == 0 {
			width = app.TextWidth(line)
		} else if app.TextWidth(line) != width {
			t.Fatalf("table row misaligned: width %d, got %d: %q", width, app.TextWidth(line), line)
		}
	}
	if width == 0 {
		t.Fatal("no table found")
	}
}
