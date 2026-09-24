//go:build externaljobs

package persistence

import (
	"context"
	"slices"
)

// WorkerExecutions pages every retained original intent, without checking its
// owner, deadline, or current eligibility. Startup verification runs before
// execution writers; this lexical traversal is not a concurrent public cursor.
func (s *Store) WorkerExecutions(ctx context.Context, afterID string, limit int) (WorkerExecutionPage, error) {
	var page WorkerExecutionPage
	if ctx == nil || afterID != "" && !catalogIdentifier(afterID, 256) || limit < 1 || limit > 256 {
		return page, ErrWorkerExecutionInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return page, err
	}
	defer unlock()
	state := s.fsm.image.WorkerExecutions
	if state == nil {
		return page, ctx.Err()
	}
	if len(state.Records) > MaxWorkerExecutions {
		return page, ErrWorkerExecutionQuota
	}
	ids := make([]string, 0, len(state.Records))
	for id := range state.Records {
		if err := ctx.Err(); err != nil {
			return WorkerExecutionPage{}, err
		}
		if id > afterID {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var used int64
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return WorkerExecutionPage{}, err
		}
		record := state.Records[id]
		if record.Intent.ID != id {
			return WorkerExecutionPage{}, ErrWorkerExecutionInvalid
		}
		cost, err := workerExecutionRecordCost(id, record)
		if err != nil {
			return WorkerExecutionPage{}, err
		}
		if len(page.Items) == limit || cost > MaxWorkerExecutionPageBytes-used {
			page.More = true
			break
		}
		used += cost
		page.Items = append(page.Items, record.Clone())
		page.Next = id
	}
	if err := ctx.Err(); err != nil {
		return WorkerExecutionPage{}, err
	}
	return page, nil
}
