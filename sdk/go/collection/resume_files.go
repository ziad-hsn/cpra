package collection

import (
	"context"
	"errors"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// ContinuationOperations combines original-input verification and collection
// activation. cpra.OperationsService implements it. No replacement creation is
// used by ResumeFiles, even though Operations also supports ordinary Apply.
type ContinuationOperations interface {
	Operations
	ReselectionOperations
}

// ResumeFiles verifies exact raw source bytes, source order, and ordered resource
// identities using the server-held encrypted original key. Verification is
// required even when every resource was already uploaded. An ordinary unprofiled
// file operation is not relabeled as a versioned normalization profile.
//
// It fills missing uploads, reconciles the original phase, then requests whole
// input validation and conditional activation. No mutation is automatically
// retried. A stopped Wait or context never cancels the server operation. Sources
// and attempt spools retain Reselect's byte/count limits and ownership rules.
func ResumeFiles(ctx context.Context, operations ContinuationOperations, id string, sources []Source, options ReselectionOptions) (Result, error) {
	result := Result{OperationID: id}
	reselected, err := reselect(ctx, operations, id, sources, options, true)
	result.Operation = reselected.Operation
	if reselected.Attempt != nil {
		result.VerificationAttemptID = reselected.Attempt.ID
	}
	if err != nil {
		return result, err
	}
	if !reselected.Complete {
		return result, ErrProgressUnavailable
	}
	identity := result.Operation
	match := func(o api.Operation) bool {
		return o.ID == id && o.IdentityFormat == identity.IdentityFormat && o.ContentDigest == identity.ContentDigest &&
			o.NormalizationProfile == identity.NormalizationProfile && o.ItemCount != nil && *o.ItemCount == *identity.ItemCount &&
			o.Uploaded != nil && *o.Uploaded == *identity.ItemCount
	}
	observed, err := operations.Get(ctx, id)
	if err != nil {
		return result, err
	}
	if observed == nil || !match(observed.Data) {
		return result, ErrReselectionObservation
	}
	result.Operation = observed.Data
	if result.Operation.State == "uploading" {
		admitted, err := operations.Validate(ctx, id)
		if err != nil {
			return result, err
		}
		if admitted == nil || !match(admitted.Data) {
			return result, reselectionAmbiguous(ErrReselectionObservation, id)
		}
		result.Operation = admitted.Data
	}
	switch result.Operation.State {
	case "validating", "validated":
	case "applying":
		return result, nil
	default:
		if executionAdmitted(result.Operation) {
			return result, nil
		}
		return result, ErrReselectionState
	}
	page, err := operations.WaitValidation(ctx, id)
	if err != nil {
		return result, err
	}
	if page == nil || page.Data.OperationID != id || page.Data.IdentityFormat != commitment.Format ||
		page.Data.ContentDigest != identity.ContentDigest || page.Data.ItemCount != *identity.ItemCount {
		return result, ErrReselectionObservation
	}
	if err := api.ValidateValidationResultPage(page.Data); err != nil {
		return result, err
	}
	result.Validation = &page.Data.Summary
	if !page.Data.Summary.Valid {
		return result, ErrPreflightRejected
	}
	// The server proved every ordered resource against the original encrypted
	// identity. The sealed validation summary must cover that same whole input.
	activated, err := operations.Activate(ctx, id)
	if err == nil && (activated == nil || !match(activated.Data) || activated.Data.State != "applying" && !executionAdmitted(activated.Data)) {
		err = reselectionAmbiguous(ErrReselectionObservation, id)
	}
	if err == nil {
		result.Operation = activated.Data
		return result, nil
	}
	if errors.Is(err, cpra.ErrAmbiguous) && ctx.Err() == nil {
		last, readErr := operations.Get(ctx, id)
		if readErr == nil && last != nil && match(last.Data) {
			result.Operation = last.Data
			if last.Data.State == "applying" || executionAdmitted(last.Data) {
				return result, nil
			}
		}
	}
	return result, err
}
