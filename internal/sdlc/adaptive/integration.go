package adaptive

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Integration outcome statuses persisted on the supervisor-owned record.
const (
	IntegrationStatusPending    = "pending"
	IntegrationStatusApplied    = "applied"
	IntegrationStatusConflict   = "conflict"
	IntegrationStatusRepair     = "repair"
	IntegrationStatusPaused     = "paused-for-human"
	IntegrationStatusRolledBack = "rolled-back"
)

// Apply/rollback policies are explicit so operators can inspect intent.
const (
	ApplyPreserveDirty = "preserve-dirty"
	RollbackExplicit   = "explicit-rollback-of-applied-paths"
)

// ArtifactIntegration is the durable integration decision JSON under the run.
const ArtifactIntegration = "integration/decision.json"

// SubtaskContribution is one subtask's patch and handoff collected against its
// recorded base revision. ChangedPaths must be supplied by the driver (from
// the worktree diff or change report); the integrator does not invent them.
type SubtaskContribution struct {
	SubtaskID    string
	BaseRevision string
	Patch        []byte
	Handoff      string
	ChangedPaths []string
	OwnedPaths   []string
	MergeOrder   int
	DependsOn    []string
	Status       string // SubtaskSucceeded / Failed / TimedOut / Cancelled
	Artifact     string // relative run artifact path for provenance
	InvocationID string
	AgentID      string
	Workspace    string
}

// ProjectSurface describes the user's live project at integration time.
type ProjectSurface struct {
	HeadRevision string   // current HEAD; compared to recorded bases for staleness
	DirtyPaths   []string // pre-existing dirty files that must be preserved
}

// SubtaskProvenance records where a contribution came from for inspect/delete.
type SubtaskProvenance struct {
	SubtaskID    string   `json:"subtaskId"`
	BaseRevision string   `json:"baseRevision,omitempty"`
	Artifact     string   `json:"artifact,omitempty"`
	InvocationID string   `json:"invocationId,omitempty"`
	AgentID      string   `json:"agentId,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
	ChangedPaths []string `json:"changedPaths,omitempty"`
	MergeOrder   int      `json:"mergeOrder"`
	Status       string   `json:"status"`
}

// IntegrationDecision is one recorded choice during planning or apply.
type IntegrationDecision struct {
	At     string   `json:"at,omitempty"`
	Kind   string   `json:"kind"`
	Detail string   `json:"detail,omitempty"`
	Paths  []string `json:"paths,omitempty"`
}

// PathApply describes one path the supervisor intends to write or skip.
type PathApply struct {
	Path    string `json:"path"`
	Subtask string `json:"subtaskId"`
	Action  string `json:"action"` // apply, preserve-dirty, skip
	Reason  string `json:"reason,omitempty"`
}

// RepairBrief is the exact artifact set and summary for one bounded repair.
type RepairBrief struct {
	Summary       string   `json:"summary"`
	SubtaskIDs    []string `json:"subtaskIds,omitempty"`
	Artifacts     []string `json:"artifacts,omitempty"`
	ConflictPaths []string `json:"conflictPaths,omitempty"`
	FailedIDs     []string `json:"failedIds,omitempty"`
}

// IntegrationRecord is the durable supervisor-owned integration step. Only this
// record (not a subtask) may advance the aggregate candidate after fan-out.
type IntegrationRecord struct {
	Status                string                `json:"status"`
	SourceRevision        string                `json:"sourceRevision,omitempty"`
	ProjectHead           string                `json:"projectHead,omitempty"`
	CandidateFingerprint  string                `json:"candidateFingerprint,omitempty"`
	ApplyOrder            []string              `json:"applyOrder,omitempty"`
	AppliedSubtasks       []string              `json:"appliedSubtasks,omitempty"`
	PendingSubtasks       []string              `json:"pendingSubtasks,omitempty"`
	PathPlan              []PathApply           `json:"pathPlan,omitempty"`
	PreservedDirty        []string              `json:"preservedDirty,omitempty"`
	ApplyPolicy           string                `json:"applyPolicy,omitempty"`
	RollbackPolicy        string                `json:"rollbackPolicy,omitempty"`
	Decisions             []IntegrationDecision `json:"decisions,omitempty"`
	Provenance            []SubtaskProvenance   `json:"provenance,omitempty"`
	Repair                *RepairBrief          `json:"repair,omitempty"`
	RepairAssignment      *Assignment           `json:"repairAssignment,omitempty"`
	Summary               string                `json:"summary,omitempty"`
	CombinedPatchArtifact string                `json:"combinedPatchArtifact,omitempty"`
	CheckReceiptsCleared  bool                  `json:"checkReceiptsCleared,omitempty"`
	AssessmentsCleared    bool                  `json:"assessmentsCleared,omitempty"`
	AppliedAt             string                `json:"appliedAt,omitempty"`
	RolledBackAt          string                `json:"rolledBackAt,omitempty"`
}

// CollectContributions builds contributions from a durable schedule plus driver-
// supplied patch bodies keyed by subtask id. Missing successful patches are
// treated as empty contributions (still ordered).
func CollectContributions(s Schedule, patches map[string][]byte, handoffs map[string]string, changed map[string][]string, artifactFor func(id string) string) []SubtaskContribution {
	out := make([]SubtaskContribution, 0, len(s.Subtasks))
	for _, st := range s.Subtasks {
		c := SubtaskContribution{
			SubtaskID:    st.ID,
			BaseRevision: st.BaseRevision,
			OwnedPaths:   append([]string(nil), st.OwnedPaths...),
			MergeOrder:   st.MergeOrder,
			DependsOn:    append([]string(nil), st.DependsOn...),
			Status:       st.Status,
			Workspace:    st.Workspace,
		}
		if st.Result != nil {
			c.InvocationID = st.Result.InvocationID
			c.AgentID = st.Result.AgentID
			if c.InvocationID == "" && st.Assignment != nil {
				c.InvocationID = st.Assignment.InvocationID
				c.AgentID = st.Assignment.AgentID
			}
		} else if st.Assignment != nil {
			c.InvocationID = st.Assignment.InvocationID
			c.AgentID = st.Assignment.AgentID
		}
		if patches != nil {
			c.Patch = append([]byte(nil), patches[st.ID]...)
		}
		if handoffs != nil {
			c.Handoff = handoffs[st.ID]
		}
		if changed != nil {
			c.ChangedPaths = append([]string(nil), changed[st.ID]...)
		}
		if artifactFor != nil {
			c.Artifact = artifactFor(st.ID)
		}
		if c.BaseRevision == "" {
			c.BaseRevision = s.SourceRevision
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MergeOrder != out[j].MergeOrder {
			return out[i].MergeOrder < out[j].MergeOrder
		}
		return out[i].SubtaskID < out[j].SubtaskID
	})
	return out
}

// PlanIntegration is the supervisor-owned step that validates contributions
// before any write to the user's project. It never marks the aggregate run
// complete; callers advance adaptive state only after a successful Commit.
func PlanIntegration(s Schedule, contribs []SubtaskContribution, project ProjectSurface, alreadyApplied []string) IntegrationRecord {
	now := time.Now().UTC().Format(time.RFC3339)
	rec := IntegrationRecord{
		Status:                IntegrationStatusPending,
		SourceRevision:        s.SourceRevision,
		ProjectHead:           project.HeadRevision,
		ApplyPolicy:           ApplyPreserveDirty,
		RollbackPolicy:        RollbackExplicit,
		CombinedPatchArtifact: "patch.diff",
	}
	appliedSet := map[string]bool{}
	for _, id := range alreadyApplied {
		appliedSet[id] = true
		rec.AppliedSubtasks = append(rec.AppliedSubtasks, id)
	}
	dirty := map[string]bool{}
	for _, p := range project.DirtyPaths {
		p = cleanRel(p)
		if p != "" {
			dirty[p] = true
		}
	}

	// Index contributions by id for dependency-aware ordering.
	byID := map[string]SubtaskContribution{}
	for _, c := range contribs {
		byID[c.ID()] = c
	}
	order := mergeOrderIDs(contribs)
	rec.ApplyOrder = append([]string(nil), order...)

	var failed []SubtaskContribution
	var successes []SubtaskContribution
	for _, id := range order {
		c := byID[id]
		rec.Provenance = append(rec.Provenance, c.provenance())
		switch c.Status {
		case SubtaskSucceeded:
			if appliedSet[id] {
				rec.Decisions = append(rec.Decisions, IntegrationDecision{
					At: now, Kind: "skip-already-applied", Detail: "subtask already integrated", Paths: nil,
				})
				continue
			}
			successes = append(successes, c)
		case SubtaskFailed, SubtaskTimedOut, SubtaskCancelled:
			failed = append(failed, c)
		default:
			rec.Decisions = append(rec.Decisions, IntegrationDecision{
				At: now, Kind: "incomplete", Detail: fmt.Sprintf("subtask %s is %s", id, c.Status),
			})
			rec.Status = IntegrationStatusPaused
			rec.Summary = "safe integration impossible: subtasks are not terminal"
			return rec
		}
	}

	if len(failed) > 0 {
		return repairOrPause(rec, now, successes, failed, nil, "failed subtask(s)")
	}

	// Stale base: contribution base must match recorded source and project head when known.
	var stale []string
	for _, c := range successes {
		if s.SourceRevision != "" && c.BaseRevision != "" && c.BaseRevision != s.SourceRevision {
			stale = append(stale, c.SubtaskID)
			rec.Decisions = append(rec.Decisions, IntegrationDecision{
				At: now, Kind: "stale-base", Detail: fmt.Sprintf("%s base %s != source %s", c.SubtaskID, c.BaseRevision, s.SourceRevision),
			})
		}
		if project.HeadRevision != "" && c.BaseRevision != "" && project.HeadRevision != c.BaseRevision {
			stale = append(stale, c.SubtaskID)
			rec.Decisions = append(rec.Decisions, IntegrationDecision{
				At: now, Kind: "stale-base", Detail: fmt.Sprintf("%s base %s != project head %s", c.SubtaskID, c.BaseRevision, project.HeadRevision),
			})
		}
	}
	stale = uniqueSorted(stale)
	if len(stale) > 0 {
		return repairOrPause(rec, now, successes, nil, stale, "stale base revision")
	}

	// Scope + overlap detection before any project write.
	claimed := map[string]string{} // path -> subtask
	var scopeViolations []string
	var overlaps []string
	var dirtyConflicts []string
	for _, c := range successes {
		for _, raw := range c.ChangedPaths {
			p := cleanRel(raw)
			if p == "" {
				continue
			}
			if !pathInOwnedScopes(p, c.OwnedPaths) {
				scopeViolations = append(scopeViolations, p)
				rec.Decisions = append(rec.Decisions, IntegrationDecision{
					At: now, Kind: "scope-violation", Detail: fmt.Sprintf("%s changed %s outside owned scopes", c.SubtaskID, p), Paths: []string{p},
				})
				continue
			}
			if owner, ok := claimed[p]; ok && owner != c.SubtaskID {
				overlaps = append(overlaps, p)
				rec.Decisions = append(rec.Decisions, IntegrationDecision{
					At: now, Kind: "overlap", Detail: fmt.Sprintf("%s and %s both change %s", owner, c.SubtaskID, p), Paths: []string{p},
				})
				continue
			}
			claimed[p] = c.SubtaskID
			if dirty[p] {
				dirtyConflicts = append(dirtyConflicts, p)
				rec.PathPlan = append(rec.PathPlan, PathApply{Path: p, Subtask: c.SubtaskID, Action: "preserve-dirty", Reason: "user dirty file preserved"})
				rec.PreservedDirty = append(rec.PreservedDirty, p)
				rec.Decisions = append(rec.Decisions, IntegrationDecision{
					At: now, Kind: "preserve-dirty", Detail: "refusing to overwrite pre-existing dirty file", Paths: []string{p},
				})
				continue
			}
			rec.PathPlan = append(rec.PathPlan, PathApply{Path: p, Subtask: c.SubtaskID, Action: "apply"})
		}
		rec.PendingSubtasks = append(rec.PendingSubtasks, c.SubtaskID)
	}
	rec.PreservedDirty = uniqueSorted(rec.PreservedDirty)

	if len(scopeViolations) > 0 || len(overlaps) > 0 {
		conflictPaths := uniqueSorted(append(scopeViolations, overlaps...))
		rec.Status = IntegrationStatusConflict
		return repairOrPause(rec, now, successes, nil, conflictPaths, "overlapping or out-of-scope changes")
	}
	if len(dirtyConflicts) > 0 {
		// Patches that only touch dirty paths cannot be applied safely.
		onlyDirty := true
		for _, pa := range rec.PathPlan {
			if pa.Action == "apply" {
				onlyDirty = false
				break
			}
		}
		if onlyDirty && len(dirtyConflicts) > 0 {
			rec.Status = IntegrationStatusPaused
			rec.Summary = "safe integration impossible: all candidate paths are pre-existing dirty files"
			rec.Decisions = append(rec.Decisions, IntegrationDecision{
				At: now, Kind: "pause-for-human", Detail: rec.Summary, Paths: uniqueSorted(dirtyConflicts),
			})
			return rec
		}
	}

	if len(successes) == 0 {
		rec.Status = IntegrationStatusPaused
		rec.Summary = "no successful subtask patches to integrate"
		return rec
	}

	rec.Status = IntegrationStatusPending
	rec.Summary = fmt.Sprintf("ready to apply %d subtask(s) in merge order", len(rec.PendingSubtasks))
	rec.Decisions = append(rec.Decisions, IntegrationDecision{
		At: now, Kind: "plan-ready", Detail: rec.Summary,
	})
	return rec
}

// CommitApply records a successful apply of the pending path plan. It recomputes
// the candidate fingerprint from the combined patch and refuses double-apply.
func (r *IntegrationRecord) CommitApply(combinedPatch []byte, now time.Time) error {
	if r == nil {
		return fmt.Errorf("adaptive: nil integration record")
	}
	if r.Status == IntegrationStatusApplied {
		return fmt.Errorf("adaptive: integration already applied (no double-apply)")
	}
	if r.Status != IntegrationStatusPending && r.Status != IntegrationStatusRepair {
		return fmt.Errorf("adaptive: cannot apply integration in status %s", r.Status)
	}
	if len(r.PendingSubtasks) == 0 && len(r.AppliedSubtasks) == 0 {
		return fmt.Errorf("adaptive: nothing to apply")
	}
	// Only commit when the plan was clean (pending with apply actions or empty path conflicts).
	for _, pa := range r.PathPlan {
		if pa.Action != "apply" && pa.Action != "preserve-dirty" {
			return fmt.Errorf("adaptive: path plan still has unresolved action %q", pa.Action)
		}
	}
	if r.Status == IntegrationStatusRepair {
		return fmt.Errorf("adaptive: resolve repair before apply")
	}
	fp := DigestHex(combinedPatch)
	r.CandidateFingerprint = fp
	r.Status = IntegrationStatusApplied
	r.AppliedAt = now.UTC().Format(time.RFC3339)
	r.AppliedSubtasks = uniqueSorted(append(r.AppliedSubtasks, r.PendingSubtasks...))
	r.PendingSubtasks = nil
	r.CheckReceiptsCleared = true
	r.AssessmentsCleared = true
	r.Decisions = append(r.Decisions, IntegrationDecision{
		At: r.AppliedAt, Kind: "applied", Detail: "candidate fingerprint " + fp,
	})
	return nil
}

// Rollback clears applied state explicitly without touching preserved dirty files.
func (r *IntegrationRecord) Rollback(now time.Time) error {
	if r == nil {
		return fmt.Errorf("adaptive: nil integration record")
	}
	if r.Status != IntegrationStatusApplied {
		return fmt.Errorf("adaptive: rollback requires applied integration")
	}
	r.Status = IntegrationStatusRolledBack
	r.RolledBackAt = now.UTC().Format(time.RFC3339)
	r.CandidateFingerprint = ""
	r.PendingSubtasks = append([]string(nil), r.AppliedSubtasks...)
	r.AppliedSubtasks = nil
	r.Decisions = append(r.Decisions, IntegrationDecision{
		At: r.RolledBackAt, Kind: "rollback", Detail: r.RollbackPolicy,
	})
	return nil
}

// InvalidateOnCandidateChange clears assessments and check receipts when the
// integrated candidate changes. Subtasks never call this to mark the run done.
func InvalidateOnCandidateChange(st *State) {
	if st == nil {
		return
	}
	st.RepairFeedback = nil
	st.NoProgressCount = 0
	st.Assessments = nil
	st.SpecialistReviews = nil
	st.SpecialistDecisions = nil
	st.SpecialistQueue = nil
	st.CheckReceipts = nil
	st.AfterSpecialists = ""
	st.Assignments = map[string]Assignment{}
}

// ApplyIntegratedCandidate updates adaptive state after a successful supervisor
// integration. Only the supervisor path may set DiffRevision from fan-out.
func ApplyIntegratedCandidate(st *State, fingerprint string) error {
	if st == nil {
		return fmt.Errorf("adaptive: nil state")
	}
	if fingerprint == "" {
		return fmt.Errorf("adaptive: candidate fingerprint required")
	}
	if st.Stage != Implementing && st.Stage != Paused {
		return fmt.Errorf("adaptive: integrate from implementing/paused, not %s", st.Stage)
	}
	if fingerprint == st.DiffRevision {
		return fmt.Errorf("adaptive: candidate fingerprint unchanged")
	}
	InvalidateOnCandidateChange(st)
	st.RevisionCount++
	if !st.TreeBudget && st.MaxRevisions > 0 && st.RevisionCount > st.MaxRevisions {
		st.Pause("revision-budget-exhausted")
		return nil
	}
	st.DiffRevision = fingerprint
	st.Stage = Verifying
	st.Outcome = ""
	st.Assignments = map[string]Assignment{}
	st.LastImplementerBinding = "supervisor-integration"
	return nil
}

func (c SubtaskContribution) ID() string { return c.SubtaskID }

func (c SubtaskContribution) provenance() SubtaskProvenance {
	return SubtaskProvenance{
		SubtaskID:    c.SubtaskID,
		BaseRevision: c.BaseRevision,
		Artifact:     c.Artifact,
		InvocationID: c.InvocationID,
		AgentID:      c.AgentID,
		Workspace:    c.Workspace,
		ChangedPaths: append([]string(nil), c.ChangedPaths...),
		MergeOrder:   c.MergeOrder,
		Status:       c.Status,
	}
}

func mergeOrderIDs(contribs []SubtaskContribution) []string {
	cp := append([]SubtaskContribution(nil), contribs...)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].MergeOrder != cp[j].MergeOrder {
			return cp[i].MergeOrder < cp[j].MergeOrder
		}
		return cp[i].SubtaskID < cp[j].SubtaskID
	})
	out := make([]string, len(cp))
	for i, c := range cp {
		out[i] = c.SubtaskID
	}
	return out
}

func repairOrPause(rec IntegrationRecord, now string, successes, failed []SubtaskContribution, conflictPaths []string, reason string) IntegrationRecord {
	var failedIDs []string
	var arts []string
	var ids []string
	for _, c := range failed {
		failedIDs = append(failedIDs, c.SubtaskID)
		ids = append(ids, c.SubtaskID)
		if c.Artifact != "" {
			arts = append(arts, c.Artifact)
		}
	}
	for _, c := range successes {
		ids = append(ids, c.SubtaskID)
		if c.Artifact != "" {
			arts = append(arts, c.Artifact)
		}
	}
	ids = uniqueSorted(ids)
	arts = uniqueSorted(arts)
	conflictPaths = uniqueSorted(conflictPaths)

	// One bounded repair assignment when there is a clear artifact set; otherwise pause for a human.
	if len(ids) == 0 {
		rec.Status = IntegrationStatusPaused
		rec.Summary = "safe integration impossible: " + reason
		rec.Decisions = append(rec.Decisions, IntegrationDecision{At: now, Kind: "pause-for-human", Detail: rec.Summary, Paths: conflictPaths})
		return rec
	}
	summary := reason + "; repair with exact fan-out artifacts"
	if len(failedIDs) > 0 {
		summary = fmt.Sprintf("%s: failed=%s", reason, strings.Join(failedIDs, ","))
	}
	if len(conflictPaths) > 0 {
		summary = fmt.Sprintf("%s; conflicts=%s", summary, strings.Join(conflictPaths, ","))
	}
	rec.Status = IntegrationStatusRepair
	rec.Summary = summary
	rec.Repair = &RepairBrief{
		Summary:       summary,
		SubtaskIDs:    ids,
		Artifacts:     arts,
		ConflictPaths: conflictPaths,
		FailedIDs:     uniqueSorted(failedIDs),
	}
	rec.RepairAssignment = &Assignment{
		Role:      "implementer",
		Objective: "Integrate or repair fan-out results",
		Reason:    summary,
		Revision:  rec.SourceRevision,
	}
	rec.Decisions = append(rec.Decisions, IntegrationDecision{
		At: now, Kind: "repair-assignment", Detail: summary, Paths: conflictPaths,
	})
	return rec
}

func pathInOwnedScopes(filePath string, owned []string) bool {
	filePath = cleanRel(filePath)
	if filePath == "" {
		return false
	}
	for _, scope := range owned {
		scope = cleanRel(scope)
		if scope == "" {
			continue
		}
		if filePath == scope {
			return true
		}
		// Directory scope: "internal/api/" or "internal/api"
		if strings.HasSuffix(scope, "/") || !strings.Contains(path.Base(scope), ".") {
			prefix := strings.TrimSuffix(scope, "/")
			if filePath == prefix || strings.HasPrefix(filePath, prefix+"/") {
				return true
			}
		}
		if pathsOverlap(filePath, scope) && (filePath == scope || strings.HasPrefix(filePath, strings.TrimSuffix(scope, "/")+"/")) {
			return true
		}
	}
	return false
}

func cleanRel(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = path.Clean("/" + strings.TrimPrefix(p, "/"))
	p = strings.TrimPrefix(p, "/")
	if p == "." || p == ".." {
		return ""
	}
	return p
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// CombinePatches concatenates successful contribution patches in apply order
// for the single candidate artifact / fingerprint.
func CombinePatches(order []string, byID map[string]SubtaskContribution) []byte {
	var b strings.Builder
	b.WriteString("Supervisor integration candidate\n")
	for _, id := range order {
		c, ok := byID[id]
		if !ok || c.Status != SubtaskSucceeded {
			continue
		}
		fmt.Fprintf(&b, "\n--- subtask %s (mergeOrder=%d, base=%s) ---\n", id, c.MergeOrder, c.BaseRevision)
		if c.Handoff != "" {
			fmt.Fprintf(&b, "handoff: %s\n", c.Handoff)
		}
		if len(c.ChangedPaths) > 0 {
			fmt.Fprintf(&b, "paths: %s\n", strings.Join(c.ChangedPaths, ", "))
		}
		if len(c.Patch) > 0 {
			b.Write(c.Patch)
			if c.Patch[len(c.Patch)-1] != '\n' {
				b.WriteByte('\n')
			}
		}
	}
	return []byte(b.String())
}

// ContributionsByID indexes contributions by subtask id.
func ContributionsByID(contribs []SubtaskContribution) map[string]SubtaskContribution {
	out := make(map[string]SubtaskContribution, len(contribs))
	for _, c := range contribs {
		out[c.SubtaskID] = c
	}
	return out
}
