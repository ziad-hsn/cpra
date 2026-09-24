package persistence

import "time"

func collectionValidationRequestProgressMatches(s CollectionState, p *CollectionCleanup) bool {
	return p != nil && s.Uploaded == p.Uploaded && s.EncodedBytes == p.EncodedBytes && s.RemovedRows == p.RemovedRows &&
		s.RemovedBytes == p.RemovedBytes && s.ActivityAt.Equal(p.ActivityAt) &&
		collectionPlanCleanupEqual(collectionPlanCleanupFor(s.Plan), p.Plan) &&
		collectionValidationCleanupEqual(collectionValidationCleanupFor(s.Validation), p.Validation) &&
		collectionActivationFenceEqual(CollectionActivationFenceFor(s), p.Activation) &&
		collectionValidationRequestFenceEqual(CollectionValidationRequestFenceFor(s), p.ValidationRequest)
}

// checkCollectionValidationRequestFence preserves the historical unrequested
// path while requiring every new artifact write to carry its original claim.
// Reconciliation of artifact writes is execution-sensitive too: an expired or
// revoked claim cannot regain authority through an identical retry.
func (f *machine) checkCollectionValidationRequestFence(s CollectionState, c CollectionCommand, at time.Time) error {
	r := s.ValidationRequest
	if r == nil {
		if c.ValidationFence != nil {
			return ErrCollectionConflict
		}
		return nil
	}
	if r.Claim == nil || r.Interruption != nil || !collectionInactive(s.Phase) ||
		!collectionValidationRequestFenceEqual(CollectionValidationRequestFenceFor(s), c.ValidationFence) || at.Before(s.ActivityAt) {
		return ErrCollectionConflict
	}
	if !at.Before(s.ExpiresAt) {
		return ErrOperationExpired
	}
	if err := f.checkOperatorAuthority(r.Authority, at); err != nil {
		return err
	}
	if c.ValidationBegin != nil && (c.ValidationBegin.Header.Authority != r.Authority ||
		c.ValidationBegin.Header.CapabilitiesDigest != r.CapabilitiesDigest) {
		return ErrCollectionConflict
	}
	return nil
}

func (f *machine) applyCollectionValidationRequest(c CollectionCommand, at time.Time) Result {
	epoch, seq, _ := ParseOperationHandle(c.OperationID)
	if epoch != f.image.OperationEpoch {
		return Result{Err: ErrOperationExpired}
	}
	original, exists := f.image.Collections[c.OperationID]
	if !exists {
		if seq <= f.image.OperationHighWater {
			return Result{Err: ErrOperationExpired}
		}
		return Result{Err: ErrOperationNotFound}
	}
	s := original.Clone()
	if s.UploadID != c.UploadID {
		return Result{Err: ErrCollectionConflict}
	}
	if c.Action == "validation_request" {
		want := c.ValidationRequest
		if s.ValidationRequest != nil {
			prior := s.ValidationRequest.Clone()
			prior.Claim, prior.Interruption = nil, nil
			if !collectionValidationRequestsEqual(&prior, want) {
				return Result{Err: ErrCollectionConflict}
			}
			// This reconciles a committed fact, never an execution permission.
			return collectionResult(s)
		}
		if s.Phase != "uploading" || s.Plan != nil || s.Validation != nil || s.Uploaded != s.ItemCount ||
			want.InputProgressDigest != s.ProgressDigest || want.ItemCount != s.ItemCount || at.Before(s.ActivityAt) {
			return Result{Err: ErrCollectionConflict}
		}
		if !at.Before(s.ExpiresAt) {
			return Result{Err: ErrOperationExpired}
		}
		if s.Owner == nil || s.Owner.Epoch != want.Authority.Epoch || s.Actor != want.Authority.Actor {
			return Result{Err: ErrOperatorAuthorityDenied}
		}
		if err := f.checkOperatorAuthority(want.Authority, at); err != nil {
			return Result{Err: err}
		}
		r := want.Clone()
		s.ValidationRequest, s.Phase = &r, "validating"
		s.ActivityAt, s.ExpiresAt = at, at.Add(CollectionInactivityLifetime)
		if s.validate() != nil {
			return Result{Err: ErrCollectionConflict}
		}
		f.image.Collections[s.ID] = s
		f.image.Version = max(f.image.Version, CollectionValidationRequestFormatVersion)
		return collectionResult(s)
	}
	r := s.ValidationRequest
	if r == nil || c.ValidationFence == nil || r.ID != c.ValidationFence.RequestID {
		return Result{Err: ErrCollectionConflict}
	}
	if c.Action == "validation_interrupt" {
		if !collectionValidationRequestFenceEqual(CollectionValidationRequestFenceFor(s), c.ValidationFence) {
			return Result{Err: ErrCollectionConflict}
		}
		if r.Interruption != nil {
			if s.Phase != "interrupted" || !collectionValidationInterruptionsEqual(r.Interruption, c.ValidationInterruption) {
				return Result{Err: ErrCollectionConflict}
			}
			return collectionResult(s)
		}
		if s.Phase != "validating" && s.Phase != "validated" || s.Validation != nil && !s.Validation.FinalizedAt.IsZero() ||
			!collectionValidationRequestProgressMatches(s, c.ValidationProgress) || at.Before(s.ActivityAt) {
			return Result{Err: ErrCollectionConflict}
		}
		if !at.Before(s.ExpiresAt) {
			return Result{Err: ErrOperationExpired}
		}
		// Internal retirement must remain possible after the captured authority
		// becomes invalid. It is fenced to this exact attempt and all committed
		// prefixes, and grants no permission to continue compilation or dispatch.
		interruption := *c.ValidationInterruption
		r.Interruption, s.Phase, s.TerminalAt = &interruption, "interrupted", at
		if s.validate() != nil {
			return Result{Err: ErrCollectionConflict}
		}
		f.image.Collections[s.ID] = s
		f.discardCollectionPlanPrefix(s.ID)
		f.discardCollectionValidationPlan(s.ID)
		result := collectionResult(s)
		result.Events = []Event{collectionReceiptEvent(collectionReceiptFor(s))}
		return result
	}
	if c.Action != "validation_claim" || r.Interruption != nil || !collectionInactive(s.Phase) ||
		s.Validation != nil && !s.Validation.FinalizedAt.IsZero() || at.Before(s.ActivityAt) {
		return Result{Err: ErrCollectionConflict}
	}
	if !at.Before(s.ExpiresAt) {
		return Result{Err: ErrOperationExpired}
	}
	if err := f.checkOperatorAuthority(r.Authority, at); err != nil {
		return Result{Err: err}
	}
	if r.Claim != nil {
		if !collectionValidationClaimsEqual(r.Claim, c.ValidationClaim) {
			return Result{Err: ErrCollectionConflict}
		}
		return collectionResult(s)
	}
	if s.Phase != "validating" || s.Plan != nil || s.Validation != nil || !c.ValidationClaim.At.Equal(at) {
		return Result{Err: ErrCollectionConflict}
	}
	claim := *c.ValidationClaim
	r.Claim = &claim
	s.ActivityAt, s.ExpiresAt = at, at.Add(CollectionInactivityLifetime)
	if s.validate() != nil {
		return Result{Err: ErrCollectionConflict}
	}
	f.image.Collections[s.ID] = s
	return collectionResult(s)
}
