package main

import (
	"strings"
	"testing"
)

func TestHelpColorRespectsTerminalSettings(t *testing.T) {
	a := newApp(t)
	if code, out, err := run(a, "", "--help"); code != exitOK || strings.Contains(out, "\x1b[") {
		t.Fatalf("plain help: %d %q %q", code, out, err)
	}
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	if code, out, err := run(a, "", "sdlc", "--help"); code != exitOK || !strings.Contains(out, ansiCyan+"Available Commands:"+ansiReset) || !strings.Contains(out, ansiBold+"agents"+ansiReset) || strings.Contains(out, ansiCyan+"agents"+ansiReset) {
		t.Fatalf("colored help: %d %q %q", code, out, err)
	}
	a.Environ = append(a.Environ, "NO_COLOR=1")
	if code, out, err := run(a, "", "sdlc", "--help"); code != exitOK || strings.Contains(out, "\x1b[") {
		t.Fatalf("NO_COLOR help: %d %q %q", code, out, err)
	}
}

func TestRedactSubcommandHelpHasUsageAndGoesToStdout(t *testing.T) {
	a := newApp(t)
	for _, name := range []string{"list", "add", "audit"} {
		code, out, err := run(a, "", "redact", name, "--help")
		if code != exitOK || err != "" || !strings.Contains(out, "Usage:\n  jevkit redact "+name) {
			t.Fatalf("redact %s help: %d %q %q", name, code, out, err)
		}
	}
}

func TestColoredAgentTableStaysAligned(t *testing.T) {
	a := newApp(t)
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	code, out, err := run(a, "", "sdlc", "agents")
	if code != exitOK || !strings.Contains(out, ansiCyan+"YOUR AGENTS"+ansiReset) {
		t.Fatalf("agents: %d %q %q", code, out, err)
	}
	width := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│") {
			continue
		}
		if width == 0 {
			width = textWidth(line)
		} else if textWidth(line) != width {
			t.Fatalf("table row misaligned: width %d, got %d: %q", width, textWidth(line), line)
		}
	}
	if width == 0 {
		t.Fatal("no table found")
	}
}
