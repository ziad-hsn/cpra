package persistence

import (
	"context"
	"time"

	"github.com/hashicorp/raft"
)

// ObserveCollectionOwner captures the permanent named identity at upload
// creation. A historical/absent policy can still admit inactive legacy uploads,
// but returns nil: such uploads must never acquire background authority later
// merely because a textual actor ID is reused. This method authenticates no HTTP
// caller; the public admission boundary must establish that actor separately.
func (s *Store) ObserveCollectionOwner(ctx context.Context, actor string, at time.Time) (*OperatorAuthority, error) {
	var owner *OperatorAuthority
	err := s.withAuthenticationRead(ctx, func(f *machine) error {
		if s.raft != nil && s.raft.State() != raft.Leader || f.bootstrapPending() || f.restorePending() {
			return ErrAuthenticationUnavailable
		}
		policy := f.image.Authentication
		if policy != nil && policy.ResetRequired {
			return ErrAuthenticationResetRequired
		}
		if policy == nil || policy.Version == AuthenticationFormatVersion {
			return nil
		}
		a := OperatorAuthority{Epoch: policy.Epoch, Revision: policy.Revision, Actor: actor}
		if err := f.checkOperatorAuthority(a, at); err != nil {
			return err
		}
		owner = &a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return owner, nil
}
