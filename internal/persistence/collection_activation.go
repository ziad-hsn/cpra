package persistence

import "time"

// CollectionActivationFormatVersion adds immutable admission metadata. It does
// not add an item executor, an outcome namespace or a public activation route.
const CollectionActivationFormatVersion = 8

// CollectionActivation binds admission to the original sealed successful result
// and complete plan. Authority and At describe the first admission. Retries use
// a separate current command authority and never replace these observations.
type CollectionActivation struct {
	ID                  string                            `json:"id"`
	Authority           OperatorAuthority                 `json:"authority"`
	InputProgressDigest string                            `json:"input_progress_digest"`
	ItemCount           uint64                            `json:"item_count"`
	ResultID            string                            `json:"result_id"`
	ResultDescriptor    CollectionValidationDescriptor    `json:"result_descriptor"`
	PlanID              string                            `json:"plan_id"`
	PlanDescriptor      CollectionPlanDescriptor          `json:"plan_descriptor"`
	CapabilitiesDigest  string                            `json:"capabilities_digest"`
	ValidationRequest   *CollectionValidationRequestFence `json:"validation_request,omitempty"`
	At                  time.Time                         `json:"at"`
}

type CollectionActivationFence struct {
	ID       string `json:"id"`
	ResultID string `json:"result_id"`
	PlanID   string `json:"plan_id"`
}

func (a CollectionActivation) Clone() CollectionActivation {
	if a.ValidationRequest != nil {
		p := *a.ValidationRequest
		a.ValidationRequest = &p
	}
	return a
}

func (a CollectionActivation) validate() error {
	if !validOperationEpoch(a.ID) || a.Authority.validate() != nil || !bootstrapHash(a.InputProgressDigest) ||
		a.ItemCount == 0 || a.ItemCount > CollectionValidationMaxItems || !validOperationEpoch(a.ResultID) ||
		a.ResultDescriptor.Count != a.ItemCount || a.ResultDescriptor.Bytes < 4*a.ItemCount ||
		a.ResultDescriptor.Bytes > CollectionValidationResultMaxBytes || !bootstrapHash(a.ResultDescriptor.Digest) ||
		!validOperationEpoch(a.PlanID) || !collectionPlanDescriptorValid(a.PlanDescriptor) ||
		!bootstrapHash(a.CapabilitiesDigest) || a.At.IsZero() ||
		a.ValidationRequest != nil && (a.ValidationRequest.validate() != nil || a.ValidationRequest.ClaimID == "") {
		return ErrCollectionInvalid
	}
	return nil
}

func (a CollectionActivation) validateState(s CollectionState) error {
	if a.validate() != nil || s.Owner == nil || s.Owner.Epoch != a.Authority.Epoch || s.Actor != a.Authority.Actor ||
		a.ItemCount != s.ItemCount || s.Uploaded != s.ItemCount || a.InputProgressDigest != s.ProgressDigest ||
		!collectionValidationRequestFenceEqual(a.ValidationRequest, CollectionValidationRequestFenceFor(s)) ||
		a.At.Before(s.ActivityAt) || !a.At.Before(s.ExpiresAt) ||
		s.Phase != "applying" && s.Phase != "canceled" && s.Phase != "invalidated" && !collectionExecutionCompleted(s.Phase) {
		return ErrCollectionInvalid
	}
	v, p := s.Validation, s.Plan
	if v == nil || p == nil || !v.Header.Valid || v.Header.SummaryOnly || v.FinalizedAt.IsZero() ||
		!v.HistorySealed || !v.HistoryExpiredAt.IsZero() || v.Header.ResultID != a.ResultID || v.Descriptor != a.ResultDescriptor ||
		v.Header.CapabilitiesDigest != a.CapabilitiesDigest || v.Header.PlanID != a.PlanID || v.Header.PlanDigest != a.PlanDescriptor.Digest ||
		p.Header.PlanID != a.PlanID || p.Descriptor != a.PlanDescriptor || p.FinalizedAt.IsZero() ||
		a.At.Before(v.FinalizedAt) || a.At.Before(p.FinalizedAt) || !a.At.Before(v.FinalizedAt.AddDate(0, 0, 30)) {
		return ErrCollectionInvalid
	}
	if s.Phase == "applying" && (s.RemovedRows != 0 || s.RemovedBytes != 0 || p.RemovedFragments != 0 || p.RemovedBytes != 0 ||
		v.RemovedRows != 0 || v.RemovedBytes != 0 || !s.TerminalAt.IsZero()) ||
		!s.TerminalAt.IsZero() && s.TerminalAt.Before(a.At) {
		return ErrCollectionInvalid
	}
	return nil
}

func (p CollectionActivationFence) validate() error {
	if !validOperationEpoch(p.ID) || !validOperationEpoch(p.ResultID) || !validOperationEpoch(p.PlanID) {
		return ErrCollectionInvalid
	}
	return nil
}

func CollectionActivationFenceFor(s CollectionState) *CollectionActivationFence {
	if s.Activation == nil {
		return nil
	}
	return &CollectionActivationFence{ID: s.Activation.ID, ResultID: s.Activation.ResultID, PlanID: s.Activation.PlanID}
}

func collectionActivationFenceEqual(a, b *CollectionActivationFence) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func collectionActivationsEqual(a, b *CollectionActivation) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	x, y := *a, *b
	x.ValidationRequest, y.ValidationRequest = nil, nil
	x.At, y.At = time.Time{}, time.Time{}
	return x == y && a.At.Equal(b.At) && collectionValidationRequestFenceEqual(a.ValidationRequest, b.ValidationRequest)
}

func collectionLive(phase string) bool { return collectionInactive(phase) || phase == "applying" }

func collectionTerminal(phase string) bool {
	return phase == "canceled" || phase == "expired" || phase == "invalidated" || phase == "interrupted" || collectionExecutionCompleted(phase)
}

func collectionActivationCommand(c CollectionCommand) bool {
	return c.Action == "activation_admit" || c.Activation != nil || c.ActivationFence != nil || c.ActivationAuthority != nil ||
		c.Cleanup != nil && c.Cleanup.Activation != nil || c.Create != nil && c.Create.Activation != nil ||
		c.ValidationProgress != nil && c.ValidationProgress.Activation != nil
}

func (c CollectionCommand) validateActivation(at time.Time) error {
	if _, _, err := ParseOperationHandle(c.OperationID); err != nil || !validOperationEpoch(c.UploadID) ||
		c.Activation == nil || c.Activation.validate() != nil || c.Activation.At.After(at) ||
		c.ActivationAuthority == nil || c.ActivationAuthority.validate() != nil || c.ActivationFence != nil ||
		c.Epoch != "" || c.Create != nil || c.Item != nil || c.Cleanup != nil || c.Cancel != nil ||
		c.PlanID != "" || c.PlanBegin != nil || c.PlanFragment != nil || c.PlanFinalize != nil ||
		c.ValidationID != "" || c.ValidationBegin != nil || len(c.ValidationItems) != 0 || c.ValidationPublished != 0 ||
		c.ValidationRequest != nil || c.ValidationClaim != nil || c.ValidationInterruption != nil || c.ValidationFence != nil || c.ValidationProgress != nil {
		return ErrCollectionInvalid
	}
	return nil
}
