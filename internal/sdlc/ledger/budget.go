package ledger

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/JoshJancula/jevkit/internal/filelock"
)

// Budget is the single admission ledger for an entire run tree. It lives in
// budget.json under the root, separately from run.json so a stale child/driver
// snapshot cannot overwrite a reservation or an operator's grant.
type Budget struct {
	Version           int                    `json:"version"`
	Original          Allowances             `json:"original"`
	Limits            Allowances             `json:"limits"`
	Usage             Allowances             `json:"usage"`
	Reservations      map[string]Reservation `json:"reservations"`
	Activities        map[string]Activity    `json:"activities"`
	Extensions        []Extension            `json:"extensions,omitempty"`
	Warned            map[string]bool        `json:"warned,omitempty"`
	Checkpoint        time.Time              `json:"checkpoint"`
	TimeEstimated     bool                   `json:"timeEstimated,omitempty"`
	InvocationSeconds float64                `json:"invocationSeconds"`
	WorkflowSteps     map[string]int         `json:"workflowSteps,omitempty"`
}

type Allowances struct {
	Assignments int     `json:"assignments"`
	Revisions   int     `json:"revisions"`
	Children    int     `json:"children"`
	Steps       int     `json:"steps"`
	Seconds     float64 `json:"seconds"`
	CostUSD     float64 `json:"costUsd"`
}

type Reservation struct {
	RunID     string    `json:"runId"`
	Revision  bool      `json:"revision,omitempty"`
	Completed bool      `json:"completed,omitempty"`
	CostUSD   float64   `json:"costUsd,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

type Activity struct {
	Owner string    `json:"owner,omitempty"`
	Until time.Time `json:"until"`
	Seen  time.Time `json:"seen"`
	Host  bool      `json:"host,omitempty"`
}

type Extension struct {
	At     time.Time  `json:"at"`
	RunID  string     `json:"runId"`
	Action string     `json:"action"`
	Before Allowances `json:"before"`
	After  Allowances `json:"after"`
}

func NewBudget(limits Allowances, now time.Time, invocationSeconds float64) Budget {
	return Budget{Version: 1, Original: limits, Limits: limits, Checkpoint: now,
		InvocationSeconds: invocationSeconds, Reservations: map[string]Reservation{},
		Activities: map[string]Activity{}, Warned: map[string]bool{}, WorkflowSteps: map[string]int{}}
}

func (s *Store) ReadBudget() (Budget, error) {
	if err := s.check(); err != nil {
		return Budget{}, err
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "budget.json"))
	if err != nil {
		return Budget{}, err
	}
	var b Budget
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, err
	}
	if b.Version != 1 {
		return b, fmt.Errorf("unsupported budget version %d", b.Version)
	}
	if b.Warned == nil {
		b.Warned = map[string]bool{}
	}
	if b.WorkflowSteps == nil {
		b.WorkflowSteps = map[string]int{}
	}
	if b.Reservations == nil {
		b.Reservations = map[string]Reservation{}
	}
	if b.Activities == nil {
		b.Activities = map[string]Activity{}
	}
	return b, nil
}

// UpdateBudget holds the root's dedicated budget lock. Lock order is run lock,
// then budget lock; callbacks must never acquire a run lock. Atomic rename
// commits reservations, accounting and extension history together.
func (s *Store) UpdateBudget(initial func() (Budget, error), update func(*Budget) error) error {
	if err := s.check(); err != nil {
		return err
	}
	if err := safeMkdirAll(s.Dir); err != nil {
		return err
	}
	l, err := filelock.Acquire(filepath.Join(s.Dir, ".budget.lock"))
	if err != nil {
		return err
	}
	defer l.Release()
	b, err := s.ReadBudget()
	if os.IsNotExist(err) && initial != nil {
		b, err = initial()
	}
	if err != nil {
		return err
	}
	if err := update(&b); err != nil {
		return err
	}
	return writeAtomicJSON(filepath.Join(s.Dir, "budget.json"), b)
}

// Tick counts the union of active intervals. Driver leases that expired since
// the last checkpoint are charged only through their last observed heartbeat;
// host assignments are charged through their independent invocation expiry.
func (b *Budget) Tick(now time.Time) {
	end := b.Checkpoint
	for id, activity := range b.Activities {
		until := now
		if now.After(activity.Until) {
			until = activity.Until
			if !activity.Host {
				until = activity.Seen
			}
			delete(b.Activities, id)
		}
		if until.After(end) {
			end = until
		}
	}
	if end.After(b.Checkpoint) {
		b.Usage.Seconds += end.Sub(b.Checkpoint).Seconds()
	}
	if now.After(b.Checkpoint) {
		b.Checkpoint = now
	}
}

func (b Budget) ReservedRevisions() int {
	n := 0
	for _, r := range b.Reservations {
		if r.Revision && !r.Completed {
			n++
		}
	}
	return n
}

// Blockers returns all allowances which would block the requested admission.
// Completed admitted work does not consult these limits.
func (b Budget) Blockers(kind string) []string {
	var out []string
	if b.Limits.Seconds > 0 && b.Usage.Seconds >= b.Limits.Seconds {
		out = append(out, "time")
	}
	if b.Limits.CostUSD > 0 && b.Usage.CostUSD >= b.Limits.CostUSD {
		out = append(out, "cost-usd")
	}
	if kind == "assignment" || kind == "revision" || kind == "fanout" {
		if b.Limits.Assignments > 0 && b.Usage.Assignments >= b.Limits.Assignments {
			out = append(out, "assignments")
		}
	}
	if kind == "revision" || kind == "fanout" {
		if b.Limits.Revisions > 0 && b.Usage.Revisions+b.ReservedRevisions() >= b.Limits.Revisions {
			out = append(out, "revisions")
		}
	}
	if kind == "child" || kind == "fanout" {
		if b.Limits.Children > 0 && b.Usage.Children >= b.Limits.Children {
			out = append(out, "children")
		}
	}
	if kind == "step" && b.Limits.Steps > 0 && b.Usage.Steps >= b.Limits.Steps {
		out = append(out, "steps")
	}
	return out
}

func (b *Budget) Reserve(id, runID, kind string) error {
	if _, ok := b.Reservations[id]; ok {
		return nil
	}
	if blocked := b.Blockers(kind); len(blocked) > 0 {
		return fmt.Errorf("budget exhausted: %v", blocked)
	}
	r := Reservation{RunID: runID, Revision: kind == "revision" || kind == "fanout"}
	switch kind {
	case "assignment", "revision", "fanout":
		b.Usage.Assignments++
	case "step":
		b.Usage.Steps++
		r.Completed = true
	case "child":
		b.Usage.Children++
		r.Completed = true
	}
	if kind == "fanout" {
		b.Usage.Children++
	}
	b.Reservations[id] = r
	return nil
}

func (b *Budget) Complete(id string, changed bool, cost float64) error {
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return fmt.Errorf("cost must be finite and nonnegative")
	}
	r := b.Reservations[id]
	// Usage can arrive before the result. Only charge the newly reported part.
	if cost > r.CostUSD {
		b.Usage.CostUSD += cost - r.CostUSD
		r.CostUSD = cost
	}
	if !r.Completed && changed {
		b.Usage.Revisions++
	}
	r.Completed = true
	b.Reservations[id] = r
	delete(b.Activities, id)
	return nil
}

func (b *Budget) Extend(runID, action string, add Allowances, now time.Time) error {
	before := b.Limits
	if add.Assignments < 0 || add.Revisions < 0 || add.Children < 0 || add.Steps < 0 || add.Seconds < 0 || add.CostUSD < 0 || math.IsNaN(add.Seconds) || math.IsNaN(add.CostUSD) || math.IsInf(add.Seconds, 0) || math.IsInf(add.CostUSD, 0) {
		return fmt.Errorf("extensions must be positive and finite")
	}
	if add == (Allowances{}) {
		return fmt.Errorf("no extension requested")
	}
	b.Limits.Assignments += add.Assignments
	b.Limits.Revisions += add.Revisions
	b.Limits.Children += add.Children
	if before.Steps > 0 {
		b.Limits.Steps += add.Steps
	}
	b.Limits.Seconds += add.Seconds
	if before.CostUSD > 0 {
		b.Limits.CostUSD += add.CostUSD
	}
	if b.Limits.Assignments < before.Assignments || b.Limits.Revisions < before.Revisions || b.Limits.Children < before.Children || b.Limits.Steps < before.Steps || math.IsInf(b.Limits.Seconds, 0) || math.IsInf(b.Limits.CostUSD, 0) {
		return fmt.Errorf("extension overflows budget")
	}
	if add.Steps > 0 {
		b.WorkflowSteps[runID] += add.Steps
	}
	b.Extensions = append(b.Extensions, Extension{At: now, RunID: runID, Action: action, Before: before, After: b.Limits})
	return nil
}

func (b *Budget) Warnings() []string {
	var out []string
	for key, pair := range map[string][2]float64{
		"assignments": {float64(b.Usage.Assignments), float64(b.Limits.Assignments)},
		"revisions":   {float64(b.Usage.Revisions + b.ReservedRevisions()), float64(b.Limits.Revisions)},
		"children":    {float64(b.Usage.Children), float64(b.Limits.Children)},
		"steps":       {float64(b.Usage.Steps), float64(b.Limits.Steps)},
		"time":        {b.Usage.Seconds, b.Limits.Seconds}, "cost-usd": {b.Usage.CostUSD, b.Limits.CostUSD},
	} {
		if pair[1] > 0 && pair[0] >= .8*pair[1] && !b.Warned[key] {
			b.Warned[key] = true
			out = append(out, fmt.Sprintf("%s: %.2f / %.2f used, %.2f remaining; next admission may require an explicit extension", key, pair[0], pair[1], math.Max(0, pair[1]-pair[0])))
		}
	}
	sort.Strings(out)
	return out
}
