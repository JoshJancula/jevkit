// Package exec runs wrapped commands and safely compacts their output.
package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	osexec "os/exec"
	"strings"

	"github.com/OWNER/jevkit/internal/compact"
)

// Options configures Run. Zero values use the operating system process
// runner and discard no output (nil writers are io.Discard).
type Options struct {
	Stdout     io.Writer
	Stderr     io.Writer
	Asker      compact.Asker
	JevOptions compact.JevOptions
	// Execute is primarily for embedding and tests. It receives exactly the
	// argv supplied to Run: no shell is involved and no quoting is rewritten.
	Execute func(context.Context, []string) (stdout, stderr []byte, exitCode int, err error)
}

// Run executes argv, prints its compacted output, and returns the wrapped
// process's exit status. It deliberately does not invoke a shell: this keeps
// argument boundaries intact on Unix and Windows.
func Run(ctx context.Context, argv []string, opts Options) int {
	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(argv) == 0 {
		_, _ = fmt.Fprintln(stderr, "jevkit exec: missing command")
		return 2
	}
	execute := opts.Execute
	if execute == nil {
		execute = runProcess
	}
	out, errOut, code, err := execute(ctx, argv)
	if err != nil {
		if code == 0 {
			code = 127
		}
		_, _ = stdout.Write(out)
		_, _ = stderr.Write(errOut)
		if len(out) == 0 && len(errOut) == 0 {
			_, _ = fmt.Fprintf(stderr, "jevkit exec: %v\n", err)
		}
		return code
	}
	command := strings.Join(argv, " ")
	if !opts.JevOptions.Enabled {
		compacted := compact.Compact(command, string(out), string(errOut), code, compact.Options{})
		_, _ = io.WriteString(stdout, compacted.Stdout)
		_, _ = io.WriteString(stderr, compacted.Stderr)
		return code
	}
	result, compacted := compact.JevCompact(command, string(out), string(errOut), code, opts.Asker, opts.JevOptions)
	if result.Err != nil {
		// Compaction is advisory. An unavailable Jev service must never hide
		// diagnostics from the command it wrapped.
		_, _ = stdout.Write(out)
		_, _ = stderr.Write(errOut)
		return code
	}
	_, _ = io.WriteString(stdout, compacted.Stdout)
	_, _ = io.WriteString(stderr, compacted.Stderr)
	return code
}

func runProcess(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0, nil
	}
	if _, ok := err.(*osexec.ExitError); ok {
		return stdout.Bytes(), stderr.Bytes(), processExitCode(cmd), nil
	}
	return stdout.Bytes(), stderr.Bytes(), 0, err
}
