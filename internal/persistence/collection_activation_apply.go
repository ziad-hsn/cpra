package persistence

import "time"

func (f *machine) checkCollectionActivationAuthority(s CollectionState, c CollectionCommand, at time.Time) error {
	a := c.ActivationAuthority
	if a == nil || s.Owner == nil || s.Owner.Epoch != a.Epoch || s.Actor != a.Actor {
		return ErrOperatorAuthorityDenied
	}
	return f.checkOperatorAuthority(*a, at)
}

func (f *machine) admitCollectionActivation(c CollectionCommand, at time.Time) Result {
	epoch, sequence, _ := ParseOperationHandle(c.OperationID)
	if epoch != f.image.OperationEpoch {
		return Result{Err: ErrOperationExpired}
	}
	s, exists := f.image.Collections[c.OperationID]
	if !exists {
		if sequence <= f.image.OperationHighWater {
			return Result{Err: ErrOperationExpired}
		}
		return Result{Err: ErrOperationNotFound}
	}
	if s.UploadID != c.UploadID || at.Before(s.ActivityAt) {
		return Result{Err: ErrCollectionConflict}
	}
	if err := f.checkCollectionActivationAuthority(s, c, at); err != nil {
		return Result{Err: err}
	}
	if s.Activation != nil {
		if !collectionActivationsEqual(s.Activation, c.Activation) || at.Before(s.Activation.At) ||
			!s.TerminalAt.IsZero() && at.Before(s.TerminalAt) || s.Phase != "applying" && s.Phase != "canceled" {
			return Result{Err: ErrCollectionConflict}
		}
		// Reconcile the original disposition without granting an item execution,
		// refreshing the admitted authority or reopening a canceled operation.
		return collectionResult(s)
	}
	if s.Phase != "validated" || !c.Activation.At.Equal(at) || c.Activation.Authority != *c.ActivationAuthority {
		return Result{Err: ErrCollectionConflict}
	}
	if !at.Before(s.ExpiresAt) {
		return Result{Err: ErrOperationExpired}
	}
	s = s.Clone()
	a := c.Activation.Clone()
	s.Activation, s.Phase = &a, "applying"
	if s.validate() != nil {
		return Result{Err: ErrCollectionConflict}
	}
	f.image.Collections[s.ID] = s
	f.image.Version = max(f.image.Version, CollectionActivationFormatVersion)
	r := collectionResult(s)
	r.Events = []Event{{MonitorID: "collection-activation/" + s.ID, At: at, Type: "collection_activation_admitted", Kind: "Collection",
		Actor: s.Actor, ActionID: a.ID, Outcome: "applying"}}
	return r
}
