package persistence

import (
	"context"
	"errors"

	"github.com/hashicorp/raft"
)

var errControllerUnavailable = errors.New("durable controller storage unavailable")

// ControllerHealth classifies recorded storage health without a catalog scan or
// an I/O probe. A leadership error is temporary; every other error keeps the
// controller unavailable. This observation is not an external-action grant.
func (s *Store) ControllerHealth() error {
	return s.ControllerHealthContext(context.Background())
}

// ControllerHealthContext bounds lock acquisition by ctx.
func (s *Store) ControllerHealthContext(ctx context.Context) error {
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return err
	}
	defer unlock()
	return ctx.Err()
}

func (s *Store) controllerHealthContext(ctx context.Context) error {
	return s.ControllerHealthContext(ctx)
}

// lockControllerState holds Store.mu before FSM.mu and checks permanent health
// before leadership under those same locks. The caller must release both locks.
func (s *Store) lockControllerState(ctx context.Context, write bool) (func(), error) {
	unlock, err := s.lockStateContext(ctx, write)
	if err != nil {
		return nil, err
	}
	if err := s.controllerHealth(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// lockStateContext acquires Store.mu before FSM.mu without imposing a read's
// health policy. Native catalog inspection also runs before runtime admission.
func (s *Store) lockStateContext(ctx context.Context, write bool) (func(), error) {
	if s == nil {
		return nil, errControllerUnavailable
	}
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return nil, err
	}
	if s.fsm == nil {
		s.mu.RUnlock()
		return nil, errControllerUnavailable
	}
	f := s.fsm
	lock, unlockFSM := collectionReadLock, f.mu.RUnlock
	if write {
		lock, unlockFSM = collectionCoordinatorLock, f.mu.Unlock
	}
	if err := lock(ctx, &f.mu); err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	return func() { unlockFSM(); s.mu.RUnlock() }, nil
}

// Caller holds Store.mu before FSM.mu. Do not expose a recorded permanent
// error's nested leadership cause: MarkUnavailable is an irreversible decision.
func (s *Store) controllerHealth() error {
	select {
	case <-s.stop:
		return errControllerUnavailable
	default:
	}
	if s.administrative || s.opening || s.err != nil || s.fsm == nil || s.fsm.err != nil {
		return errControllerUnavailable
	}
	if s.fsm.bootstrapPending() {
		return errors.Join(errControllerUnavailable, ErrBootstrapPending)
	}
	if s.fsm.restorePending() || s.fsm.image.Authentication != nil && s.fsm.image.Authentication.ResetRequired {
		return errors.Join(errControllerUnavailable, ErrAuthenticationResetRequired)
	}
	if s.raft != nil {
		switch s.raft.State() {
		case raft.Leader:
		case raft.Shutdown:
			return errors.Join(errControllerUnavailable, raft.ErrRaftShutdown)
		default:
			return errors.Join(errControllerUnavailable, raft.ErrNotLeader)
		}
	}
	return nil
}
