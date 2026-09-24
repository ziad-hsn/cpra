package collection

import (
	"context"
	"errors"
	"fmt"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// ReselectionOperations exposes only original-upload recovery. It deliberately
// excludes collection creation, validation, activation, cancellation and discard.
type ReselectionOperations interface {
	Get(context.Context, string) (*cpra.Response[api.Operation], error)
	CreateReselection(context.Context, string, api.CollectionReselectionCreateRequest) (*cpra.Response[api.CollectionReselectionAttempt], error)
	GetReselection(context.Context, string, string) (*cpra.Response[api.CollectionReselectionAttempt], error)
	UploadReselectionSource(context.Context, string, string, cpra.ReselectionSourcePart) (*cpra.Response[api.CollectionReselectionAttempt], error)
	VerifyReselection(context.Context, string, string) (*cpra.Response[api.CollectionReselectionAttempt], error)
	ResumeReselection(context.Context, string, string) (*cpra.Response[api.CollectionReselectionAttempt], error)
}

// ReselectionOptions selects a known attempt and bounds raw source acquisition.
// Sources uses Options' file expansion and separate unauthenticated URL client.
// Raw source bytes never exceed 64 MiB; a smaller MaxSourceBytes or
// MaxStagingBytes lowers that limit. Resource decoding remains server-owned.
type ReselectionOptions struct {
	AttemptID string
	Sources   Options
}

// ReselectionResult retains the original handle and last checked metadata on
// failure. Complete means only that the original upload is full; it does not
// mean validation, activation, controller application or external effects.
type ReselectionResult struct {
	OperationID string
	Operation   api.Operation
	Attempt     *api.CollectionReselectionAttempt
	Complete    bool
}

func (ReselectionResult) String() string               { return "original upload recovery result (metadata omitted)" }
func (r ReselectionResult) GoString() string           { return r.String() }
func (r ReselectionResult) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(r.String())) }

// ErrReselectionState means the original operation or attempt cannot continue.
// A failed attempt's allowlisted category remains in ReselectionResult.Attempt.
var ErrReselectionState = errors.New("original upload cannot continue in its observed state")

// ErrReselectionObservation means a response cannot establish the original
// identity or monotone progress. Mutation responses also match cpra.ErrAmbiguous.
var ErrReselectionObservation = errors.New("original upload recovery observation is invalid")

// Reselect verifies selected raw files and completes only the missing upload
// suffix under the server-held original identity. It never creates a replacement
// collection, validates configuration, activates it or cancels server work.
//
// All required raw inputs are privately frozen before creating an attempt or
// uploading a part. Explicit source order and lexical directory expansion match
// Freeze; retain the original source boundaries and include empty sources.
// The supported original profile is FileNormalizationProfile. Ordinary unprofiled
// Freeze/FreezeResources operations cannot be relabeled by this helper.
//
// A supplied AttemptID is read before continuing its observed phase. Its already
// staged raw bytes remain authoritative: selected sources supply only missing
// bytes. Same-length edits to a previously staged prefix are not compared with
// the newly selected prefix; the server proves its complete retained input
// against the original collection identity before appending any resources.
// Sources are needed only for an uploading attempt; verification and transfer
// can finish without reopening files. An already full original upload returns Complete
// without consulting a possibly disappeared attempt. Lost mutation replies stop
// immediately: call again explicitly with the known attempt ID to reconcile.
// A lost create reply without an attempt ID cannot be rediscovered by this API.
// An initially observed failed/transfer_failed attempt may receive one explicit
// Resume request; the server decides whether its retained transfer can continue.
// A failure observed later in this invocation stops without retrying.
// Polling is read-only, at least five seconds apart, and does not renew expiry.
// Reader sources remain caller-owned; callers must interrupt blocking readers
// themselves when their context is canceled.
// Context cancellation stops this helper, never an admitted server operation.
func Reselect(ctx context.Context, operations ReselectionOperations, operationID string, sources []Source, options ReselectionOptions) (result ReselectionResult, err error) {
	if !reselectionHandle(operationID, 128) {
		return result, errors.New("an original operation handle is required")
	}
	result.OperationID = operationID
	defer func() {
		if err != nil {
			err = &reselectionFailure{cause: err}
		}
	}()
	if ctx == nil || operations == nil || options.AttemptID != "" && !reselectionUUID(options.AttemptID) {
		return result, errors.New("invalid original upload recovery options")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	first, err := operations.Get(ctx, operationID)
	if err != nil {
		return result, err
	}
	original, err := reselectionOperation(first, operationID)
	if err != nil {
		return result, err
	}
	result.Operation = original
	if *original.Uploaded == *original.ItemCount {
		result.Complete = true
		return result, nil
	}
	if original.State != "uploading" || first.Data.ExecutionResult != nil || original.Committed != nil && *original.Committed != 0 || original.Applied != nil && *original.Applied != 0 {
		return result, ErrReselectionState
	}
	var response *cpra.Response[api.CollectionReselectionAttempt]
	if options.AttemptID != "" {
		response, err = operations.GetReselection(ctx, operationID, options.AttemptID)
		if err != nil {
			return result, err
		}
		if err := reselectionObserve(&result, response, options.AttemptID, nil); err != nil {
			return result, err
		}
		// A known attempt's fixed deadline also bounds local source freezing.
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, result.Attempt.ExpiresAt)
		defer cancel()
	}
	retryTransfer := result.Attempt != nil && result.Attempt.Phase == "failed" && result.Attempt.ErrorCode != nil && *result.Attempt.ErrorCode == "transfer_failed" && result.Attempt.SourcesCompleted == result.Attempt.SourceCount
	var spool *reselectionSources
	if result.Attempt == nil || result.Attempt.Phase == "uploading" {
		spool, err = freezeReselectionSources(ctx, sources, options.Sources)
		if err != nil {
			return result, err
		}
		defer func() { err = errors.Join(err, spool.close()) }()
		if result.Attempt != nil && result.Attempt.SourceCount != int64(len(spool.records)) {
			return result, ErrReselectionObservation
		}
	}
	if result.Attempt == nil {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		response, err = operations.CreateReselection(ctx, operationID, api.CollectionReselectionCreateRequest{NormalizationProfile: FileNormalizationProfile, SourceCount: int64(len(spool.records))})
		if err != nil {
			return result, err
		}
		if err := reselectionObserve(&result, response, "", nil); err != nil {
			return result, reselectionAmbiguous(err, operationID)
		}
		if result.Attempt.SourceCount != int64(len(spool.records)) || result.Attempt.Phase != "uploading" || result.Attempt.SourcesCompleted != 0 || result.Attempt.RawBytes != 0 || result.Attempt.NextSource != 1 || result.Attempt.NextOffset != 0 {
			result.Attempt = nil
			return result, reselectionAmbiguous(ErrReselectionObservation, operationID)
		}
	}
	work, cancel := context.WithDeadline(ctx, result.Attempt.ExpiresAt)
	defer cancel()
	for {
		if err := work.Err(); err != nil {
			return result, err
		}
		prior := *result.Attempt
		var mutation, resuming bool
		switch prior.Phase {
		case "uploading":
			if prior.SourcesCompleted == prior.SourceCount {
				if err := spool.check(prior); err != nil {
					return result, err
				}
				response, err = operations.VerifyReselection(work, operationID, prior.ID)
				mutation = true
			} else {
				part, partErr := spool.part(prior)
				if partErr != nil {
					return result, partErr
				}
				response, err = operations.UploadReselectionSource(work, operationID, prior.ID, part)
				clear(part.Data)
				if err != nil {
					return result, err
				}
				if err := reselectionObserve(&result, response, prior.ID, &prior); err != nil {
					return result, reselectionAmbiguous(err, operationID)
				}
				if err := spool.covers(*result.Attempt, part); err != nil {
					result.Attempt = &prior
					return result, reselectionAmbiguous(err, operationID)
				}
				continue
			}
		case "verifying", "transferring":
			if err := waitReselection(work, response.RetryAfter); err != nil {
				return result, err
			}
			response, err = operations.GetReselection(work, operationID, prior.ID)
		case "verified":
			response, err = operations.ResumeReselection(work, operationID, prior.ID)
			mutation, resuming = true, true
		case "completed":
			last, readErr := operations.Get(work, operationID)
			if readErr != nil {
				return result, readErr
			}
			current, readErr := reselectionOperation(last, operationID)
			if readErr != nil || current.IdentityFormat != original.IdentityFormat || current.NormalizationProfile != original.NormalizationProfile || current.ContentDigest != original.ContentDigest || current.ItemCount == nil || *current.ItemCount != *original.ItemCount || current.Uploaded == nil || *current.Uploaded != *original.ItemCount || prior.OperationUploaded != *current.Uploaded {
				return result, ErrReselectionObservation
			}
			result.Operation, result.Complete = current, true
			return result, nil
		case "failed":
			if !retryTransfer {
				return result, ErrReselectionState
			}
			retryTransfer = false
			response, err = operations.ResumeReselection(work, operationID, prior.ID)
			mutation, resuming = true, true
		default:
			return result, ErrReselectionObservation
		}
		if err != nil {
			return result, err
		}
		observationPrior := prior
		if resuming && prior.Phase == "failed" {
			// This one caller-requested retry is the only backward phase edge.
			// Identity, expiry and every progress counter remain pinned.
			observationPrior.Phase, observationPrior.ErrorCode = "verified", nil
		}
		if err := reselectionObserve(&result, response, prior.ID, &observationPrior); err != nil {
			if mutation {
				return result, reselectionAmbiguous(err, operationID)
			}
			return result, err
		}
		// A successful HTTP response must acknowledge the command's admission;
		// accepting its previous phase would submit that mutation twice.
		if mutation && (prior.Phase == "uploading" && result.Attempt.Phase == "uploading" || resuming && result.Attempt.Phase != "transferring" && result.Attempt.Phase != "completed" && result.Attempt.Phase != "failed") {
			result.Attempt = &prior
			return result, reselectionAmbiguous(ErrReselectionObservation, operationID)
		}
	}
}

func waitReselection(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(max(5*time.Second, delay))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func reselectionAmbiguous(err error, id string) error {
	return &cpra.AmbiguousError{Cause: err, OperationID: id}
}

type reselectionFailure struct{ cause error }

func (*reselectionFailure) Error() string {
	return "original upload recovery failed; inspect the original operation and attempt"
}
func (e *reselectionFailure) Unwrap() error              { return e.cause }
func (e *reselectionFailure) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(e.Error())) }
