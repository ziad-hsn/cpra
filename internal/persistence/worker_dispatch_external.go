//go:build externaljobs

package persistence

import (
	"context"
	"time"
)

// WorkerDispatchState is a bounded scheduling observation. It contains neither
// assignment ciphertext nor an execution grant.
type WorkerDispatchState struct {
	Schedule                                             WorkerScheduleTicket
	Scheduled                                            time.Time
	ID, MonitorID, MonitorUID, MonitorRevision, ActionID string
	Generation                                           uint64
	Terminal                                             bool
}

// CheckWorkerExecutionCapacity avoids reserving IDs and sealing payloads when
// committed limits already prevent admission. This is only a read-side hint;
// CommitWorkerExecution must still enforce every limit atomically.
func (s *Store) CheckWorkerExecutionCapacity(ctx context.Context, ref JobTypeReference) error {
	if ctx == nil || ref.Validate() != nil {
		return ErrWorkerExecutionInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, true)
	if err != nil {
		return err
	}
	defer unlock()
	state := s.fsm.image.WorkerExecutions
	if state == nil {
		return ctx.Err()
	}
	if err := state.ensureIndexes(ctx); err != nil {
		return err
	}
	if len(state.Records) >= MaxWorkerExecutions || state.activeTypes[workerExecutionTypeKey(ref)] >= MaxWorkerExecutionsPerType || state.EncodedBytes >= MaxWorkerExecutionBytes {
		return ErrWorkerExecutionQuota
	}
	return ctx.Err()
}

func workerDispatchState(r WorkerExecutionRecord) WorkerDispatchState {
	_, terminal := r.TerminalReceipt()
	state := WorkerDispatchState{ID: r.Intent.ID, MonitorID: r.Intent.MonitorID,
		MonitorUID: r.Intent.MonitorUID, MonitorRevision: r.Intent.MonitorRevision,
		ActionID: r.Intent.ActionID, Generation: r.Intent.Generation, Scheduled: r.Intent.Scheduled, Terminal: terminal}
	if r.Intent.Schedule != nil {
		state.Schedule = *r.Intent.Schedule
	}
	return state
}

// FindWorkerExecution finds the original active check or retained action using
// the admission indexes. An action remains discoverable after it is terminal;
// another endpoint's unfinished work cannot cause it to be submitted again.
func (s *Store) FindWorkerExecution(ctx context.Context, monitorID, monitorUID, actionID string) (WorkerDispatchState, bool, error) {
	if !catalogIdentifier(monitorID, 256) || !catalogIdentifier(monitorUID, 256) || actionID != "" && !catalogIdentifier(actionID, 256) {
		return WorkerDispatchState{}, false, ErrWorkerExecutionInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, true)
	if err != nil {
		return WorkerDispatchState{}, false, err
	}
	defer unlock()
	state := s.fsm.image.WorkerExecutions
	if state == nil {
		return WorkerDispatchState{}, false, ctx.Err()
	}
	if err := state.ensureIndexes(ctx); err != nil {
		return WorkerDispatchState{}, false, err
	}
	id := state.actions[actionID]
	if actionID == "" {
		id = state.checks[monitorID+"\x00"+monitorUID]
	}
	if id == "" {
		return WorkerDispatchState{}, false, ctx.Err()
	}
	r, ok := state.Records[id]
	if !ok || r.Intent.MonitorID != monitorID || r.Intent.MonitorUID != monitorUID || r.Intent.ActionID != actionID {
		return WorkerDispatchState{}, false, ErrWorkerExecutionConflict
	}
	return workerDispatchState(r), true, ctx.Err()
}

// WorkerDispatchStatus observes a previously committed identity without copying
// its payload. A missing completed check may have been retired. This read never
// determines whether an unconfirmed submission was committed; use a FIFO flush
// and the original intent for that reconciliation.
func (s *Store) WorkerDispatchStatus(ctx context.Context, id string) (WorkerDispatchState, bool, error) {
	if !catalogIdentifier(id, 256) {
		return WorkerDispatchState{}, false, ErrWorkerExecutionInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return WorkerDispatchState{}, false, err
	}
	defer unlock()
	state := s.fsm.image.WorkerExecutions
	if state == nil {
		return WorkerDispatchState{}, false, ctx.Err()
	}
	r, ok := state.Records[id]
	return workerDispatchState(r), ok, ctx.Err()
}
