//go:build externaljobs

package persistence

import (
	"context"
	"time"
)

const workerPollInterval = 100 * time.Millisecond

// WaitWorkerPoll waits before committing a new poll, within the requested wait
// and caller deadline. It never reserves an assignment, renews a session, or
// writes an empty reply. HTTP admission bounds concurrent callers before this
// method; no goroutine or persistent waiter is created here.
//
// CommitWorkerPoll must follow this read: availability is only a hint, and the
// committed transition rechecks authority, session identity, controls and quotas.
func (s *Store) WaitWorkerPoll(ctx context.Context, authority WorkerAuthority, request WorkerPollRequest) error {
	if ctx == nil || !authority.sessionShapeValid() {
		return ErrWorkerSessionInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.administrative {
		return ErrWorkerSessionUnavailable
	}
	request, err := normalizeWorkerPoll(request)
	if err != nil {
		return err
	}
	if request.ServerID != workerProtocolServerID(s.nodeID, authority.Epoch) {
		return ErrWorkerSessionIdentity
	}
	c := WorkerSessionCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: authority, Request: request}
	deadline := time.Now().Add(time.Duration(request.WaitSeconds) * time.Second)
	for {
		unlock, err := s.lockControllerState(ctx, true)
		if err != nil {
			return err
		}
		ready, err := s.fsm.workerPollReady(ctx, c, time.Now().UTC())
		if s.fsm.err != nil {
			err = errControllerUnavailable
		}
		unlock()
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if ready || remaining <= 0 {
			return nil
		}
		// Recheck time-based expiry and leadership as well as new work. Reads
		// scan at most MaxWorkerExecutions small identities, never payloads or
		// the fleet. There is no lock, database transaction or admission gate
		// held during the wait.
		timer := time.NewTimer(min(workerPollInterval, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-s.stop:
			timer.Stop()
			return ErrWorkerSessionUnavailable
		case <-timer.C:
		}
	}
}

func (f *machine) workerPollReady(ctx context.Context, c WorkerSessionCommand, at time.Time) (bool, error) {
	_, exists, replay, err := f.inspectWorkerSession(c, at)
	if err != nil || replay {
		return replay, err // Reconcile the original reply immediately, even empty.
	}
	if sessions := f.image.WorkerSessions; sessions != nil && !exists && len(sessions.Sessions) >= MaxWorkerSessions {
		return false, ErrWorkerSessionQuota
	}
	state := f.image.WorkerExecutions
	if state == nil {
		return false, ctx.Err()
	}
	if len(state.Records) > MaxWorkerExecutions {
		return false, ErrWorkerExecutionQuota
	}
	active := 0
	for _, record := range state.Records {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if l := record.Lifecycle; l != nil && l.WorkerUID == c.Authority.WorkerUID && l.Phase != "rejected" && l.Phase != "finished" {
			active++
		}
	}
	if active >= 100 {
		return false, nil
	}
	capabilities := make(map[WorkerSessionCapability]bool, len(c.Request.Capabilities))
	for _, capability := range c.Request.Capabilities {
		capabilities[capability] = true
	}
	for _, record := range state.Records {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		ref := record.Intent.JobType
		if !capabilities[WorkerSessionCapability{JobTypeID: ref.JobTypeID, Version: ref.Version, Category: ref.Category}] ||
			len(record.Intent.Payload.Ciphertext)+workerOfferWireOverhead > workerOfferWireBudget {
			continue
		}
		if f.workerAssignmentEligible(record, c.Authority, c.OwnerEpoch, at) {
			return true, nil
		}
	}
	return false, nil
}
