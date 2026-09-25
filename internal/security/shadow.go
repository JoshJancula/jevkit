package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
)

// recordShadow is local telemetry only. A log failure never changes the
// command's allow/deny result.
func recordShadow(stateDir, reason, runtime string) {
	if stateDir == "" {
		return
	}
	path := filepath.Join(stateDir, "jevkit", "security-shadow.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	lock, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return
	}
	defer lock.Release()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	raw, err := json.Marshal(map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"reason":    reason, "runtime": runtime,
	})
	if err == nil {
		_, _ = file.Write(append(raw, '\n'))
	}
}
