package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
)

// HookOutcome values match agents.Outcome* constants.
const (
	HookFileName = "hooks.jsonl"
)

// HookInvocation is one hooks.jsonl line recording a hook dispatch. It sits
// beside usage.jsonl under the same state directory so hook activity is part
// of local usage tracking, without polluting Jev call aggregates.
type HookInvocation struct {
	Timestamp  string `json:"timestamp"`
	Agent      string `json:"agent,omitempty"`
	Event      string `json:"event"`
	Outcome    string `json:"outcome"`
	Tool       string `json:"tool,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	LoadError  string `json:"loadError,omitempty"`
}

// HookPath is <stateDir>/jevkit/hooks.jsonl.
func HookPath(stateDir string) string {
	return filepath.Join(stateDir, "jevkit", HookFileName)
}

// AppendHook writes rec as one line under an exclusive lock. Failures are
// returned to the caller; hook runners ignore them so telemetry never breaks
// a fail-open path. An empty timestamp is filled with Now (UTC).
func AppendHook(stateDir string, rec HookInvocation) error {
	if stateDir == "" {
		return nil
	}
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if rec.DurationMs < 0 {
		rec.DurationMs = 0
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	path := HookPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	l, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer l.Release()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
