package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OWNER/jevkit/internal/filelock"
)

// Decision is recorded evidence about a routing or lifecycle choice. Detail is
// display text, never a claim to contain Jev's private reasoning.
type Decision struct {
	At         string            `json:"at"`
	RunID      string            `json:"runId"`
	Kind       string            `json:"kind"`
	Stage      string            `json:"stage,omitempty"`
	Invocation string            `json:"invocation,omitempty"`
	Runtime    string            `json:"runtime,omitempty"`
	Trigger    string            `json:"trigger,omitempty"`
	Candidates []Candidate       `json:"candidates,omitempty"`
	Rubrics    map[string]string `json:"rubrics,omitempty"`
	Choice     string            `json:"choice,omitempty"`
	Confidence *float64          `json:"confidence,omitempty"`
	Outcome    string            `json:"outcome,omitempty"`
	Next       string            `json:"next,omitempty"`
	Detail     string            `json:"detail,omitempty"`
}

type Candidate struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"` // empty means eligible
}

func boundDecision(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 240 {
		s = string([]rune(s)[:239]) + "…"
	}
	return s
}

// AppendDecision serializes complete JSON lines. Callers supply already
// redacted text; this layer also bounds every free text field.
func (s *Store) AppendDecision(d Decision) error {
	if d.At == "" {
		d.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	d.Kind, d.Stage, d.Invocation = boundDecision(d.Kind), boundDecision(d.Stage), boundDecision(d.Invocation)
	d.Runtime = boundDecision(d.Runtime)
	d.Trigger, d.Choice, d.Outcome, d.Next, d.Detail = boundDecision(d.Trigger), boundDecision(d.Choice), boundDecision(d.Outcome), boundDecision(d.Next), boundDecision(d.Detail)
	if len(d.Candidates) > 64 {
		d.Candidates = d.Candidates[:64]
	}
	if len(d.Rubrics) > 64 {
		keys := make([]string, 0, len(d.Rubrics))
		for k := range d.Rubrics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[64:] {
			delete(d.Rubrics, k)
		}
	}
	for i := range d.Candidates {
		d.Candidates[i].ID = boundDecision(d.Candidates[i].ID)
		d.Candidates[i].Reason = boundDecision(d.Candidates[i].Reason)
	}
	for k, v := range d.Rubrics {
		d.Rubrics[k] = boundDecision(v)
	}
	path := filepath.Join(s.Dir, "decisions.jsonl")
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	l, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer l.Release()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func (s *Store) ReadDecisions() ([]Decision, error) {
	f, err := os.Open(filepath.Join(s.Dir, "decisions.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Decision
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var d Decision
		if json.Unmarshal(scan.Bytes(), &d) == nil {
			out = append(out, d)
		}
	}
	return out, scan.Err()
}
