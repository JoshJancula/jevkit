package worker

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/JoshJancula/jevkit/internal/security"
)

// environmentMarkers are output lines that mean the check could not run in the
// supervisor environment, independent of the candidate's code.
var environmentMarkers = []struct{ needle, hint string }{
	{"is not in GOROOT", "the Go toolchain on the supervisor PATH is older than this module needs"},
	{"is not in std", "the Go toolchain on the supervisor PATH is older than this module needs"},
	{"go: go.mod requires go >=", "the Go toolchain on the supervisor PATH is older than go.mod requires"},
	{"toolchain not available", "the Go toolchain go.mod asks for is not installed"},
	{"command not found", "a command the check needs is not installed or not on the supervisor PATH"},
	{"executable file not found in $PATH", "a command the check needs is not on the supervisor PATH"},
}

// classifyEnvironmentFailure reports why a failed check could not run in the
// supervisor environment, or "" when the failure may come from the candidate.
// The reason names the binary the supervisor actually resolved so an operator
// can spot a stale toolchain earlier on PATH.
func classifyEnvironmentFailure(argv []string, result security.ArgvResult, runErr error) string {
	if len(argv) == 0 || result.TimedOut {
		return ""
	}
	bin := argv[0]
	if runErr != nil && (errors.Is(runErr, exec.ErrNotFound) || strings.Contains(runErr.Error(), "executable file not found")) {
		return fmt.Sprintf("%s: command not found on the supervisor PATH", bin)
	}
	if runErr == nil && result.ExitCode == 0 {
		return ""
	}
	output := string(result.Stderr) + "\n" + string(result.Stdout)
	for _, m := range environmentMarkers {
		idx := strings.Index(output, m.needle)
		if idx < 0 {
			continue
		}
		return fmt.Sprintf("%s: %s (%s; %s)", strings.Join(argv, " "), m.hint, firstLineAround(output, idx), resolvedBinary(bin))
	}
	if result.ExitCode == 127 {
		return fmt.Sprintf("%s: exit 127, command not found (%s)", strings.Join(argv, " "), resolvedBinary(bin))
	}
	return ""
}

func resolvedBinary(bin string) string {
	path, err := exec.LookPath(bin)
	if err != nil {
		return bin + " is not on the supervisor PATH"
	}
	return bin + " resolved to " + path
}

func firstLineAround(output string, idx int) string {
	start := strings.LastIndexByte(output[:idx], '\n') + 1
	end := strings.IndexByte(output[idx:], '\n')
	line := output[start:]
	if end >= 0 {
		line = output[start : idx+end]
	}
	line = strings.TrimSpace(line)
	if len(line) > 200 {
		line = line[:200] + "…"
	}
	return line
}
