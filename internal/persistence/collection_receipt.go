package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/raft"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// CollectionReceipt is an allowlisted observation of inactive input. It contains
// no secret envelope, provider configuration, source path or source fingerprint.
// Terminal receipts are retained in history independently of upload cleanup.
// These initial terminal phases make no claim that resources were activated.
type CollectionReceipt struct {
	// ExecutionObservation is read-only metadata and never changes historical
	// receipt bytes, cancellation identity, or command compatibility.
	ExecutionObservation *CollectionExecutionObservation `json:"-"`
	Execution            *CollectionExecutionSummary     `json:"execution,omitempty"`
	Activation           *CollectionActivation           `json:"activation,omitempty"`
	ValidationRequest    *CollectionValidationRequest    `json:"validation_request,omitempty"`
	Validation           *CollectionValidationReceipt    `json:"validation,omitempty"`
	ID                   string                          `json:"id"`
	UploadID             string                          `json:"upload_id"`
	Actor                string                          `json:"actor"`
	NormalizationProfile string                          `json:"normalization_profile,omitempty"`
	IdentityFormat       string                          `json:"identity_format"`
	ContentDigest        string                          `json:"content_digest"`
	ItemCount            uint64                          `json:"item_count"`
	Uploaded             uint64                          `json:"uploaded"`
	Phase                string                          `json:"phase"`
	CreatedAt            time.Time                       `json:"created_at"`
	ActivityAt           time.Time                       `json:"activity_at"`
	ExpiresAt            time.Time                       `json:"expires_at"`
	TerminalAt           time.Time                       `json:"terminal_at,omitempty"`
	CancellationID       string                          `json:"cancellation_id,omitempty"`
	InvalidatedByRestore string                          `json:"invalidated_by_restore,omitempty"`
}

func (r CollectionReceipt) Clone() CollectionReceipt {
	if r.ExecutionObservation != nil {
		v := r.ExecutionObservation.Clone()
		r.ExecutionObservation = &v
	}
	if r.Execution != nil {
		v := r.Execution.Clone()
		r.Execution = &v
	}
	if r.Activation != nil {
		a := r.Activation.Clone()
		r.Activation = &a
	}
	if r.ValidationRequest != nil {
		request := r.ValidationRequest.Clone()
		r.ValidationRequest = &request
	}
	if r.Validation != nil {
		v := *r.Validation
		r.Validation = &v
	}
	return r
}

func (r CollectionReceipt) validate() error {
	if collectionExecutionCompleted(r.Phase) {
		v := r.Execution
		if v == nil || v.validate() != nil || v.Outcome != r.Phase || v.Binding.OperationID != r.ID || v.Binding.UploadID != r.UploadID || v.Actor != r.Actor || v.IdentityFormat != r.IdentityFormat || v.NormalizationProfile != r.NormalizationProfile || v.ContentDigest != r.ContentDigest || v.ItemCount != r.ItemCount || !v.FinalizedAt.Equal(r.TerminalAt) || r.Activation == nil || v.Binding.ActivationID != r.Activation.ID || v.Binding.PlanID != r.Activation.PlanID || v.Binding.PlanDigest != r.Activation.PlanDescriptor.Digest || !v.ActivationAt.Equal(r.Activation.At) {
			return ErrCollectionInvalid
		}
	} else if r.Execution != nil {
		return ErrCollectionInvalid
	}
	if a := r.Activation; a != nil {
		if a.validate() != nil || a.Authority.Actor != r.Actor || a.ItemCount != r.ItemCount || r.Uploaded != r.ItemCount ||
			a.At.Before(r.ActivityAt) || !a.At.Before(r.ExpiresAt) ||
			!collectionValidationRequestFenceEqual(a.ValidationRequest, receiptValidationRequestFence(r.ValidationRequest)) ||
			r.Phase != "applying" && r.Phase != "canceled" && r.Phase != "invalidated" && !collectionExecutionCompleted(r.Phase) ||
			!r.TerminalAt.IsZero() && r.TerminalAt.Before(a.At) || collectionTerminal(r.Phase) && r.Validation == nil {
			return ErrCollectionInvalid
		}
		if v := r.Validation; v != nil && (!v.Header.Valid || v.Header.Authority.Epoch != a.Authority.Epoch ||
			v.Header.ResultID != a.ResultID || v.Descriptor != a.ResultDescriptor || v.Header.InputProgressDigest != a.InputProgressDigest ||
			v.Header.PlanID != a.PlanID || v.Header.PlanDigest != a.PlanDescriptor.Digest || v.Header.CapabilitiesDigest != a.CapabilitiesDigest ||
			a.At.Before(v.FinalizedAt) || !a.At.Before(v.FinalizedAt.AddDate(0, 0, 30))) {
			return ErrCollectionInvalid
		}
	} else if r.Phase == "applying" {
		return ErrCollectionInvalid
	}
	if request := r.ValidationRequest; request != nil {
		if request.validate() != nil || request.Authority.Actor != r.Actor || request.ItemCount != r.ItemCount || r.Uploaded != r.ItemCount || r.Phase == "uploading" ||
			request.RequestedAt.Before(r.CreatedAt) || request.RequestedAt.After(r.ActivityAt) ||
			request.Claim != nil && request.Claim.At.After(r.ActivityAt) {
			return ErrCollectionInvalid
		}
		if interruption := request.Interruption; interruption != nil {
			if r.Phase != "interrupted" || !interruption.At.Equal(r.TerminalAt) || interruption.At.Before(r.ActivityAt) || !interruption.At.Before(r.ExpiresAt) || r.Validation != nil {
				return ErrCollectionInvalid
			}
		}
		if v := r.Validation; v != nil && (request.Claim == nil || request.Authority != v.Header.Authority ||
			request.CapabilitiesDigest != v.Header.CapabilitiesDigest || request.InputProgressDigest != v.Header.InputProgressDigest ||
			v.FinalizedAt.Before(request.Claim.At)) {
			return ErrCollectionInvalid
		}
	}
	if v := r.Validation; v != nil && (v.validate() != nil || v.Header.OperationID != r.ID || v.Header.UploadID != r.UploadID ||
		v.Header.Authority.Actor != r.Actor || v.Header.ItemCount != r.ItemCount || v.FinalizedAt.Before(r.CreatedAt) || v.FinalizedAt.After(r.ActivityAt)) {
		return ErrCollectionInvalid
	}
	if _, _, err := ParseOperationHandle(r.ID); err != nil || !validOperationEpoch(r.UploadID) || !catalogIdentifier(r.Actor, 128) ||
		r.IdentityFormat != commitment.Format || !validCollectionNormalizationProfile(r.NormalizationProfile) || !bootstrapHash(r.ContentDigest) || r.ItemCount == 0 || r.ItemCount > maxCollectionItems || r.Uploaded > r.ItemCount ||
		r.CreatedAt.IsZero() || r.ActivityAt.Before(r.CreatedAt) || !r.ExpiresAt.Equal(r.ActivityAt.Add(CollectionInactivityLifetime)) {
		return ErrCollectionInvalid
	}
	if collectionLive(r.Phase) {
		if !r.TerminalAt.IsZero() || r.CancellationID != "" || r.InvalidatedByRestore != "" {
			return ErrCollectionInvalid
		}
		if r.Phase == "uploading" && r.Validation != nil || r.Phase == "validated" && (r.Validation == nil || !r.Validation.Header.Valid) || r.Phase == "rejected" && (r.Validation == nil || r.Validation.Header.Valid) {
			return ErrCollectionInvalid
		}
		return nil
	}
	if r.TerminalAt.IsZero() || r.TerminalAt.Before(r.ActivityAt) {
		return ErrCollectionInvalid
	}
	switch r.Phase {
	case "completed", "partial", "failed":
		if r.CancellationID != "" || r.InvalidatedByRestore != "" {
			return ErrCollectionInvalid
		}
	case "interrupted":
		if r.ValidationRequest == nil || r.ValidationRequest.Interruption == nil || r.CancellationID != "" || r.InvalidatedByRestore != "" {
			return ErrCollectionInvalid
		}
	case "canceled":
		if !validOperationEpoch(r.CancellationID) || r.InvalidatedByRestore != "" || r.Activation == nil && !r.TerminalAt.Before(r.ExpiresAt) {
			return ErrCollectionInvalid
		}
	case "expired":
		if r.CancellationID != "" || r.InvalidatedByRestore != "" || r.TerminalAt.Before(r.ExpiresAt) {
			return ErrCollectionInvalid
		}
	case "invalidated":
		if r.CancellationID != "" || !validOperationEpoch(r.InvalidatedByRestore) {
			return ErrCollectionInvalid
		}
	default:
		return ErrCollectionInvalid
	}
	return nil
}

func collectionReceiptFor(s CollectionState) CollectionReceipt {
	r := CollectionReceipt{ID: s.ID, UploadID: s.UploadID, Actor: s.Actor, NormalizationProfile: s.NormalizationProfile, IdentityFormat: s.IdentityFormat, ContentDigest: s.ContentDigest,
		ItemCount: s.ItemCount, Uploaded: s.Uploaded, Phase: s.Phase, CreatedAt: s.CreatedAt, ActivityAt: s.ActivityAt, ExpiresAt: s.ExpiresAt,
		TerminalAt: s.TerminalAt, InvalidatedByRestore: s.InvalidatedByRestore}
	if s.ExecutionResult != nil && collectionExecutionCompleted(s.Phase) {
		v := s.ExecutionResult.Summary.Clone()
		r.Execution = &v
	}
	if s.Activation != nil {
		a := s.Activation.Clone()
		r.Activation = &a
	}
	r.Validation = collectionValidationReceiptFor(s.Validation)
	if s.ValidationRequest != nil {
		request := s.ValidationRequest.Clone()
		r.ValidationRequest = &request
	}
	if collectionInactive(s.Phase) && s.Phase != "uploading" && (s.Validation == nil || !s.Validation.HistorySealed) {
		r.Phase = "validating" // Structural plans and unsealed results are provisional.
	}
	if s.Cancellation != nil {
		r.CancellationID = s.Cancellation.ID
	}
	return r
}

func collectionReceiptEvent(r CollectionReceipt) Event {
	e := Event{MonitorID: "collection/" + r.ID, Revision: r.ContentDigest, At: r.TerminalAt, Type: "collection_" + r.Phase,
		Kind: "Collection", Actor: r.Actor, Outcome: r.Phase, Collection: &r}
	switch r.Phase {
	case "interrupted":
		if r.ValidationRequest != nil && r.ValidationRequest.Interruption != nil {
			e.ActionID = r.ValidationRequest.Interruption.ID
			e.Reason = r.ValidationRequest.Interruption.Reason
		}
	case "canceled":
		e.ActionID = r.CancellationID
	case "expired":
		e.Reason = "inactivity"
	case "invalidated":
		e.Reason = "explicit_restore"
	}
	return e
}

func collectionReceiptsEqual(a, b CollectionReceipt) bool {
	a.ExecutionObservation, b.ExecutionObservation = nil, nil
	leftExecution, rightExecution := a.Execution, b.Execution
	a.Execution, b.Execution = nil, nil
	leftActivation, rightActivation := a.Activation, b.Activation
	a.Activation, b.Activation = nil, nil
	left, right := a.Validation, b.Validation
	a.Validation, b.Validation = nil, nil
	leftRequest, rightRequest := a.ValidationRequest, b.ValidationRequest
	a.ValidationRequest, b.ValidationRequest = nil, nil
	a.CreatedAt, b.CreatedAt = a.CreatedAt.UTC(), b.CreatedAt.UTC()
	a.ActivityAt, b.ActivityAt = a.ActivityAt.UTC(), b.ActivityAt.UTC()
	a.ExpiresAt, b.ExpiresAt = a.ExpiresAt.UTC(), b.ExpiresAt.UTC()
	a.TerminalAt, b.TerminalAt = a.TerminalAt.UTC(), b.TerminalAt.UTC()
	return a == b && collectionExecutionSummariesEqual(leftExecution, rightExecution) && collectionActivationsEqual(leftActivation, rightActivation) && collectionValidationRequestsEqual(leftRequest, rightRequest) &&
		(left == nil && right == nil || left != nil && right != nil && collectionValidationReceiptsEqual(*left, *right))
}

// CollectionReceipt returns an active upload observation or its retained terminal
// result. An old storage epoch is expired even when its historical evidence is
// retained. Missing issued handles never allocate or execute another operation.
// Supply the current observation time; reads do not extend any deadline.
func (s *Store) CollectionReceipt(ctx context.Context, id string, at time.Time) (CollectionReceipt, error) {
	epoch, seq, err := ParseOperationHandle(id)
	if err != nil {
		return CollectionReceipt{}, ErrOperationNotFound
	}
	if at.IsZero() {
		return CollectionReceipt{}, ErrCollectionInvalid
	}
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return CollectionReceipt{}, err
	}
	defer s.mu.RUnlock()
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return CollectionReceipt{}, err
	}
	defer s.fsm.mu.RUnlock()
	f := s.fsm
	select {
	case <-s.stop:
		return CollectionReceipt{}, ErrCollectionUnavailable
	default:
	}
	if s.err != nil || f.err != nil || f.bootstrapPending() || f.restorePending() ||
		f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		return CollectionReceipt{}, ErrCollectionUnavailable
	}
	if s.raft != nil && s.raft.State() != raft.Leader {
		return CollectionReceipt{}, errors.Join(ErrCollectionUnavailable, raft.ErrNotLeader)
	}
	if epoch != f.image.OperationEpoch {
		return CollectionReceipt{}, ErrOperationExpired
	}
	if seq > f.image.OperationHighWater {
		return CollectionReceipt{}, ErrOperationNotFound
	}
	var expected *CollectionReceipt
	if header, ok := f.image.Collections[id]; ok {
		if header.ID != id || header.validate() != nil {
			return CollectionReceipt{}, ErrCollectionUnavailable
		}
		if collectionLive(header.Phase) {
			if header.Activation != nil && at.Before(header.Activation.At) {
				return CollectionReceipt{}, ErrCollectionConflict
			}
			// Expired validation cannot return to a provisional state when the
			// supplied clock moves backward. Terminal operation receipts remain
			// independent: a retained cancellation must still be observable.
			if header.Validation != nil && !header.Validation.HistoryExpiredAt.IsZero() {
				return CollectionReceipt{}, ErrOperationExpired
			}
			if collectionInactive(header.Phase) && !at.Before(header.ExpiresAt) {
				return CollectionReceipt{}, ErrOperationExpired
			}
			r := collectionReceiptFor(header)
			if header.Phase == "applying" && r.Validation != nil && !at.Before(r.Validation.FinalizedAt.AddDate(0, 0, 30)) {
				r.Validation = nil
			}
			if r.validate() != nil {
				return CollectionReceipt{}, ErrCollectionUnavailable
			}
			if r.Phase == "validated" || r.Phase == "rejected" {
				// A committed publication marker alone does not prove that its
				// materialization is still retained or readable after clock/GC changes.
				if _, err := f.history.collectionValidationPage(ctx, *r.Validation, f.image.Index, 0, 1, at); err != nil {
					return CollectionReceipt{}, err
				}
			}
			return r, nil
		}
		if !header.TerminalAt.IsZero() {
			r := collectionReceiptFor(header)
			expected = &r
		}
	}
	r, err := f.history.collectionReceiptExpected(ctx, id, f.image.Index, at, expected)
	if err == nil && expected != nil && !collectionReceiptsEqual(r, *expected) {
		return CollectionReceipt{}, ErrHistoryUnavailable
	}
	if errors.Is(err, ErrOperationNotFound) {
		return CollectionReceipt{}, ErrOperationExpired
	}
	return r, err
}

func receiptValidationRequestFence(r *CollectionValidationRequest) *CollectionValidationRequestFence {
	return CollectionValidationRequestFenceFor(CollectionState{ValidationRequest: r})
}
