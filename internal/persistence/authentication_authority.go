package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/raft"
)

var ErrOperatorAuthorityDenied = errors.New("current named operator authority required")

// OperatorAuthority is a token-free conditional observation, not a bearer
// credential. A command must bind Actor to its durable operation owner and
// compare this fence again inside Apply at the command's observation time.
// Epoch and revision come from committed policy, never a process-local counter.
type OperatorAuthority struct {
	Epoch    string `json:"epoch"`
	Revision string `json:"revision"`
	Actor    string `json:"actor"`
}

func (a OperatorAuthority) validate() error {
	if !validAuthenticationID(a.Epoch) || !validAuthenticationID(a.Revision) ||
		!validAuthenticationID(a.Actor) || a.Actor == "legacy-read" {
		return ErrOperatorAuthorityDenied
	}
	return nil
}

// ObserveOperatorAuthority reads only committed policy. Policy version 1 is
// readable for ordinary requests but requires an explicit stopped replacement
// before its principals can authorize background collection work. Reading
// never renews a credential or changes policy. The caller supplies current time.
func (s *Store) ObserveOperatorAuthority(ctx context.Context, actor string, at time.Time) (OperatorAuthority, error) {
	var authority OperatorAuthority
	err := s.withAuthenticationRead(ctx, func(f *machine) error {
		if f.bootstrapPending() || f.restorePending() {
			return ErrAuthenticationUnavailable
		}
		if f.image.Authentication != nil && f.image.Authentication.ResetRequired {
			return ErrAuthenticationResetRequired
		}
		if s.raft != nil && s.raft.State() != raft.Leader {
			return errors.Join(ErrAuthenticationUnavailable, raft.ErrNotLeader)
		}
		state := f.image.Authentication
		if state == nil {
			return ErrOperatorAuthorityDenied
		}
		candidate := OperatorAuthority{Epoch: state.Epoch, Revision: state.Revision, Actor: actor}
		if err := f.checkOperatorAuthority(candidate, at); err != nil {
			return err
		}
		authority = candidate
		return nil
	})
	if err != nil {
		return OperatorAuthority{}, err
	}
	return authority, nil
}

// checkOperatorAuthority runs within the caller's existing FSM ownership. It
// performs no I/O and never reads the wall clock, making replay deterministic.
// It does not establish that an HTTP caller controls the named principal: the
// authenticated admission layer must bind that identity before submission.
func (f *machine) checkOperatorAuthority(a OperatorAuthority, at time.Time) error {
	if f.err != nil || f.bootstrapPending() || f.restorePending() {
		return ErrAuthenticationUnavailable
	}
	s := f.image.Authentication
	if s != nil && s.ResetRequired {
		return ErrAuthenticationResetRequired
	}
	if a.validate() != nil || s == nil || s.Version != AuthenticationLifecycleFormatVersion ||
		!s.BootstrapConsumed || s.AnonymousLoopback || at.IsZero() || at.Year() < 1 || at.Year() > 9999 || at.Before(s.UpdatedAt) {
		return ErrOperatorAuthorityDenied
	}
	if a.Epoch != s.Epoch || a.Revision != s.Revision {
		return ErrAuthenticationConflict
	}
	for _, principal := range s.Principals {
		if principal.ID == a.Actor {
			if principal.Role != "operator" || principal.Revoked ||
				!principal.ExpiresAt.IsZero() && !at.Before(principal.ExpiresAt) {
				return ErrOperatorAuthorityDenied
			}
			return nil
		}
	}
	return ErrOperatorAuthorityDenied
}

func (s *Store) authenticationSnapshot(ctx context.Context) (AuthenticationState, error) {
	var state AuthenticationState
	err := s.withAuthenticationRead(ctx, func(f *machine) error {
		if f.image.Authentication != nil {
			state = f.image.Authentication.Clone()
		}
		return nil
	})
	return state, err
}

// withAuthenticationRead has bounded in-memory callbacks only. Neither the
// policy nor a verifier is copied into the authority observation.
func (s *Store) withAuthenticationRead(ctx context.Context, read func(*machine) error) error {
	if ctx == nil {
		return ErrAuthenticationInvalid
	}
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	select {
	case <-s.stop:
		return ErrAuthenticationUnavailable
	default:
	}
	if s.err != nil || s.fsm == nil {
		return ErrAuthenticationUnavailable
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return err
	}
	defer s.fsm.mu.RUnlock()
	if s.fsm.err != nil {
		return ErrAuthenticationUnavailable
	}
	if err := read(s.fsm); err != nil {
		return err
	}
	return ctx.Err()
}
