package review

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/internal/filelock"
)

type Record struct {
	ID            string    `json:"id"`
	Created       time.Time `json:"created"`
	Runtime       string    `json:"runtime"`
	SessionKey    string    `json:"session_key"`
	Workspace     string    `json:"workspace"`
	Tool          string    `json:"tool"`
	ToolInput     string    `json:"tool_input"`
	Score         float64   `json:"score"`
	Confidence    float64   `json:"confidence"`
	Reason        string    `json:"reason"`
	HeuristicHits []string  `json:"heuristic_hits,omitempty"`
	Excerpt       string    `json:"excerpt"`
	RawPointer    string    `json:"raw_pointer"`
	ContentSHA256 string    `json:"content_sha256"`
	SDLCRunID     string    `json:"sdlc_run_id,omitempty"`
	Status        string    `json:"status"`
	Note          string    `json:"note,omitempty"`
}

var validID = regexp.MustCompile(`^[a-f0-9]{24}$`)

func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
func SessionKey(runtime, sessionID, conversationID, workspace string) string {
	if runtime == "cursor" && conversationID != "" {
		return conversationID
	}
	if sessionID != "" {
		return sessionID
	}
	return Hash(workspace)
}
func dir(stateDir string) string { return filepath.Join(stateDir, "jevkit", "injection-reviews") }
func pendingPath(stateDir, key string) string {
	return filepath.Join(dir(stateDir), "pending", Hash(key)+".id")
}
func path(stateDir, id string) (string, error) {
	if stateDir == "" || !validID.MatchString(id) {
		return "", errors.New("invalid review state or id")
	}
	return filepath.Join(dir(stateDir), id+".json"), nil
}
func Create(stateDir string, rec Record) (Record, error) {
	if stateDir == "" || rec.SessionKey == "" {
		return rec, errors.New("review state and session key required")
	}
	if err := os.MkdirAll(dir(stateDir), 0700); err != nil {
		return rec, err
	}
	l, err := filelock.Acquire(filepath.Join(dir(stateDir), ".lock"))
	if err != nil {
		return rec, err
	}
	defer l.Release()
	b := make([]byte, 12)
	if _, err = rand.Read(b); err != nil {
		return rec, err
	}
	rec.ID = hex.EncodeToString(b)
	rec.Created = time.Now().UTC()
	rec.Status = "pending"
	p, _ := path(stateDir, rec.ID)
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return rec, err
	}
	if err = atomicWrite(p, raw); err != nil {
		return rec, err
	}
	index := pendingPath(stateDir, rec.SessionKey)
	if err = os.MkdirAll(filepath.Dir(index), 0700); err != nil {
		_ = os.Remove(p)
		return rec, err
	}
	if _, err = os.Stat(index); errors.Is(err, os.ErrNotExist) {
		if err = atomicWrite(index, []byte(rec.ID)); err != nil {
			_ = os.Remove(p)
			return rec, err
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(p)
		return rec, err
	}
	return rec, nil
}
func Get(stateDir, id string) (Record, error) {
	var rec Record
	p, err := path(stateDir, id)
	if err != nil {
		return rec, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return rec, err
	}
	err = json.Unmarshal(raw, &rec)
	return rec, err
}
func List(stateDir string) ([]Record, error) {
	paths, err := filepath.Glob(filepath.Join(dir(stateDir), "*.json"))
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(paths))
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".json")
		rec, e := Get(stateDir, id)
		if e == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}
func Pending(stateDir, sessionKey string) (Record, bool) {
	if stateDir == "" || sessionKey == "" {
		return Record{}, false
	}
	if raw, err := os.ReadFile(pendingPath(stateDir, sessionKey)); err == nil {
		rec, e := Get(stateDir, string(raw))
		if e == nil && rec.SessionKey == sessionKey && rec.Status == "pending" {
			return rec, true
		}
	} else if errors.Is(err, os.ErrNotExist) {
		return Record{}, false
	}
	all, _ := List(stateDir)
	for _, r := range all {
		if r.SessionKey == sessionKey && r.Status == "pending" {
			return r, true
		}
	}
	return Record{}, false
}
func PendingRun(stateDir, runID string) (Record, bool) {
	all, _ := List(stateDir)
	for _, r := range all {
		if r.SDLCRunID == runID && r.Status == "pending" {
			return r, true
		}
	}
	return Record{}, false
}
func Allowed(stateDir, hash string) bool {
	if stateDir == "" || hash == "" {
		return false
	}
	raw, _ := os.ReadFile(filepath.Join(dir(stateDir), "injection-allowlist.jsonl"))
	for _, line := range strings.Split(string(raw), "\n") {
		if line == hash {
			return true
		}
	}
	return false
}
func Resolve(stateDir, id, action, note string) (Record, error) {
	if action != "allow" && action != "deny" {
		return Record{}, errors.New("action must be allow or deny")
	}
	l, err := filelock.Acquire(filepath.Join(dir(stateDir), ".lock"))
	if err != nil {
		return Record{}, err
	}
	defer l.Release()
	rec, err := Get(stateDir, id)
	if err != nil {
		return rec, err
	}
	if rec.Status != "pending" {
		return rec, fmt.Errorf("review %s already resolved", id)
	}
	rec.Status = map[string]string{"allow": "allowed", "deny": "denied"}[action]
	rec.Note = note
	p, _ := path(stateDir, id)
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return rec, err
	}
	if action == "allow" {
		f, e := os.OpenFile(filepath.Join(dir(stateDir), "injection-allowlist.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return rec, e
		}
		_, e = f.WriteString(rec.ContentSHA256 + "\n")
		_ = f.Close()
		if e != nil {
			return rec, e
		}
	}
	if err = atomicWrite(p, raw); err != nil {
		return rec, err
	}
	index := pendingPath(stateDir, rec.SessionKey)
	if id, e := os.ReadFile(index); e == nil && string(id) == rec.ID {
		remaining := pendingForSession(stateDir, rec.SessionKey)
		if len(remaining) > 0 {
			if err = atomicWrite(index, []byte(remaining[0].ID)); err != nil {
				return rec, err
			}
		} else {
			if err = os.Remove(index); err != nil && !errors.Is(err, os.ErrNotExist) {
				return rec, err
			}
		}
	}
	return rec, nil
}

func pendingForSession(stateDir, key string) []Record {
	all, _ := List(stateDir)
	out := []Record{}
	for _, r := range all {
		if r.SessionKey == key && r.Status == "pending" {
			out = append(out, r)
		}
	}
	return out
}

func atomicWrite(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".review-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
