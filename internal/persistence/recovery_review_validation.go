package persistence

import (
	"errors"
	"strings"
)

func validateRecoveryReviewImage(i image) error {
	if i.LocalExecutorSession != "" && (!catalogFormat(i.Version) || !catalogIdentifier(i.LocalExecutorSession, 256)) {
		return ErrExecutorUnfenced
	}
	seen := make(map[string]bool)
	for _, m := range i.Monitors {
		if m.DependencyRevision != "" && !catalogIdentifier(m.DependencyRevision, 256) {
			return errors.New("invalid prepared dependency revision")
		}
		if len(m.ManualRequests) > 100 {
			return errors.New("invalid manual recovery history")
		}
		for n, at := range m.ManualRequests {
			if at.IsZero() || (n > 0 && !at.After(m.ManualRequests[n-1])) {
				return errors.New("invalid manual recovery request time")
			}
		}
		for id, a := range m.Actions {
			if id != a.ID || seen[id] {
				return errors.New("invalid or duplicate durable action identity")
			}
			seen[id] = true
			if a.Manual != (a.OperationID != "") || (a.Manual && (a.Kind != "intervention" || !catalogIdentifier(a.OperationID, 256))) {
				return errors.New("invalid manual recovery action")
			}
			if a.ExecutorKind == "" {
				if a.ExecutorSession != "" || !a.ExecutorFinishedAt.IsZero() {
					return ErrExecutorUnfenced
				}
			} else if a.ExecutorKind == "worker" {
				if !validExternalActionExecutor(i, a) {
					return ErrExecutorUnfenced
				}
			} else if a.ExecutorKind != "local" || !catalogIdentifier(a.ExecutorSession, 256) || i.LocalExecutorSession == "" || a.StartedAt.IsZero() || a.State == Queued || (!a.ExecutorFinishedAt.IsZero() && a.ExecutorFinishedAt.Before(a.StartedAt)) {
				return ErrExecutorUnfenced
			}
			if a.ConflictingEvidence != nil {
				if a.ConflictingEvidence.validate(a) != nil || a.LateEvidence == nil || a.LateEvidence.Outcome == a.ConflictingEvidence.Outcome || a.Review == nil {
					return ErrLateEvidenceConflict
				}
			}
			if r := a.Review; r != nil {
				if a.State != Unknown || !catalogIdentifier(r.Revision, 256) || !catalogIdentifier(r.Actor, 128) || r.At.IsZero() || !controlText(r.Reason) || strings.TrimSpace(r.Reason) == "" || !controlText(r.Note) || !validEvidenceRefs(r.EvidenceRefs) || !validReviewResolution(r.Resolution) {
					return ErrActionReviewConflict
				}
				if r.Resolution != "inconclusive" && !actionExecutorFenced(a, i.LocalExecutorSession) {
					return ErrExecutorUnfenced
				}
				conflict := r.Resolution != "inconclusive" && (a.ConflictingEvidence != nil || (a.LateEvidence != nil && a.LateEvidence.Outcome != r.Resolution))
				if conflict != r.Conflict {
					return ErrActionReviewConflict
				}
			}
		}
	}
	return nil
}
