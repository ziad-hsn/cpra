package persistence

import (
	"context"
	"errors"

	"github.com/hashicorp/raft"
)

var errControllerUnavailable = errors.New("durable controller storage unavailable")

// controllerHealthState is published after committed application/restore while
// FSM.mu is held. Routine observations read this immutable value; execution
// admission still reads authoritative state under Store.mu then FSM.mu.
type controllerHealthState struct {
	failed, bootstrap, restore bool
	index                      uint64
	format                     int
}

func (f *machine) controllerHealthState() controllerHealthState {
	return controllerHealthState{failed: f.err != nil, bootstrap: f.bootstrapPending(),
		restore: f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired,
		index:   f.image.Index, format: f.image.Version}
}

func (f *machine) publishControllerHealth() {
	state := f.controllerHealthState()
	if current := f.health.Load(); current == nil || *current != state {
		f.health.Store(&state)
	}
}

func (s *Store) lockHealthObservation(ctx context.Context) (func(), controllerHealthState, error) {
	if ctx == nil {
		return nil, controllerHealthState{}, context.Canceled
	}
	if s == nil {
		return nil, controllerHealthState{}, errControllerUnavailable
	}
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return nil, controllerHealthState{}, err
	}
	if s.fsm == nil || s.fsm.health.Load() == nil {
		s.mu.RUnlock()
		return nil, controllerHealthState{}, errControllerUnavailable
	}
	state := *s.fsm.health.Load()
	if err := ctx.Err(); err != nil {
		s.mu.RUnlock()
		return nil, controllerHealthState{}, err
	}
	return s.mu.RUnlock, state, nil
}

// ControllerHealth classifies recorded storage health without a catalog scan or
// an I/O probe. A leadership error is temporary; every other error keeps the
// controller unavailable. This observation is not an external-action grant.
func (s *Store) ControllerHealth() error {
	return s.ControllerHealthContext(context.Background())
}

// ControllerHealthContext bounds Store lock acquisition by ctx and never waits
// for FSM.mu or disk I/O. This is an observation, not permission to execute.
func (s *Store) ControllerHealthContext(ctx context.Context) error {
	unlock, state, err := s.lockHealthObservation(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.controllerHealthObserved(state); err != nil {
		return err
	}
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
	return s.controllerHealthObserved(s.fsm.controllerHealthState())
}

// Caller holds Store.mu. For mutations the state was read with FSM.mu held;
// observations use the last published committed state instead.
func (s *Store) controllerHealthObserved(state controllerHealthState) error {
	select {
	case <-s.stop:
		return errControllerUnavailable
	default:
	}
	if s.administrative || s.opening || s.err != nil || s.fsm == nil || state.failed {
		return errControllerUnavailable
	}
	if state.bootstrap {
		return errors.Join(errControllerUnavailable, ErrBootstrapPending)
	}
	if state.restore {
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
