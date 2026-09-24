// Package journal is the append-only, observability-only record of every
// event applied to a run: events.jsonl. It is never the source of truth for
// a run's current state (internal/sdlc/ledger's run.json is authoritative,
// per the plan's ledger-authoritative/journal-observability-only split) —
// engine.Apply's own determinism is what makes replaying this log a valid
// way to double-check or rebuild state, not something normal resume needs to
// do. journal deliberately knows nothing about internal/sdlc/engine's Effect
// or Event types: a caller encodes whatever it resolved into an Entry, so
// this package stays a plain, decoupled JSONL log.
package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
)

// Entry is one events.jsonl line.
type Entry struct {
	Timestamp string `json:"timestamp"`
	RunID     string `json:"runId"`
	NodeID    string `json:"nodeId"`
	// Kind names the event's shape (e.g. "WorkCompleted", "JevDecided"); the
	// caller's own vocabulary, not one journal defines or validates.
	Kind string `json:"kind"`
	// Detail is the event, encoded as JSON however the caller chooses.
	Detail json.RawMessage `json:"detail,omitempty"`
}

// FileName is the journal's file name within a run's directory.
const FileName = "events.jsonl"

// Path is <dir>/events.jsonl.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Append writes entry as one line of <dir>/events.jsonl under an exclusive
// lock, so lines from concurrent goroutines and processes never interleave.
// A missing Timestamp is filled in as now (UTC, RFC3339).
func Append(dir string, entry Entry) error {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	path := Path(dir)
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

// ReadAll returns every parseable entry in dir's events.jsonl, in file
// order. A missing file yields none; a blank or malformed line is skipped
// rather than failing the read, mirroring usage.ReadRecords — the journal is
// observability, so a single corrupt line must never make the rest of the
// history unreadable.
func ReadAll(dir string) ([]Entry, error) {
	f, err := os.Open(Path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []Entry
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var e Entry
			if json.Unmarshal(line, &e) == nil {
				out = append(out, e)
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}
