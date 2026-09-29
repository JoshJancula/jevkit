package ledger

import (
	"fmt"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
)

// ReserveFanoutSlot serializes schedule mutation under the run lock so
// simultaneous drive/host requests cannot oversubscribe or double-launch.
func (s *Store) ReserveFanoutSlot(now time.Time, mutate func(run *Run, schedule *adaptive.Schedule) error) error {
	return s.WithRunLock(func() error {
		run, err := s.ReadRun()
		if err != nil {
			return err
		}
		if run.Fanout == nil {
			return fmt.Errorf("ledger: no fan-out schedule on run %s", run.RunID)
		}
		sched := *run.Fanout
		// Deep-copy subtask map so a failed mutate cannot leave a half-written pointer graph.
		sched.Subtasks = copySubtasks(sched.Subtasks)
		if err := mutate(&run, &sched); err != nil {
			return err
		}
		run.Fanout = &sched
		run.UpdatedAt = now.UTC().Format(time.RFC3339)
		return s.WriteRun(run)
	})
}

// UpdateFanout serializes an arbitrary schedule update under the run lock.
func (s *Store) UpdateFanout(now time.Time, mutate func(run *Run, schedule *adaptive.Schedule) error) error {
	return s.ReserveFanoutSlot(now, mutate)
}

// UpdateIntegration serializes supervisor integration state under the run lock.
func (s *Store) UpdateIntegration(now time.Time, mutate func(run *Run, integration *adaptive.IntegrationRecord) error) error {
	return s.WithRunLock(func() error {
		run, err := s.ReadRun()
		if err != nil {
			return err
		}
		var rec adaptive.IntegrationRecord
		if run.Integration != nil {
			rec = copyIntegration(*run.Integration)
		}
		if err := mutate(&run, &rec); err != nil {
			return err
		}
		run.Integration = &rec
		run.UpdatedAt = now.UTC().Format(time.RFC3339)
		return s.WriteRun(run)
	})
}

// UpdateVerification serializes supervisor verification receipts under the run lock.
func (s *Store) UpdateVerification(now time.Time, mutate func(run *Run, verification *adaptive.VerificationRecord) error) error {
	return s.WithRunLock(func() error {
		run, err := s.ReadRun()
		if err != nil {
			return err
		}
		var rec adaptive.VerificationRecord
		if run.Verification != nil {
			rec = copyVerification(*run.Verification)
		}
		if err := mutate(&run, &rec); err != nil {
			return err
		}
		run.Verification = &rec
		run.UpdatedAt = now.UTC().Format(time.RFC3339)
		return s.WriteRun(run)
	})
}

func copyVerification(in adaptive.VerificationRecord) adaptive.VerificationRecord {
	out := in
	if in.Receipts != nil {
		out.Receipts = make([]adaptive.CheckReceipt, len(in.Receipts))
		for i, r := range in.Receipts {
			cp := r
			cp.Argv = append([]string(nil), r.Argv...)
			out.Receipts[i] = cp
		}
	}
	return out
}

func copyIntegration(in adaptive.IntegrationRecord) adaptive.IntegrationRecord {
	out := in
	out.ApplyOrder = append([]string(nil), in.ApplyOrder...)
	out.AppliedSubtasks = append([]string(nil), in.AppliedSubtasks...)
	out.PendingSubtasks = append([]string(nil), in.PendingSubtasks...)
	out.PreservedDirty = append([]string(nil), in.PreservedDirty...)
	if in.PathPlan != nil {
		out.PathPlan = append([]adaptive.PathApply(nil), in.PathPlan...)
	}
	if in.Decisions != nil {
		out.Decisions = append([]adaptive.IntegrationDecision(nil), in.Decisions...)
	}
	if in.Provenance != nil {
		out.Provenance = make([]adaptive.SubtaskProvenance, len(in.Provenance))
		for i, p := range in.Provenance {
			cp := p
			cp.ChangedPaths = append([]string(nil), p.ChangedPaths...)
			out.Provenance[i] = cp
		}
	}
	if in.Repair != nil {
		r := *in.Repair
		r.SubtaskIDs = append([]string(nil), in.Repair.SubtaskIDs...)
		r.Artifacts = append([]string(nil), in.Repair.Artifacts...)
		r.ConflictPaths = append([]string(nil), in.Repair.ConflictPaths...)
		r.FailedIDs = append([]string(nil), in.Repair.FailedIDs...)
		out.Repair = &r
	}
	if in.RepairAssignment != nil {
		a := *in.RepairAssignment
		a.ProjectWriteScopes = append([]string(nil), in.RepairAssignment.ProjectWriteScopes...)
		a.AgentWriteScopes = append([]string(nil), in.RepairAssignment.AgentWriteScopes...)
		out.RepairAssignment = &a
	}
	return out
}

func copySubtasks(in map[string]adaptive.SubtaskRecord) map[string]adaptive.SubtaskRecord {
	out := make(map[string]adaptive.SubtaskRecord, len(in))
	for k, v := range in {
		cp := v
		if v.DependsOn != nil {
			cp.DependsOn = append([]string(nil), v.DependsOn...)
		}
		if v.OwnedPaths != nil {
			cp.OwnedPaths = append([]string(nil), v.OwnedPaths...)
		}
		if v.Assignment != nil {
			a := *v.Assignment
			a.ProjectWriteScopes = append([]string(nil), v.Assignment.ProjectWriteScopes...)
			a.AgentWriteScopes = append([]string(nil), v.Assignment.AgentWriteScopes...)
			cp.Assignment = &a
		}
		if v.Result != nil {
			r := *v.Result
			cp.Result = &r
		}
		if v.Usage != nil {
			u := *v.Usage
			cp.Usage = &u
		}
		out[k] = cp
	}
	return out
}
