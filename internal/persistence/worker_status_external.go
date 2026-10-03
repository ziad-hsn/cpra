//go:build externaljobs

package persistence

import (
	"context"
	"slices"
	"strings"
	"time"
)

// WorkerStatus contains operator observations only. It never copies credentials,
// grants, assignment parameters, encrypted payloads, or worker-local state.
type WorkerStatus struct {
	ID, UID, SessionStatus string
	CapacityAvailable      bool
	Capacity               int
	Active, UnknownActions int
	LastSeenAt             time.Time
}

// WorkerStatuses captures a bounded, consistent view of provisioned workers and
// their committed sessions/executions. Active counts unresolved Start grants;
// it is not evidence that remote handler code is currently running. Capacity is
// the worker's last poll advertisement, not a measured concurrency limit.
// UnknownActions counts original unknown receipts, including reviewed actions;
// the action's current hold and operator review are available through action reads.
func (s *Store) WorkerStatuses(ctx context.Context, at time.Time) ([]WorkerStatus, error) {
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return nil, ErrWorkerSessionInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	i := &s.fsm.image
	rows := make([]WorkerStatus, 0)
	if i.WorkerPolicy == nil {
		return rows, ctx.Err()
	}
	if len(i.WorkerPolicy.Workers) > MaxWorkers || i.WorkerExecutions != nil && len(i.WorkerExecutions.Records) > MaxWorkerExecutions {
		return nil, ErrWorkerSessionUnavailable
	}
	byUID := make(map[string]int, len(i.WorkerPolicy.Workers))
	for _, worker := range i.WorkerPolicy.Workers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := WorkerStatus{ID: worker.ID, UID: worker.UID, SessionStatus: "no_session"}
		if sessions := i.WorkerSessions; sessions != nil {
			if session, ok := sessions.Sessions[worker.UID]; ok && session.WorkerID == worker.ID {
				row.LastSeenAt = session.UpdatedAt
				switch {
				case sessions.PolicyEpoch != i.WorkerPolicy.Epoch || session.OwnerEpoch != s.executorSession:
					row.SessionStatus = "previous_owner"
				case at.Before(session.UpdatedAt):
					row.SessionStatus = "clock_regression"
				case !at.Before(session.Response.SessionExpiresAt):
					row.SessionStatus = "expired"
				default:
					row.SessionStatus = "current"
					row.CapacityAvailable, row.Capacity = true, session.Request.Capacity
				}
			}
		}
		switch {
		case i.WorkerPolicy.ResetRequired || worker.ResetRequired:
			row.SessionStatus = "reset_required"
		case worker.Revoked:
			row.SessionStatus = "revoked"
		case !worker.ExpiresAt.IsZero() && !at.Before(worker.ExpiresAt):
			row.SessionStatus = "credential_expired"
		}
		if row.SessionStatus != "current" {
			row.CapacityAvailable, row.Capacity = false, 0
		}
		byUID[row.UID] = len(rows)
		rows = append(rows, row)
	}
	if executions := i.WorkerExecutions; executions != nil {
		for _, record := range executions.Records {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			lifecycle := record.Lifecycle
			if lifecycle == nil || lifecycle.GrantID == "" {
				continue
			}
			index, ok := byUID[lifecycle.WorkerUID]
			if !ok || rows[index].ID != lifecycle.WorkerID {
				continue
			}
			if receipt, terminal := record.TerminalReceipt(); terminal {
				if receipt.Disposition == "unknown" && record.Intent.Category != "check" {
					rows[index].UnknownActions++
				}
			} else {
				rows[index].Active++
			}
		}
	}
	slices.SortFunc(rows, func(a, b WorkerStatus) int { return strings.Compare(a.ID, b.ID) })
	return rows, ctx.Err()
}
