//go:build externaljobs

package persistence

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/hashicorp/raft"
)

// WorkerAuthority is an immutable credential observation, not a start grant.
// Scope revisions fence new work; already-started result ingress must establish
// its own original assignment ownership and current credential policy.
type WorkerAuthority struct {
	Epoch              string `json:"epoch"`
	PolicyRevision     string `json:"policy_revision"`
	WorkerID           string `json:"worker_id"`
	WorkerUID          string `json:"worker_uid"`
	CredentialRevision string `json:"credential_revision"`
	GrantRevision      string `json:"grant_revision"`
}
type WorkerScope struct {
	JobTypeID    string
	JobTypeUID   string
	Version      string
	Category     string
	ResourceKind string
	ResourceID   string
}

func workerReadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrWorkerUnauthorized) || errors.Is(err, ErrWorkerAuthorityDenied) || errors.Is(err, ErrWorkerPolicyResetRequired) {
		return err
	}
	return ErrWorkerPolicyUnavailable
}
func (f *machine) workerPolicyReady(at time.Time) error {
	if f.err != nil || f.bootstrapPending() || f.restorePending() || f.image.Authentication == nil || f.image.Authentication.ResetRequired || f.image.Authentication.AnonymousLoopback || f.image.WorkerPolicy == nil {
		return ErrWorkerPolicyUnavailable
	}
	p := f.image.WorkerPolicy
	if p.ResetRequired {
		return ErrWorkerPolicyResetRequired
	}
	if at.IsZero() || at.Year() < 1 || at.Year() > 9999 || at.Before(p.UpdatedAt) {
		return ErrWorkerPolicyUnavailable
	}
	return nil
}
func workerAuthority(p *WorkerPolicyState, w WorkerPrincipal) WorkerAuthority {
	return WorkerAuthority{Epoch: p.Epoch, PolicyRevision: p.Revision, WorkerID: w.ID, WorkerUID: w.UID, CredentialRevision: w.CredentialRevision, GrantRevision: w.GrantRevision}
}
func workerCredentialEligible(w WorkerPrincipal, at time.Time) bool {
	return !w.Revoked && !w.ResetRequired && (w.ExpiresAt.IsZero() || at.Before(w.ExpiresAt))
}

// AuthenticateWorker accepts only a SHA256 verifier, never a bearer or scopes.
// The supplied time is a lower bound: lock-wait elapsed time and a fresh
// wall-clock observation can only advance it. FSM replay still uses command At.
func (s *Store) AuthenticateWorker(ctx context.Context, verifier string, at time.Time) (WorkerAuthority, error) {
	started := time.Now()
	if ctx == nil {
		return WorkerAuthority{}, ErrWorkerPolicyUnavailable
	}
	if err := ctx.Err(); err != nil {
		return WorkerAuthority{}, err
	}
	if !validVerifier(verifier) {
		return WorkerAuthority{}, ErrWorkerUnauthorized
	}
	expected := workerVerifier(verifier)
	var out WorkerAuthority
	err := s.withAuthenticationRead(ctx, func(f *machine) error {
		now := workerReadObservation(at, started)
		if s.raft != nil && s.raft.State() != raft.Leader {
			return ErrWorkerPolicyUnavailable
		}
		if err := f.workerPolicyReady(now); err != nil {
			return err
		}
		var matched WorkerPrincipal
		found := false
		for _, worker := range f.image.WorkerPolicy.Workers {
			digest := workerVerifier(worker.TokenSHA256)
			if subtle.ConstantTimeCompare(expected[:], digest[:]) == 1 {
				matched = worker
				found = true
			}
		}
		if !found || !workerCredentialEligible(matched, workerReadObservation(at, started)) {
			return ErrWorkerUnauthorized
		}
		out = workerAuthority(f.image.WorkerPolicy, matched)
		return nil
	})
	if err != nil {
		return WorkerAuthority{}, workerReadError(err)
	}
	return out, nil
}

// CheckWorkerAuthority authorizes only NEW work. Stable resource-ID scope may
// cover future resource incarnations; execution admission must separately pin
// and validate the actual resource UID, controls, dependencies and deadline.
func (s *Store) CheckWorkerAuthority(ctx context.Context, a WorkerAuthority, scope WorkerScope, at time.Time) error {
	started := time.Now()
	err := s.withAuthenticationRead(ctx, func(f *machine) error {
		if s.raft != nil && s.raft.State() != raft.Leader {
			return ErrWorkerPolicyUnavailable
		}
		if err := f.checkWorkerAuthority(a, scope, workerReadObservation(at, started)); err != nil {
			return err
		}
		if !workerCredentialEligible(f.image.WorkerPolicy.Workers[a.WorkerID], workerReadObservation(at, started)) {
			return ErrWorkerAuthorityDenied
		}
		return nil
	})
	if err != nil {
		return workerReadError(err)
	}
	return nil
}
func (f *machine) checkWorkerAuthority(a WorkerAuthority, scope WorkerScope, at time.Time) error {
	if err := f.workerPolicyReady(at); err != nil {
		return err
	}
	p := f.image.WorkerPolicy
	w, ok := p.Workers[a.WorkerID]
	if !ok || !workerCredentialEligible(w, at) || a != workerAuthority(p, w) {
		return ErrWorkerAuthorityDenied
	}
	g := WorkerGrant{JobTypeID: scope.JobTypeID, JobTypeUID: scope.JobTypeUID, Version: scope.Version, Category: scope.Category, ResourceKind: scope.ResourceKind, ResourceIDs: []string{scope.ResourceID}}
	if scope.ResourceID == "*" || g.Validate() != nil || validateWorkerGrantReference(f.image, w, g) != nil {
		return ErrWorkerAuthorityDenied
	}
	for _, grant := range w.Grants {
		if grant.JobTypeID != scope.JobTypeID || grant.JobTypeUID != scope.JobTypeUID || grant.Version != scope.Version || grant.Category != scope.Category || grant.ResourceKind != scope.ResourceKind {
			continue
		}
		for _, id := range grant.ResourceIDs {
			if id == "*" || id == scope.ResourceID {
				return nil
			}
		}
	}
	return ErrWorkerAuthorityDenied
}

func workerReadObservation(at, started time.Time) time.Time {
	if at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return time.Time{}
	}
	observation := at.Add(time.Since(started))
	if current := time.Now(); current.After(observation) {
		observation = current
	}
	return observation
}
