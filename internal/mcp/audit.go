package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
	"github.com/OWNER/jevkit/internal/redact"
)

// AuditQuestion is one question's privacy-safe shape in an audit entry: its
// id, declared type and byte counts, never its instructions or criteria
// text.
type AuditQuestion struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	InstructionsLen int    `json:"instructionsBytes"`
	CriteriaLen     int    `json:"criteriaBytes"`
}

// AuditEntry is one jev_ask call: identifying and size/redaction metadata
// only. It never carries state, instructions or criteria text.
type AuditEntry struct {
	Timestamp     string          `json:"timestamp"`
	Tool          string          `json:"tool"`
	Caller        string          `json:"caller"`
	Unregistered  bool            `json:"unregistered"`
	StateBytes    int             `json:"stateBytes"`
	Questions     []AuditQuestion `json:"questions"`
	RedactionHits map[string]int  `json:"redactionHits,omitempty"`
}

// AuditPath is <stateDir>/jevkit/jev-ask-audit.jsonl.
func AuditPath(stateDir string) string {
	return filepath.Join(stateDir, "jevkit", "jev-ask-audit.jsonl")
}

// AppendAudit writes e as one line under an exclusive lock, defaulting the
// timestamp to now (UTC). A missing stateDir disables auditing entirely; the
// caller should not invoke this when stateDir is empty.
func AppendAudit(stateDir string, e AuditEntry) error {
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	path := AuditPath(stateDir)
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

// mergeHits tallies hits by rule id, dropping the count entirely when there
// were none so the field stays omitted rather than an empty object.
func mergeHits(groups ...[]redact.Hit) map[string]int {
	out := map[string]int{}
	for _, hits := range groups {
		for _, h := range hits {
			out[h.RuleID] += h.Count
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
