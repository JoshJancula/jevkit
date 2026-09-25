// Package ledger is the authoritative, on-disk record of a run: run.json
// holds the engine's current State (what resume loads directly — never by
// replaying internal/sdlc/journal's observability log), and nodes/<id>.json
// holds each node's own execution history for audit and `sdlc status`.
// Every write is atomic: a same-directory temp file plus rename, guarded by
// an exclusive lock on a dedicated lock file (internal/filelock), so a
// crash mid-write can never leave a reader with a partially-written file,
// and concurrent writers can never interleave.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/filelock"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/engine"
	"github.com/OWNER/jevkit/internal/sdlc/stageflow"
)

type Event struct {
	At               string `json:"at"`
	RunID            string `json:"runId"`
	Stage            string `json:"stage"`
	Outcome          string `json:"outcome,omitempty"`
	Agent            string `json:"agent,omitempty"`
	Runtime          string `json:"runtime,omitempty"`
	Invocation       string `json:"invocation,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Workflow         string `json:"workflow,omitempty"`
	Assignments      int    `json:"assignments"`
	Revisions        int    `json:"revisions"`
	ChildRuns        int    `json:"childRuns"`
	TransitionStage  string `json:"transitionStage,omitempty"`
	TransitionKind   string `json:"transitionKind,omitempty"`
	TransitionAnswer string `json:"transitionAnswer,omitempty"`
	TransitionNext   string `json:"transitionNext,omitempty"`
	ChildRunID       string `json:"childRunId,omitempty"`
}

// RunFileName and nodesDirName name the files within a run's directory.
const (
	RunFileName  = "run.json"
	nodesDir     = "nodes"
	artifactsDir = "artifacts"
)

// Run is one run's authoritative record: run.json.
type Run struct {
	RunID                string            `json:"runId"`
	SessionStrategy      string            `json:"sessionStrategy,omitempty"`
	RequirePlanApproval  bool              `json:"requirePlanApproval,omitempty"`
	ApprovedPlanRevision string            `json:"approvedPlanRevision,omitempty"`
	PlanFeedback         string            `json:"planFeedback,omitempty"`
	Sessions             map[string]string `json:"sessions,omitempty"`
	Usage                []InvocationUsage `json:"usage,omitempty"`
	ReviewRecovery       *ReviewRecovery   `json:"reviewRecovery,omitempty"`
	WorkDir              string            `json:"workDir,omitempty"`
	AllowRead            []string          `json:"allowRead,omitempty"`
	TreeUsage            *TreeUsage        `json:"treeUsage,omitempty"`
	DelegateBuiltins     bool              `json:"delegateBuiltins,omitempty"`
	AutoDecisionDone     bool              `json:"autoDecisionDone,omitempty"`
	AutoDecisionReason   string            `json:"autoDecisionReason,omitempty"`
	AutoChildRunID       string            `json:"autoChildRunId,omitempty"`
	AutoTarget           string            `json:"autoTarget,omitempty"`
	ParentRunID          string            `json:"parentRunId,omitempty"`
	Depth                int               `json:"depth,omitempty"`
	Workflow             string            `json:"workflow"`
	SelectionReason      string            `json:"selectionReason,omitempty"`
	GraphSHA256          string            `json:"graphSha256"`
	CreatedAt            string            `json:"createdAt"`
	UpdatedAt            string            `json:"updatedAt"`
	// Task is the run's task statement, stored intact (run.json is written
	// at 0600, like every ledger file); it is redacted before it ever
	// reaches Jev, but kept whole here for a human or `sdlc status` to read.
	Task      string           `json:"task,omitempty"`
	State     engine.State     `json:"state"`
	Adaptive  *adaptive.State  `json:"adaptive,omitempty"`
	StageFlow *stageflow.State `json:"stageFlow,omitempty"`
}

// InvocationUsage uses pointers so an absent count remains unknown.
type InvocationUsage struct {
	Invocation   string   `json:"invocation"`
	Agent        string   `json:"agent"`
	Runtime      string   `json:"runtime"`
	Model        string   `json:"model,omitempty"`
	Role         string   `json:"role"`
	SessionID    string   `json:"sessionId,omitempty"`
	InputTokens  *int64   `json:"inputTokens"`
	OutputTokens *int64   `json:"outputTokens"`
	CostUSD      *float64 `json:"costUsd,omitempty"`
}

// ReviewRecovery is durable evidence for a parsed review that was interrupted
// by workspace drift or a crash before its result was applied.
type ReviewRecovery struct {
	Invocation   string   `json:"invocation"`
	Agent        string   `json:"agent"`
	Binding      string   `json:"binding"`
	Runtime      string   `json:"runtime"`
	Model        string   `json:"model,omitempty"`
	Revision     string   `json:"revision"`
	Outcome      string   `json:"outcome"`
	Content      string   `json:"content,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	SessionID    string   `json:"sessionId,omitempty"`
	InputTokens  *int64   `json:"inputTokens,omitempty"`
	OutputTokens *int64   `json:"outputTokens,omitempty"`
	CostUSD      *float64 `json:"costUsd,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	Applied      bool     `json:"applied,omitempty"`
}

// TreeUsage is charged at the root as work is reserved or completed.
type TreeUsage struct {
	Assignments      int     `json:"assignments"`
	Revisions        int     `json:"revisions"`
	ChildRuns        int     `json:"childRuns"`
	StageSteps       int     `json:"stageSteps"`
	EstimatedCostUSD float64 `json:"estimatedCostUsd,omitempty"`
}

// AttemptRecord is one execution of a node's effect: the effect asked for
// and, once resolved, the event that resolved it. Encoded as opaque JSON so
// this package stays decoupled from engine's concrete Effect/Event types,
// the same reasoning as internal/sdlc/journal.
type AttemptRecord struct {
	Attempt    int             `json:"attempt"`
	StartedAt  string          `json:"startedAt"`
	Effect     json.RawMessage `json:"effect,omitempty"`
	ResolvedAt string          `json:"resolvedAt,omitempty"`
	Event      json.RawMessage `json:"event,omitempty"`
}

// NodeRecord is one node's execution history: nodes/<id>.json.
type NodeRecord struct {
	NodeID   string          `json:"nodeId"`
	Attempts []AttemptRecord `json:"attempts,omitempty"`
}

// Store is a run's ledger directory: <root>/<runId>/.
type Store struct {
	Dir string
}

// Open returns the Store for runID under root. It does not touch disk.
func Open(root, runID string) *Store {
	return &Store{Dir: filepath.Join(root, runID)}
}

func (s *Store) runPath() string { return filepath.Join(s.Dir, RunFileName) }

// WithRunLock serializes read-modify-write operations on one run across
// processes. Individual file writes remain atomic under their own locks.
func (s *Store) WithRunLock(fn func() error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	l, err := filelock.Acquire(filepath.Join(s.Dir, "run.update.lock"))
	if err != nil {
		return err
	}
	defer l.Release()
	return fn()
}

func (s *Store) nodePath(nodeID string) (string, error) {
	if nodeID == "" || strings.ContainsAny(nodeID, "/\\") {
		return "", fmt.Errorf("ledger: invalid node id %q", nodeID)
	}
	return filepath.Join(s.Dir, nodesDir, nodeID+".json"), nil
}

// artifactPath resolves an artifact's declared path (which may nest, e.g.
// "docs/design.md") within the run's artifacts directory, rejecting any
// path that would escape it.
func (s *Store) artifactPath(artifact string) (string, error) {
	if artifact == "" {
		return "", errors.New("ledger: artifact name must not be empty")
	}
	base := filepath.Join(s.Dir, artifactsDir)
	full := filepath.Join(base, filepath.FromSlash(artifact))
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("ledger: artifact %q escapes the artifacts directory", artifact)
	}
	return full, nil
}

// WriteArtifact atomically stores a seeded or produced artifact's content
// under the run's artifacts directory.
func (s *Store) WriteArtifact(artifact string, data []byte) error {
	path, err := s.artifactPath(artifact)
	if err != nil {
		return err
	}
	return writeAtomicBytes(path, data)
}

// ReadArtifact reads a previously stored artifact.
func (s *Store) ReadArtifact(artifact string) ([]byte, error) {
	path, err := s.artifactPath(artifact)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// NewRun writes a freshly created run.json. now is stamped as both
// CreatedAt and UpdatedAt.
func NewRun(store *Store, runID, workflow, graphSHA256 string, st engine.State, now time.Time) (Run, error) {
	ts := now.UTC().Format(time.RFC3339)
	r := Run{RunID: runID, Workflow: workflow, GraphSHA256: graphSHA256, CreatedAt: ts, UpdatedAt: ts, State: st}
	return r, store.WriteRun(r)
}

// WriteRun atomically writes r to run.json.
func (s *Store) WriteRun(r Run) error {
	if err := writeAtomicJSON(s.runPath(), r); err != nil {
		return err
	}
	e := Event{At: r.UpdatedAt, RunID: r.RunID, Workflow: r.Workflow}
	if r.Adaptive != nil {
		e.Stage = r.Adaptive.Stage
		e.Outcome = r.Adaptive.Outcome
		e.Assignments = r.Adaptive.AssignmentCount
		e.Revisions = r.Adaptive.RevisionCount
		for _, a := range r.Adaptive.Pending() {
			e.Agent = a.AgentID
			e.Runtime = a.Runtime
			e.Invocation = a.InvocationID
			e.Reason = a.Reason
			break
		}
		if e.Reason == "" {
			e.Reason = r.Adaptive.PendingReason
		}
		if e.Reason == "" && e.Agent == "" {
			e.Reason = r.SelectionReason
		}
		if e.Agent == "" && r.AutoTarget != "" {
			e.Reason = "delegation target: " + r.AutoTarget
		}
	}
	if r.TreeUsage != nil {
		e.ChildRuns = r.TreeUsage.ChildRuns
	}
	if r.StageFlow != nil {
		e.ChildRunID = r.StageFlow.ChildRunID
		if n := len(r.StageFlow.Transitions); n > 0 {
			transition := r.StageFlow.Transitions[n-1]
			e.TransitionStage, e.TransitionAnswer, e.TransitionNext = transition.Stage, transition.Answer, transition.Next
			if stage, ok := r.StageFlow.Workflow.StageByID(transition.Stage); ok {
				if stage.Question != nil {
					e.TransitionKind = "question"
				} else if stage.Spawn != nil {
					e.TransitionKind = "spawn"
				} else if stage.Work != nil {
					e.TransitionKind = "work"
				}
			}
			if transition.ChildRunID != "" {
				e.ChildRunID = transition.ChildRunID
			}
		}
	}
	return s.AppendEvent(e)
}

func (s *Store) AppendEvent(e Event) error {
	path := filepath.Join(s.Dir, "events.jsonl")
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	lock, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = f.Write(b)
	return err
}

func (s *Store) ReadEvents() ([]Event, error) {
	path := filepath.Join(s.Dir, "events.jsonl")
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Event
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var e Event
		if json.Unmarshal(scan.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, scan.Err()
}

// UpdateState loads run.json, applies st, stamps UpdatedAt as now, and
// writes the result back — the one call a caller needs after every
// engine.Apply.
func (s *Store) UpdateState(st engine.State, now time.Time) (Run, error) {
	r, err := s.ReadRun()
	if err != nil {
		return Run{}, err
	}
	r.State = st
	r.UpdatedAt = now.UTC().Format(time.RFC3339)
	return r, s.WriteRun(r)
}

// ReadRun reads run.json.
func (s *Store) ReadRun() (Run, error) {
	var r Run
	err := readJSON(s.runPath(), &r)
	return r, err
}

// WriteNode atomically writes rec to nodes/<rec.NodeID>.json.
func (s *Store) WriteNode(rec NodeRecord) error {
	path, err := s.nodePath(rec.NodeID)
	if err != nil {
		return err
	}
	return writeAtomicJSON(path, rec)
}

// ReadNode reads nodes/<nodeID>.json. A missing record returns a zero-value
// NodeRecord (no attempts yet), not an error: a node the run hasn't reached
// yet simply has none.
func (s *Store) ReadNode(nodeID string) (NodeRecord, error) {
	path, err := s.nodePath(nodeID)
	if err != nil {
		return NodeRecord{}, err
	}
	var rec NodeRecord
	if err := readJSON(path, &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NodeRecord{NodeID: nodeID}, nil
		}
		return NodeRecord{}, err
	}
	return rec, nil
}

// AppendAttempt loads a node's record, appends att, and writes it back.
// Callers needing this under concurrent writers should serialize their own
// updates (e.g. one goroutine driving a given run); WriteNode's atomic
// rename only guarantees a reader never sees a torn file, not read-modify-
// write isolation across the two calls.
func (s *Store) AppendAttempt(nodeID string, att AttemptRecord) error {
	rec, err := s.ReadNode(nodeID)
	if err != nil {
		return err
	}
	rec.Attempts = append(rec.Attempts, att)
	return s.WriteNode(rec)
}

// ReadAllNodes returns every node record in the run's ledger, sorted by
// node id. A run with no nodes directory yet returns none, not an error.
func (s *Store) ReadAllNodes() (map[string]NodeRecord, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, nodesDir))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]NodeRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(names)
	out := make(map[string]NodeRecord, len(names))
	for _, id := range names {
		rec, err := s.ReadNode(id)
		if err != nil {
			return nil, err
		}
		out[id] = rec
	}
	return out, nil
}

// writeAtomicJSON marshals v and writes it to path under an exclusive lock
// on path+".lock", via a same-directory temp file and rename.
func writeAtomicJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomicBytes(path, data)
}

// writeAtomicBytes writes data to path under an exclusive lock on
// path+".lock", via a same-directory temp file and rename.
func writeAtomicBytes(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	l, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer l.Release()

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// readJSON reads and decodes path under the same lock a writer uses, so a
// reader never observes a write that is only partway through (the rename
// itself is atomic; the lock additionally serializes a read against a
// read-modify-write cycle like AppendAttempt's).
func readJSON(path string, v any) error {
	l, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer l.Release()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
