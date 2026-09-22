// Package audit is the transparency layer for what leaves the machine: an
// append-only audit log of counts (never content), an opt-in store of the
// exact redacted payloads of recent sends, and an optional confirm step.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileName is the audit log's name inside the state directory.
const FileName = "redaction-audit.jsonl"

// maxLine bounds one audit line when reading.
const maxLine = 1 << 20

// Record is one audit line. It carries rule ids and counts only: never
// matched content, and never the payload.
type Record struct {
	Time        time.Time      `json:"time"`
	Agent       string         `json:"agent"`
	QuestionSet string         `json:"question_set"`
	BytesBefore int            `json:"bytes_before"`
	BytesAfter  int            `json:"bytes_after"`
	Hits        map[string]int `json:"hits"`
}

// Log is an append-only JSONL file, 0600 in a 0700 directory.
type Log struct {
	Path string
	mu   sync.Mutex
}

// clean keeps a caller-supplied label short and free of anything but
// identifier characters, so a label can never smuggle text into the log.
func clean(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == ':', r == '/':
			return r
		}
		return '_'
	}, s)
}

// Append writes one record as a single line.
func (l *Log) Append(rec Record) error {
	rec.Agent, rec.QuestionSet = clean(rec.Agent), clean(rec.QuestionSet)
	if rec.Hits == nil {
		rec.Hits = map[string]int{}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Read returns the records at or after since (zero means all) in file order.
// Lines that do not parse are counted in skipped rather than failing the read.
func Read(path string, since time.Time) (recs []Record, skipped int, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		if len(strings.TrimSpace(sc.Text())) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			skipped++
			continue
		}
		if !since.IsZero() && r.Time.Before(since) {
			continue
		}
		recs = append(recs, r)
	}
	return recs, skipped, sc.Err()
}

// Summary aggregates records.
type Summary struct {
	Sends        int            `json:"sends"`
	BytesBefore  int            `json:"bytes_before"`
	BytesAfter   int            `json:"bytes_after"`
	First        time.Time      `json:"first,omitempty"`
	Last         time.Time      `json:"last,omitempty"`
	Agents       map[string]int `json:"agents"`
	QuestionSets map[string]int `json:"question_sets"`
	Hits         map[string]int `json:"hits"`
}

// Summarize aggregates recs.
func Summarize(recs []Record) Summary {
	s := Summary{Agents: map[string]int{}, QuestionSets: map[string]int{}, Hits: map[string]int{}}
	for _, r := range recs {
		s.Sends++
		s.BytesBefore += r.BytesBefore
		s.BytesAfter += r.BytesAfter
		if s.First.IsZero() || r.Time.Before(s.First) {
			s.First = r.Time
		}
		if r.Time.After(s.Last) {
			s.Last = r.Time
		}
		s.Agents[r.Agent]++
		s.QuestionSets[r.QuestionSet]++
		for id, n := range r.Hits {
			s.Hits[id] += n
		}
	}
	return s
}

// SortedKeys lists a count map's keys, highest count first, ties by name.
func SortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// String renders a one-line description, for error text.
func (r Record) String() string {
	return fmt.Sprintf("%s %s/%s", r.Time.Format(time.RFC3339), r.Agent, r.QuestionSet)
}
