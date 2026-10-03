package persistence

import "time"

// CollectionCancellation identifies one original request to retire inactive
// input. The actor is the upload owner, not an authorization credential. Public
// admission must separately authorize the caller before submitting a command.
// Exact retries carry the original ID and observation time; neither replay nor
// cleanup generates a new identity or renews the inactivity deadline.
type CollectionCancellation struct {
	ID    string    `json:"id"`
	Actor string    `json:"actor"`
	At    time.Time `json:"at"`
}

func (c CollectionCancellation) validate() error {
	if !validOperationEpoch(c.ID) || !catalogIdentifier(c.Actor, 128) || c.At.IsZero() {
		return ErrCollectionInvalid
	}
	return nil
}

func (f *machine) cancelCollection(c CollectionCommand, at time.Time) Result {
	epoch, seq, _ := ParseOperationHandle(c.OperationID)
	if epoch != f.image.OperationEpoch {
		return Result{Err: ErrOperationExpired}
	}
	s, exists := f.image.Collections[c.OperationID]
	if !exists {
		if seq <= f.image.OperationHighWater {
			return Result{Err: ErrOperationExpired}
		}
		return Result{Err: ErrOperationNotFound}
	}
	want := c.Cancel
	if s.UploadID != c.UploadID || s.Actor != want.Actor {
		return Result{Err: ErrCollectionConflict}
	}
	if s.Activation != nil {
		if !collectionActivationFenceEqual(CollectionActivationFenceFor(s), c.ActivationFence) || at.Before(s.Activation.At) || want.At.Before(s.Activation.At) || !s.TerminalAt.IsZero() && at.Before(s.TerminalAt) {
			return Result{Err: ErrCollectionConflict}
		}
		if err := f.checkCollectionActivationAuthority(s, c, at); err != nil {
			return Result{Err: err}
		}
	} else if c.ActivationFence != nil || c.ActivationAuthority != nil {
		return Result{Err: ErrCollectionConflict}
	}
	if s.Phase == "canceled" {
		previous := s.Cancellation
		if previous == nil || previous.ID != want.ID || previous.Actor != want.Actor || !previous.At.Equal(want.At) {
			return Result{Err: ErrCollectionConflict}
		}
		// The retained original result is safe to reconcile even after its
		// former upload deadline. A retry adds no event and removes no rows.
		return collectionResult(s)
	}
	if !collectionLive(s.Phase) || want.At.Before(s.ActivityAt) || !want.At.Equal(at) {
		return Result{Err: ErrCollectionConflict}
	}
	if s.Activation == nil && !want.At.Before(s.ExpiresAt) {
		return Result{Err: ErrOperationExpired}
	}
	copy := *want
	s.Cancellation, s.Phase, s.TerminalAt = &copy, "canceled", want.At
	if s.validate() != nil {
		return f.collectionStorageFailure(ErrCollectionInvalid)
	}
	f.image.Collections[s.ID] = s
	f.discardCollectionPlanPrefix(s.ID)
	f.discardCollectionValidationPlan(s.ID)
	result := collectionResult(s)
	result.Events = []Event{collectionReceiptEvent(collectionReceiptFor(s))}
	return result
}
