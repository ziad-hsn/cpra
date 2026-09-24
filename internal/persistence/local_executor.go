package persistence

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

const maxLocalExecutors = 65536

// LocalExecution owns a process-local claim reserved before a started marker.
// The caller must retain it even when BeginLocalAction returns an error, and
// invoke Finish only after Before/the provider invocation/panic unwinding has
// returned. Context cancellation, lease expiry and cleared ECS flags are not
// evidence that arbitrary Go code has stopped.
type LocalExecution struct {
	store                                  *Store
	monitorID, actionID, revision, session string
	serial                                 chan struct{}
	startedAt                              time.Time
	returnedAt                             atomic.Pointer[time.Time]
	completed                              bool
}

func localExecutionKey(monitor, action string) string { return monitor + "\x00" + action }

func (s *Store) BeginLocalAction(ctx context.Context, start Command) (*LocalExecution, error) {
	if ctx == nil || start.Kind != "start" || start.ExecutorSession != "" {
		return nil, errors.New("local action requires an unstamped start command")
	}
	if err := validateCommand(start); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.controllerHealthContext(ctx); err != nil {
		return nil, err
	}
	s.executorMu.Lock()
	if s.executorClosing || len(s.executors) >= maxLocalExecutors {
		s.executorMu.Unlock()
		return nil, ErrLocalExecutorActive
	}
	key := localExecutionKey(start.MonitorID, start.ActionID)
	if _, exists := s.executors[key]; exists {
		s.executorMu.Unlock()
		return nil, ErrLocalExecutorActive
	}
	handle := &LocalExecution{store: s, monitorID: start.MonitorID, actionID: start.ActionID, revision: start.Revision, session: s.executorSession, startedAt: start.At, serial: make(chan struct{}, 1)}
	if s.executors == nil {
		s.executors = make(map[string]*LocalExecution)
	}
	s.executors[key] = handle
	s.executorMu.Unlock()
	start.ExecutorSession = handle.session
	results, err := s.Submit(ctx, []Command{start})
	if err != nil {
		return handle, err
	}
	if len(results) != 1 {
		return handle, ErrCommitUnconfirmed
	}
	if results[0].Err != nil {
		return handle, results[0].Err
	}
	if !results[0].Allowed {
		return handle, ErrRecoveryIneligible
	}
	return handle, nil
}

// Finish durably records only executor completion, never provider acceptance.
// It is safe to retry the same handle. An unconfirmed marker retains the claim
// and the exclusive data-directory lock; it never silently unlocks storage.
func (h *LocalExecution) Finish(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("local completion requires a context")
	}
	// The first returned observation is part of the retry identity. Publish it
	// before waiting on serialization, including when this caller is canceled.
	at := h.returnedAt.Load()
	if at == nil {
		observed := time.Now().UTC()
		if observed.Before(h.startedAt) {
			observed = h.startedAt
		}
		if h.returnedAt.CompareAndSwap(nil, &observed) {
			at = &observed
		} else {
			at = h.returnedAt.Load()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case h.serial <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-h.serial }()
	if h.completed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.store.controllerHealthContext(ctx); err != nil {
		return err
	}
	results, err := h.store.Submit(ctx, []Command{{Kind: "executor_finished", MonitorID: h.monitorID, Revision: h.revision, ActionID: h.actionID, ExecutorSession: h.session, At: *at}})
	if err != nil {
		return err
	}
	if len(results) != 1 {
		return ErrCommitUnconfirmed
	}
	if results[0].Err != nil {
		return results[0].Err
	}
	if !results[0].Allowed {
		return ErrExecutorUnfenced
	}
	h.store.executorMu.Lock()
	delete(h.store.executors, localExecutionKey(h.monitorID, h.actionID))
	h.store.executorMu.Unlock()
	h.completed = true
	return nil
}

// LocalExecutorStatus is a point-in-time observation of bounded local claims.
// Returned claims still require a confirmed completion marker. Running includes
// uncertain starts whose caller has not returned, not just provider invocations.
type LocalExecutorStatus struct {
	Running, Returned int
}

func (s *Store) localExecutorStatus() LocalExecutorStatus {
	s.executorMu.Lock()
	defer s.executorMu.Unlock()
	var status LocalExecutorStatus
	for _, h := range s.executors {
		if h.returnedAt.Load() == nil {
			status.Running++
		} else {
			status.Returned++
		}
	}
	return status
}

// RetryReturnedExecutions retries at most 128 already-returned invocations
// within five seconds or the caller's earlier deadline. It never invokes a
// provider or marks a running handler complete. Remaining returned claims are
// reported separately from running handlers so recovery can span owner ticks.
// The registry and both scans are bounded by maxLocalExecutors.
func (s *Store) RetryReturnedExecutions(ctx context.Context) (LocalExecutorStatus, error) {
	if ctx == nil {
		return s.localExecutorStatus(), errors.New("local completion requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.executorMu.Lock()
	handles := make([]*LocalExecution, 0, min(128, len(s.executors)))
	for _, h := range s.executors {
		if h.returnedAt.Load() != nil {
			handles = append(handles, h)
			if len(handles) == 128 {
				break
			}
		}
	}
	s.executorMu.Unlock()
	for _, h := range handles {
		if err := h.Finish(ctx); err != nil {
			return s.localExecutorStatus(), err
		}
	}
	return s.localExecutorStatus(), nil
}

// FinishReturnedExecutions retains the shutdown contract: every claim must be
// resolved before storage can close, including handlers that have not returned.
func (s *Store) FinishReturnedExecutions(ctx context.Context) error {
	status, err := s.RetryReturnedExecutions(ctx)
	if err != nil {
		return err
	}
	if status.Running+status.Returned > 0 {
		return ErrLocalExecutorActive
	}
	return nil
}

func (f *machine) applyExecutorCommand(c Command) Result {
	if c.Kind == "local_session" {
		f.image.LocalExecutorSession = c.ExecutorSession
		f.image.Version = max(f.image.Version, CatalogFormatVersion)
		return Result{Allowed: true}
	}
	if c.ExecutorSession != f.image.LocalExecutorSession {
		return Result{Err: ErrExecutorUnfenced}
	}
	m, ok := f.image.Monitors[c.MonitorID]
	if !ok {
		return Result{Allowed: true}
	} // A definitively ungranted start has no executor to finish.
	a, ok := m.Actions[c.ActionID]
	if !ok || a.ExecutorSession != c.ExecutorSession {
		return Result{Allowed: true}
	}
	if a.ExecutorKind != "local" || a.Revision != c.Revision || a.StartedAt.IsZero() {
		return Result{Err: ErrExecutorUnfenced}
	}
	if !a.ExecutorFinishedAt.IsZero() {
		return Result{Allowed: true}
	}
	if c.At.Before(a.StartedAt) {
		return Result{Err: ErrExecutorUnfenced}
	}
	m = m.Clone()
	a.ExecutorFinishedAt = c.At
	m.Actions[a.ID] = a
	return Result{Allowed: true, Monitor: &m}
}
func actionExecutorFenced(a Action, session string) bool {
	return a.ExecutorKind == "local" && a.ExecutorSession != "" && session != "" && (!a.ExecutorFinishedAt.IsZero() || a.ExecutorSession != session)
}
