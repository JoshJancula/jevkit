package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

// planArtifactBundle is the plan.md + checks.json + subtasks.json triple that
// plan approval binds to. Editing any member invalidates approval.
type planArtifactBundle struct {
	Plan           []byte
	Checks         []byte
	Subtasks       []byte
	PlanDigest     string
	ChecksDigest   string
	SubtasksDigest string
	ChecksFile     adaptive.ChecksFile
	Graph          adaptive.SubtaskGraph
}

func emptyChecksBytes() []byte {
	raw, _ := adaptive.MarshalChecks(nil)
	return raw
}

func emptySubtasksBytes() []byte {
	raw, _ := adaptive.MarshalSubtasks(adaptive.BoundGraphConcurrency(adaptive.SingleImplementerGraph("no subtasks proposed"), 1, 0))
	return raw
}

func loadPlanArtifactBundle(store *ledger.Store) (planArtifactBundle, error) {
	var b planArtifactBundle
	plan, err := store.ReadArtifact(adaptive.ArtifactPlan)
	if err != nil {
		return b, fmt.Errorf("read plan.md: %w", err)
	}
	b.Plan = plan
	b.PlanDigest = adaptive.DigestHex(plan)

	checks, err := store.ReadArtifact(adaptive.ArtifactChecks)
	if err != nil {
		checks = emptyChecksBytes()
	}
	b.Checks = checks
	b.ChecksDigest = adaptive.DigestHex(checks)
	if err := json.Unmarshal(checks, &b.ChecksFile); err != nil {
		return b, fmt.Errorf("parse checks.json: %w", err)
	}

	subtasks, err := store.ReadArtifact(adaptive.ArtifactSubtasks)
	if err != nil {
		subtasks = emptySubtasksBytes()
	}
	b.Subtasks = subtasks
	b.SubtasksDigest = adaptive.DigestHex(subtasks)
	if err := json.Unmarshal(subtasks, &b.Graph); err != nil {
		return b, fmt.Errorf("parse subtasks.json: %w", err)
	}
	return b, nil
}

func ensurePlanSideArtifacts(store *ledger.Store, st *adaptive.State, maxConcurrent, remainingAssignments int) (planArtifactBundle, error) {
	b, err := loadPlanArtifactBundle(store)
	if err != nil {
		return b, err
	}
	if _, err := store.ReadArtifact(adaptive.ArtifactChecks); err != nil {
		if err := store.WriteArtifact(adaptive.ArtifactChecks, b.Checks); err != nil {
			return b, err
		}
	}
	if _, err := store.ReadArtifact(adaptive.ArtifactSubtasks); err != nil {
		graph := adaptive.BoundGraphConcurrency(b.Graph, maxConcurrent, remainingAssignments)
		raw, err := adaptive.MarshalSubtasks(graph)
		if err != nil {
			return b, err
		}
		if err := store.WriteArtifact(adaptive.ArtifactSubtasks, raw); err != nil {
			return b, err
		}
		b.Subtasks = raw
		b.SubtasksDigest = adaptive.DigestHex(raw)
		b.Graph = graph
	}
	if st.ChecksRevision == "" {
		st.ChecksRevision = b.ChecksDigest
	}
	if st.SubtasksRevision == "" {
		st.SubtasksRevision = b.SubtasksDigest
	}
	return b, nil
}

func planArtifactsMatchState(b planArtifactBundle, st *adaptive.State) error {
	if st == nil {
		return fmt.Errorf("missing adaptive state")
	}
	if b.PlanDigest != st.PlanRevision {
		return fmt.Errorf("saved plan.md no longer matches the planned revision")
	}
	if st.ChecksRevision != "" && b.ChecksDigest != st.ChecksRevision {
		return fmt.Errorf("saved checks.json no longer matches the planned revision")
	}
	if st.SubtasksRevision != "" && b.SubtasksDigest != st.SubtasksRevision {
		return fmt.Errorf("saved subtasks.json no longer matches the planned revision")
	}
	return nil
}

func planApprovalComplete(run ledger.Run) bool {
	if run.Adaptive == nil || run.Adaptive.PlanRevision == "" {
		return false
	}
	st := run.Adaptive
	if run.ApprovedPlanRevision != st.PlanRevision {
		return false
	}
	if st.ChecksRevision != "" && run.ApprovedChecksRevision != st.ChecksRevision {
		return false
	}
	if st.SubtasksRevision != "" && run.ApprovedSubtasksRevision != st.SubtasksRevision {
		return false
	}
	return true
}

func argvChecksNeedAuthorization(checks []adaptive.Check, authorizedDigest, checksDigest string) bool {
	if !adaptive.HasArgvChecks(checks) {
		return false
	}
	return strings.TrimSpace(authorizedDigest) != strings.TrimSpace(checksDigest)
}

func remainingAssignmentBudget(run ledger.Run) int {
	if run.Adaptive == nil {
		return 0
	}
	st := run.Adaptive
	if st.MaxAssignments <= 0 {
		return st.MaxConcurrent
	}
	left := st.MaxAssignments - st.AssignmentCount
	if left < 0 {
		return 0
	}
	return left
}
