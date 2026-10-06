package adaptive

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Subtask lifecycle statuses persisted on the durable supervisor schedule.
const (
	SubtaskPending   = "pending"
	SubtaskReserved  = "reserved"
	SubtaskRunning   = "running"
	SubtaskSucceeded = "succeeded"
	SubtaskFailed    = "failed"
	SubtaskTimedOut  = "timed-out"
	SubtaskCancelled = "cancelled"
)

// Workspace modes. A Git worktree is a write boundary, not a security sandbox.
const (
	WorkspaceWorktree       = "worktree"
	WorkspaceSharedReadOnly = "shared-readonly"
	WorkspaceSequential     = "sequential"
	WorkspaceUnavailable    = "unavailable"
)

// SubtaskUsage is the measured usage charged for one subtask attempt.
type SubtaskUsage struct {
	InputTokens  *int64   `json:"inputTokens,omitempty"`
	OutputTokens *int64   `json:"outputTokens,omitempty"`
	ToolCalls    *int64   `json:"toolCalls,omitempty"`
	CostUSD      *float64 `json:"costUsd,omitempty"`
}

// SubtaskRecord is the durable per-subtask supervisor record.
type SubtaskRecord struct {
	ID             string        `json:"id"`
	Status         string        `json:"status"`
	DependsOn      []string      `json:"dependsOn,omitempty"`
	OwnedPaths     []string      `json:"ownedPaths,omitempty"`
	Objective      string        `json:"objective,omitempty"`
	ExpectedOutput string        `json:"expectedOutput,omitempty"`
	MergeOrder     int           `json:"mergeOrder"`
	ReadOnly       bool          `json:"readOnly,omitempty"`
	Assignment     *Assignment   `json:"assignment,omitempty"`
	Workspace      string        `json:"workspace,omitempty"`
	WorkspaceMode  string        `json:"workspaceMode,omitempty"`
	BaseRevision   string        `json:"baseRevision,omitempty"`
	HeartbeatAt    string        `json:"heartbeatAt,omitempty"`
	LeaseOwner     string        `json:"leaseOwner,omitempty"`
	PID            int           `json:"pid,omitempty"`
	Result         *Result       `json:"result,omitempty"`
	Usage          *SubtaskUsage `json:"usage,omitempty"`
	Attempt        int           `json:"attempt,omitempty"`
	PauseReason    string        `json:"pauseReason,omitempty"`
	StartedAt      string        `json:"startedAt,omitempty"`
	FinishedAt     string        `json:"finishedAt,omitempty"`
}

// Schedule is the durable supervisor scheduler for an approved fan-out graph.
type Schedule struct {
	GraphRevision        string                   `json:"graphRevision,omitempty"`
	Mode                 string                   `json:"mode"`
	Parallelize          bool                     `json:"parallelize,omitempty"`
	EffectiveConcurrency int                      `json:"effectiveConcurrency"`
	SourceRevision       string                   `json:"sourceRevision,omitempty"`
	Subtasks             map[string]SubtaskRecord `json:"subtasks,omitempty"`
	PauseReason          string                   `json:"pauseReason,omitempty"`
	IntegrationPending   bool                     `json:"integrationPending,omitempty"`
}

// AdmitBudgets are the independent ceilings that bound how many ready
// subtasks may be reserved. AdmitCap takes the positive minimum.
type AdmitBudgets struct {
	PolicyMax            int
	RunTreeLimit         int
	EligibleBindings     int
	RemainingAssignments int
	RemainingChildRuns   int
	TimeRemaining        time.Duration
	CostRemainingOK      bool
}

// LiveLease is observed process/lease state used during reconcile.
type LiveLease struct {
	SubtaskID  string
	LeaseOwner string
	PID        int
	Alive      bool
}

// NewSchedule builds a durable schedule from an approved fan-out graph.
// Single-mode graphs return a schedule with EffectiveConcurrency 1 and no
// subtask fan-out records.
func NewSchedule(g SubtaskGraph, graphRevision, sourceRevision string) Schedule {
	s := Schedule{
		GraphRevision:        graphRevision,
		Mode:                 g.Mode,
		Parallelize:          g.Parallelize,
		EffectiveConcurrency: g.EffectiveConcurrency,
		SourceRevision:       sourceRevision,
		Subtasks:             map[string]SubtaskRecord{},
	}
	if s.EffectiveConcurrency < 1 {
		s.EffectiveConcurrency = 1
	}
	if g.Mode != "fan-out" {
		s.Mode = "single"
		s.EffectiveConcurrency = 1
		s.Parallelize = false
		return s
	}
	for _, st := range g.Subtasks {
		id := strings.TrimSpace(st.ID)
		s.Subtasks[id] = SubtaskRecord{
			ID:             id,
			Status:         SubtaskPending,
			DependsOn:      append([]string(nil), st.DependsOn...),
			OwnedPaths:     append([]string(nil), st.OwnedPaths...),
			Objective:      st.Objective,
			ExpectedOutput: st.ExpectedOutput,
			MergeOrder:     st.MergeOrder,
			BaseRevision:   sourceRevision,
		}
	}
	return s
}

// AdmitCap returns how many new slots may be reserved right now.
func AdmitCap(budgets AdmitBudgets, effectiveConcurrency, inFlight int) int {
	if !budgets.CostRemainingOK || budgets.TimeRemaining <= 0 {
		return 0
	}
	limits := []int{effectiveConcurrency}
	for _, n := range []int{
		budgets.PolicyMax,
		budgets.RunTreeLimit,
		budgets.EligibleBindings,
		budgets.RemainingAssignments,
		budgets.RemainingChildRuns,
	} {
		if n > 0 {
			limits = append(limits, n)
		}
	}
	cap := limits[0]
	for _, n := range limits[1:] {
		if n < cap {
			cap = n
		}
	}
	if inFlight > 0 {
		cap -= inFlight
	}
	if cap < 0 {
		return 0
	}
	return cap
}

// InFlight counts reserved/running subtasks.
func (s Schedule) InFlight() int {
	n := 0
	for _, st := range s.Subtasks {
		if st.Status == SubtaskReserved || st.Status == SubtaskRunning {
			n++
		}
	}
	return n
}

// ReadyIDs returns pending subtasks whose dependencies have succeeded,
// sorted by merge order then id for stable admission.
func (s Schedule) ReadyIDs() []string {
	done := map[string]bool{}
	for id, st := range s.Subtasks {
		if st.Status == SubtaskSucceeded {
			done[id] = true
		}
	}
	var ready []SubtaskRecord
	for _, st := range s.Subtasks {
		if st.Status != SubtaskPending {
			continue
		}
		ok := true
		for _, dep := range st.DependsOn {
			if !done[dep] {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, st)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].MergeOrder != ready[j].MergeOrder {
			return ready[i].MergeOrder < ready[j].MergeOrder
		}
		return ready[i].ID < ready[j].ID
	})
	out := make([]string, len(ready))
	for i, st := range ready {
		out[i] = st.ID
	}
	return out
}

// WritableInFlight reports whether any non-read-only subtask is reserved or running.
func (s Schedule) WritableInFlight() bool {
	for _, st := range s.Subtasks {
		if (st.Status == SubtaskReserved || st.Status == SubtaskRunning) && !st.ReadOnly {
			return true
		}
	}
	return false
}

// Reserve atomically claims one ready subtask. Callers must hold the root run
// lock. Failed/retried attempts still consume Attempt (budget is charged by
// the driver when Reserve succeeds).
func (s *Schedule) Reserve(id string, assignment Assignment, workspace, mode, leaseOwner string, now time.Time) error {
	if s.PauseReason != "" {
		return fmt.Errorf("adaptive: schedule paused: %s", s.PauseReason)
	}
	st, ok := s.Subtasks[id]
	if !ok {
		return fmt.Errorf("adaptive: unknown subtask %q", id)
	}
	if st.Status != SubtaskPending {
		return fmt.Errorf("adaptive: subtask %q is %s", id, st.Status)
	}
	ready := false
	for _, rid := range s.ReadyIDs() {
		if rid == id {
			ready = true
			break
		}
	}
	if !ready {
		return fmt.Errorf("adaptive: subtask %q is not ready", id)
	}
	if mode == WorkspaceUnavailable {
		return fmt.Errorf("adaptive: isolation unavailable for subtask %q", id)
	}
	if !assignment.ReadOnly && mode == WorkspaceSharedReadOnly {
		return fmt.Errorf("adaptive: writable subtask %q cannot share a read-only snapshot", id)
	}
	if !assignment.ReadOnly && mode != WorkspaceWorktree && mode != WorkspaceSequential {
		return fmt.Errorf("adaptive: writable subtask %q needs an isolated worktree or sequential boundary", id)
	}
	if !assignment.ReadOnly && mode == WorkspaceWorktree && strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("adaptive: worktree path required for subtask %q", id)
	}
	if !assignment.ReadOnly && s.WritableInFlight() && mode != WorkspaceWorktree {
		return fmt.Errorf("adaptive: concurrent writable agents require isolated worktrees")
	}
	if assignment.InvocationID == "" || assignment.AgentID == "" || assignment.Binding == "" {
		return fmt.Errorf("adaptive: reservation needs a complete assignment")
	}
	for _, other := range s.Subtasks {
		if other.Status == SubtaskReserved || other.Status == SubtaskRunning {
			if other.Assignment != nil && (other.Assignment.Binding == assignment.Binding || other.Assignment.AgentID == assignment.AgentID) {
				return fmt.Errorf("adaptive: binding %q already in flight", assignment.Binding)
			}
		}
	}
	st.Status = SubtaskReserved
	st.Assignment = &assignment
	st.Workspace = workspace
	st.WorkspaceMode = mode
	st.LeaseOwner = leaseOwner
	st.Attempt++
	st.PauseReason = ""
	ts := now.UTC().Format(time.RFC3339)
	st.StartedAt = ts
	st.HeartbeatAt = ts
	if st.BaseRevision == "" {
		st.BaseRevision = s.SourceRevision
	}
	s.Subtasks[id] = st
	return nil
}

// MarkRunning transitions a reserved subtask after the process has started.
func (s *Schedule) MarkRunning(id string, pid int, now time.Time) error {
	st, ok := s.Subtasks[id]
	if !ok || st.Status != SubtaskReserved {
		return fmt.Errorf("adaptive: subtask %q is not reserved", id)
	}
	st.Status = SubtaskRunning
	st.PID = pid
	st.HeartbeatAt = now.UTC().Format(time.RFC3339)
	s.Subtasks[id] = st
	return nil
}

// Heartbeat refreshes a live lease.
func (s *Schedule) Heartbeat(id, leaseOwner string, now time.Time) error {
	st, ok := s.Subtasks[id]
	if !ok {
		return fmt.Errorf("adaptive: unknown subtask %q", id)
	}
	if st.Status != SubtaskReserved && st.Status != SubtaskRunning {
		return fmt.Errorf("adaptive: subtask %q is not live", id)
	}
	if leaseOwner != "" && st.LeaseOwner != "" && st.LeaseOwner != leaseOwner {
		return fmt.Errorf("adaptive: lease mismatch for subtask %q", id)
	}
	st.HeartbeatAt = now.UTC().Format(time.RFC3339)
	s.Subtasks[id] = st
	return nil
}

// Complete records a terminal result. Attempt budget was already consumed at Reserve.
func (s *Schedule) Complete(id, status string, result Result, usage *SubtaskUsage, now time.Time) error {
	st, ok := s.Subtasks[id]
	if !ok {
		return fmt.Errorf("adaptive: unknown subtask %q", id)
	}
	switch status {
	case SubtaskSucceeded, SubtaskFailed, SubtaskTimedOut, SubtaskCancelled:
	default:
		return fmt.Errorf("adaptive: invalid terminal status %q", status)
	}
	if st.Status != SubtaskReserved && st.Status != SubtaskRunning {
		return fmt.Errorf("adaptive: subtask %q is %s", id, st.Status)
	}
	st.Status = status
	st.Result = &result
	st.Usage = usage
	st.FinishedAt = now.UTC().Format(time.RFC3339)
	st.HeartbeatAt = st.FinishedAt
	st.PID = 0
	st.LeaseOwner = ""
	s.Subtasks[id] = st
	if s.AllTerminal() && !s.HasFailures() {
		s.IntegrationPending = true
	}
	return nil
}

// RetryFailed moves a failed/timed-out/cancelled subtask back to pending so
// another attempt can be reserved. The prior attempt still consumed budget.
func (s *Schedule) RetryFailed(id string) error {
	st, ok := s.Subtasks[id]
	if !ok {
		return fmt.Errorf("adaptive: unknown subtask %q", id)
	}
	switch st.Status {
	case SubtaskFailed, SubtaskTimedOut, SubtaskCancelled:
	default:
		return fmt.Errorf("adaptive: subtask %q cannot be retried from %s", id, st.Status)
	}
	st.Status = SubtaskPending
	st.Assignment = nil
	st.Workspace = ""
	st.WorkspaceMode = ""
	st.Result = nil
	st.Usage = nil
	st.PID = 0
	st.LeaseOwner = ""
	st.StartedAt = ""
	st.FinishedAt = ""
	st.HeartbeatAt = ""
	st.PauseReason = ""
	s.Subtasks[id] = st
	s.IntegrationPending = false
	s.PauseReason = ""
	return nil
}

// Pause stops further reservation with an operator-visible reason.
func (s *Schedule) Pause(reason string) {
	s.PauseReason = strings.TrimSpace(reason)
}

// AllTerminal reports whether every subtask reached a terminal status.
func (s Schedule) AllTerminal() bool {
	if len(s.Subtasks) == 0 {
		return false
	}
	for _, st := range s.Subtasks {
		switch st.Status {
		case SubtaskSucceeded, SubtaskFailed, SubtaskTimedOut, SubtaskCancelled:
		default:
			return false
		}
	}
	return true
}

// HasFailures reports whether any subtask ended unsuccessfully.
func (s Schedule) HasFailures() bool {
	for _, st := range s.Subtasks {
		switch st.Status {
		case SubtaskFailed, SubtaskTimedOut, SubtaskCancelled:
			return true
		}
	}
	return false
}

// Reconcile aligns durable leases with observed live processes before the
// next schedule tick. Dead reserved/running leases become failed attempts.
func (s *Schedule) Reconcile(live []LiveLease, now time.Time) []string {
	alive := map[string]LiveLease{}
	for _, l := range live {
		if l.SubtaskID != "" {
			alive[l.SubtaskID] = l
		}
	}
	var recovered []string
	for id, st := range s.Subtasks {
		if st.Status != SubtaskReserved && st.Status != SubtaskRunning {
			continue
		}
		obs, ok := alive[id]
		if ok && obs.Alive && (obs.LeaseOwner == "" || st.LeaseOwner == "" || obs.LeaseOwner == st.LeaseOwner) {
			if obs.PID > 0 {
				st.PID = obs.PID
			}
			st.HeartbeatAt = now.UTC().Format(time.RFC3339)
			s.Subtasks[id] = st
			continue
		}
		// Crash or cancel: release the lease and mark failed so budget stays consumed.
		st.Status = SubtaskFailed
		st.PauseReason = "reconciled-dead-lease"
		st.FinishedAt = now.UTC().Format(time.RFC3339)
		st.HeartbeatAt = st.FinishedAt
		st.PID = 0
		st.LeaseOwner = ""
		if st.Result == nil {
			st.Result = &Result{Outcome: "failed", Reason: "reconciled-dead-lease"}
			if st.Assignment != nil {
				st.Result.InvocationID = st.Assignment.InvocationID
				st.Result.AgentID = st.Assignment.AgentID
			}
		}
		s.Subtasks[id] = st
		recovered = append(recovered, id)
	}
	sort.Strings(recovered)
	return recovered
}

// IsolationDecision chooses a workspace mode for the next reservation.
// Concurrent writable work requires an isolated worktree; without it the
// scheduler either sequences or pauses — never concurrent live-worktree writes.
type IsolationDecision struct {
	Mode   string
	Reason string
}

func DecideIsolation(wantParallel bool, writable bool, readOnlyEnforced bool, worktreeAvailable bool, writableInFlight bool) IsolationDecision {
	if !writable {
		if readOnlyEnforced {
			return IsolationDecision{Mode: WorkspaceSharedReadOnly, Reason: "runtime enforces read-only"}
		}
		if worktreeAvailable {
			return IsolationDecision{Mode: WorkspaceWorktree, Reason: "read-only runtime not enforced; isolate via worktree"}
		}
		return IsolationDecision{Mode: WorkspaceUnavailable, Reason: "read-only access is not enforced and isolation is unavailable"}
	}
	if wantParallel || writableInFlight {
		if worktreeAvailable {
			return IsolationDecision{Mode: WorkspaceWorktree, Reason: "isolated write boundary"}
		}
		if !writableInFlight && !wantParallel {
			return IsolationDecision{Mode: WorkspaceSequential, Reason: "isolation unavailable; run one writable agent at a time"}
		}
		if !writableInFlight {
			return IsolationDecision{Mode: WorkspaceSequential, Reason: "isolation unavailable; degrade to sequential writes"}
		}
		return IsolationDecision{Mode: WorkspaceUnavailable, Reason: "cannot run concurrent writable agents without isolated workspaces"}
	}
	if worktreeAvailable {
		return IsolationDecision{Mode: WorkspaceWorktree, Reason: "isolated write boundary"}
	}
	return IsolationDecision{Mode: WorkspaceSequential, Reason: "isolation unavailable; single writable agent may proceed sequentially"}
}
