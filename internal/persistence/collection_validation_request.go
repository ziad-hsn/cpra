package persistence

import "time"

const CollectionValidationRequestFormatVersion = 7

// CollectionValidationRequest fixes the original validation input, authority and
// capability profile before a coordinator can inspect or compile it. A claim is
// a single attempt identity, not a transferable lease or an activation grant.
type CollectionValidationRequest struct {
	ID                  string                            `json:"id"`
	InputProgressDigest string                            `json:"input_progress_digest"`
	ItemCount           uint64                            `json:"item_count"`
	Authority           OperatorAuthority                 `json:"authority"`
	CapabilitiesDigest  string                            `json:"capabilities_digest"`
	RequestedAt         time.Time                         `json:"requested_at"`
	Claim               *CollectionValidationClaim        `json:"claim,omitempty"`
	Interruption        *CollectionValidationInterruption `json:"interruption,omitempty"`
}

type CollectionValidationClaim struct {
	ID    string    `json:"id"`
	RunID string    `json:"run_id"`
	At    time.Time `json:"at"`
}

type CollectionValidationInterruption struct {
	ID     string    `json:"id"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// CollectionValidationRequestFence binds work to one original request and
// attempt. An unclaimed request has empty ClaimID and RunID together. None of
// these identifiers authenticates an HTTP caller.
type CollectionValidationRequestFence struct {
	RequestID string `json:"request_id"`
	ClaimID   string `json:"claim_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
}

func (r CollectionValidationRequest) Clone() CollectionValidationRequest {
	if r.Claim != nil {
		copy := *r.Claim
		r.Claim = &copy
	}
	if r.Interruption != nil {
		copy := *r.Interruption
		r.Interruption = &copy
	}
	return r
}

func (c CollectionValidationClaim) validate() error {
	if !validOperationEpoch(c.ID) || !validOperationEpoch(c.RunID) || c.At.IsZero() {
		return ErrCollectionInvalid
	}
	return nil
}

func (i CollectionValidationInterruption) validate() error {
	if !validOperationEpoch(i.ID) || i.At.IsZero() {
		return ErrCollectionInvalid
	}
	switch i.Reason {
	case "coordinatorRestarted", "authorityChanged", "capabilitiesChanged", "validationInterrupted":
		return nil
	default:
		return ErrCollectionInvalid
	}
}

func (p CollectionValidationRequestFence) validate() error {
	if !validOperationEpoch(p.RequestID) || (p.ClaimID == "") != (p.RunID == "") ||
		p.ClaimID != "" && (!validOperationEpoch(p.ClaimID) || !validOperationEpoch(p.RunID)) {
		return ErrCollectionInvalid
	}
	return nil
}

func (r CollectionValidationRequest) validate() error {
	if !validOperationEpoch(r.ID) || !bootstrapHash(r.InputProgressDigest) || r.ItemCount == 0 || r.ItemCount > maxCollectionItems ||
		r.Authority.validate() != nil || !bootstrapHash(r.CapabilitiesDigest) || r.RequestedAt.IsZero() {
		return ErrCollectionInvalid
	}
	if r.Claim != nil && (r.Claim.validate() != nil || r.Claim.At.Before(r.RequestedAt)) {
		return ErrCollectionInvalid
	}
	if r.Interruption != nil && (r.Interruption.validate() != nil || r.Interruption.At.Before(r.RequestedAt) ||
		r.Claim != nil && r.Interruption.At.Before(r.Claim.At)) {
		return ErrCollectionInvalid
	}
	return nil
}

func (r CollectionValidationRequest) validateState(s CollectionState) error {
	if r.validate() != nil || s.Owner == nil || s.Owner.Epoch != r.Authority.Epoch || s.Actor != r.Authority.Actor ||
		s.Uploaded != s.ItemCount || r.ItemCount != s.ItemCount || r.InputProgressDigest != s.ProgressDigest ||
		r.RequestedAt.Before(s.CreatedAt) || r.RequestedAt.After(s.ActivityAt) || s.Phase == "uploading" {
		return ErrCollectionInvalid
	}
	if r.Claim == nil {
		if s.Plan != nil || s.Validation != nil || s.Phase == "validated" || s.Phase == "rejected" {
			return ErrCollectionInvalid
		}
	} else {
		if r.Claim.At.After(s.ActivityAt) || s.Plan != nil && s.Plan.BegunAt.Before(r.Claim.At) ||
			s.Validation != nil && s.Validation.BegunAt.Before(r.Claim.At) {
			return ErrCollectionInvalid
		}
	}
	if v := s.Validation; v != nil && (v.Header.Authority != r.Authority || v.Header.CapabilitiesDigest != r.CapabilitiesDigest) {
		return ErrCollectionInvalid
	}
	if r.Interruption != nil {
		if s.Phase != "interrupted" || !s.TerminalAt.Equal(r.Interruption.At) || r.Interruption.At.Before(s.ActivityAt) ||
			!r.Interruption.At.Before(s.ExpiresAt) || s.Validation != nil && !s.Validation.FinalizedAt.IsZero() {
			return ErrCollectionInvalid
		}
	} else if s.Phase == "interrupted" {
		return ErrCollectionInvalid
	}
	return nil
}

func collectionValidationClaimsEqual(a, b *CollectionValidationClaim) bool {
	return a == nil && b == nil || a != nil && b != nil && a.ID == b.ID && a.RunID == b.RunID && a.At.Equal(b.At)
}

func collectionValidationInterruptionsEqual(a, b *CollectionValidationInterruption) bool {
	return a == nil && b == nil || a != nil && b != nil && a.ID == b.ID && a.Reason == b.Reason && a.At.Equal(b.At)
}

func collectionValidationRequestsEqual(a, b *CollectionValidationRequest) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	x, y := *a, *b
	x.Claim, y.Claim, x.Interruption, y.Interruption = nil, nil, nil, nil
	x.RequestedAt, y.RequestedAt = time.Time{}, time.Time{}
	return x == y && a.RequestedAt.Equal(b.RequestedAt) && collectionValidationClaimsEqual(a.Claim, b.Claim) &&
		collectionValidationInterruptionsEqual(a.Interruption, b.Interruption)
}

func CollectionValidationRequestFenceFor(s CollectionState) *CollectionValidationRequestFence {
	r := s.ValidationRequest
	if r == nil {
		return nil
	}
	p := &CollectionValidationRequestFence{RequestID: r.ID}
	if r.Claim != nil {
		p.ClaimID, p.RunID = r.Claim.ID, r.Claim.RunID
	}
	return p
}

func collectionValidationRequestFenceEqual(a, b *CollectionValidationRequestFence) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func collectionValidationRequestAction(action string) bool {
	return action == "validation_request" || action == "validation_claim" || action == "validation_interrupt"
}

func collectionValidationRequestCommand(c CollectionCommand) bool {
	return collectionValidationRequestAction(c.Action) || c.ValidationRequest != nil || c.ValidationClaim != nil ||
		c.ValidationInterruption != nil || c.ValidationFence != nil || c.ValidationProgress != nil ||
		c.Cleanup != nil && c.Cleanup.ValidationRequest != nil
}

func (c CollectionCommand) validateValidationRequest(at time.Time) error {
	if _, _, err := ParseOperationHandle(c.OperationID); err != nil || !validOperationEpoch(c.UploadID) ||
		c.Epoch != "" || c.Create != nil || c.Item != nil || c.Cleanup != nil || c.Cancel != nil ||
		c.PlanID != "" || c.PlanBegin != nil || c.PlanFragment != nil || c.PlanFinalize != nil ||
		c.ValidationID != "" || c.ValidationBegin != nil || len(c.ValidationItems) != 0 || c.ValidationPublished != 0 {
		return ErrCollectionInvalid
	}
	switch c.Action {
	case "validation_request":
		r := c.ValidationRequest
		if r == nil || r.validate() != nil || r.Claim != nil || r.Interruption != nil || !r.RequestedAt.Equal(at) ||
			c.ValidationClaim != nil || c.ValidationInterruption != nil || c.ValidationFence != nil || c.ValidationProgress != nil {
			return ErrCollectionInvalid
		}
	case "validation_claim":
		p := c.ValidationFence
		if c.ValidationClaim == nil || c.ValidationClaim.validate() != nil || c.ValidationClaim.At.After(at) || p == nil ||
			p.validate() != nil || p.ClaimID != "" || c.ValidationRequest != nil || c.ValidationInterruption != nil || c.ValidationProgress != nil {
			return ErrCollectionInvalid
		}
	case "validation_interrupt":
		p := c.ValidationProgress
		if c.ValidationInterruption == nil || c.ValidationInterruption.validate() != nil || !c.ValidationInterruption.At.Equal(at) ||
			c.ValidationFence == nil || c.ValidationFence.validate() != nil || c.ValidationRequest != nil || c.ValidationClaim != nil ||
			p == nil || !collectionValidationRequestFenceEqual(p.ValidationRequest, c.ValidationFence) {
			return ErrCollectionInvalid
		}
		// Reuse the cleanup shape validator without changing or applying it. Its
		// complete observed progress is the interruption CAS boundary too.
		if (CollectionCommand{Action: "cleanup", OperationID: c.OperationID, UploadID: c.UploadID, Cleanup: p}).validate(at) != nil {
			return ErrCollectionInvalid
		}
	default:
		return ErrCollectionInvalid
	}
	return nil
}
