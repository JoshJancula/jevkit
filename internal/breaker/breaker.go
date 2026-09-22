// Package breaker is a circuit breaker persisted in a state file, so short
// lived processes (hooks, CLI invocations) share one view of it.
//
// Policy (same as the ralph bash/python transports):
//   - two consecutive failures open the breaker;
//   - HTTP 401/422 (config errors) open it immediately via Open;
//   - success resets the consecutive counter, but only while closed;
//   - once open it stays open until Reset.
//
// Every mutation holds an exclusive file lock across read-modify-write and
// replaces the state file by atomic rename, so concurrent processes never
// lose an update and readers never see a torn file. Persistence is best
// effort: I/O errors never fail the caller; an unreadable file reads closed.
package breaker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
)

const (
	dirName  = "jevkit"
	fileName = "breaker.json"
	lockName = "breaker.lock"

	// FailureThreshold is the consecutive-failure count that opens the breaker.
	FailureThreshold = 2
)

// State is the persisted breaker record.
type State struct {
	Open        bool      `json:"open"`
	Consecutive int       `json:"consecutive"`
	Reason      string    `json:"reason,omitempty"`
	OpenedAt    time.Time `json:"opened_at,omitzero"`
}

// Breaker is a file-backed circuit breaker. It satisfies jev.Breaker.
type Breaker struct {
	dir string
	now func() time.Time
}

// New returns a Breaker whose files live in <stateDir>/jevkit.
func New(stateDir string) *Breaker {
	return &Breaker{dir: filepath.Join(stateDir, dirName), now: time.Now}
}

// Path is the state file location.
func (b *Breaker) Path() string { return filepath.Join(b.dir, fileName) }

// Load returns the current state; a missing or corrupt file reads as closed.
func (b *Breaker) Load() State {
	data, err := os.ReadFile(b.Path())
	if err != nil {
		return State{}
	}
	var s State
	if json.Unmarshal(data, &s) != nil || s.Consecutive < 0 {
		return State{}
	}
	return s
}

// IsOpen reports whether the breaker is open.
func (b *Breaker) IsOpen() bool { return b.Load().Open }

// Open force-opens the breaker. It is a no-op when already open.
func (b *Breaker) Open(reason string) {
	b.update(func(s *State) {
		if !s.Open {
			s.open(reason, b.now())
		}
	})
}

// RecordFailure counts a failure and opens after FailureThreshold in a row.
// It is silent once open.
func (b *Breaker) RecordFailure(reason string) {
	b.update(func(s *State) {
		if s.Open {
			return
		}
		s.Consecutive++
		if s.Consecutive >= FailureThreshold {
			s.open(reason, b.now())
		}
	})
}

// RecordSuccess resets the failure counter. It does not close an open breaker.
func (b *Breaker) RecordSuccess() {
	b.update(func(s *State) {
		if !s.Open {
			s.Consecutive = 0
		}
	})
}

// Reset closes the breaker and clears the counter.
func (b *Breaker) Reset() {
	b.update(func(s *State) { *s = State{} })
}

func (s *State) open(reason string, at time.Time) {
	s.Open, s.Reason, s.OpenedAt = true, reason, at.UTC()
}

func (b *Breaker) update(fn func(*State)) {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return
	}
	l, err := filelock.Acquire(filepath.Join(b.dir, lockName))
	if err != nil {
		return
	}
	defer l.Release()

	s := b.Load()
	before := s
	fn(&s)
	if s == before {
		if _, err := os.Stat(b.Path()); err == nil {
			return
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(b.dir, fileName+".tmp.*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), b.Path()) != nil {
		_ = os.Remove(tmp.Name())
	}
}
