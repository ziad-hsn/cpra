package collection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

const (
	// MaxChunkItems bounds the resource records in one upload request.
	MaxChunkItems = 256
	// MaxChunkBytes bounds an encoded upload or ephemeral preflight request.
	MaxChunkBytes = 4 << 20
)

// ErrPreflightRejected reports a server validation response whose Valid is false.
var ErrPreflightRejected = errors.New("collection preflight rejected")

// ErrProgressUnavailable means the server did not report upload progress needed
// to resume a mutable phase. No upload, validation or activation was submitted.
var ErrProgressUnavailable = errors.New("operation upload progress is unavailable; no resume mutation was submitted")

// ErrApplyInProgress means this Frozen already has an active Apply or Resume.
// No request from the competing call was submitted.
var ErrApplyInProgress = errors.New("collection application is already in progress")

// Operations is implemented by cpra.OperationsService. Keeping the interface
// narrow also permits protocol contract tests without importing a server.
type Operations interface {
	Preflight(context.Context, api.PreflightRequest) (*cpra.Response[api.Preflight], error)
	Prepare(context.Context, api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error)
	Create(context.Context, api.OperationCreateRequest) (*cpra.Response[api.Operation], error)
	Upload(context.Context, string, api.UploadRequest) (*cpra.Response[api.Operation], error)
	Validate(context.Context, string) (*cpra.Response[api.Operation], error)
	WaitValidation(context.Context, string) (*cpra.Response[api.ValidationResultPage], error)
	Activate(context.Context, string) (*cpra.Response[api.Operation], error)
	Get(context.Context, string) (*cpra.Response[api.Operation], error)
	Cancel(context.Context, string) (*cpra.Response[api.Operation], error)
}

// Result retains the server-issued handle on partial progress or an error. It
// never treats a stopped client as a request to undo or cancel server commits.
type Result struct {
	OperationID string
	Operation   api.Operation
	Preflight   *api.Preflight
	Validation  *api.ValidationResultSummary
	Noop        bool
}

// Preflight performs ephemeral, bounded server validation/diff. It does not
// create a durable apply operation. A collection larger than one 4 MiB request
// fails explicitly: streaming ephemeral validation requires server support.
// Apply can stage a larger collection in encrypted, inactive server storage.
// The ephemeral contract carries exact item commitments without a file-profile
// field. Durable Prepare/Create and operation observations bind that profile.
func Preflight(ctx context.Context, operations Operations, frozen *Frozen) (*api.Preflight, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if frozen == nil {
		return nil, fmt.Errorf("frozen collection is required")
	}
	identity, err := frozen.creationRequest()
	if err != nil {
		return nil, err
	}
	if frozen.Len() == 0 {
		return &api.Preflight{Valid: true, ContentDigest: frozen.Digest(), IdentityFormat: commitment.Format, ItemCount: api.Pointer(int64(0))}, nil
	}
	if operations == nil {
		return nil, fmt.Errorf("operations client is required")
	}
	if frozen.Len() > 10_000 {
		return nil, fmt.Errorf("ephemeral preflight exceeds 10000 resources; use staged validation")
	}
	request := api.PreflightRequest{IdentityFormat: api.PreflightRequestIdentityFormat(commitment.Format), IdentityKey: identity.IdentityKey, SourceFingerprint: identity.SourceFingerprint, ContentDigest: identity.ContentDigest, ItemCount: identity.ItemCount, Items: []api.ApplyItem{}}
	initial, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	encodedBytes := len(initial)
	err = frozen.Range(ctx, func(item Item) error {
		value := applyItem(item)
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		comma := 0
		if len(request.Items) > 0 {
			comma = 1
		}
		if encodedBytes+len(raw)+comma > MaxChunkBytes {
			return fmt.Errorf("ephemeral preflight exceeds 4 MiB; durable staged validation is available through Apply")
		}
		encodedBytes += len(raw) + comma
		request.Items = append(request.Items, value)
		return nil
	})
	if err != nil {
		return nil, err
	}
	response, err := operations.Preflight(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("empty preflight response")
	}
	if response.Data.IdentityFormat != commitment.Format || response.Data.ContentDigest != frozen.Digest() || !matchingCount(response.Data.ItemCount, frozen) {
		return nil, fmt.Errorf("preflight content identity mismatch")
	}
	if !response.Data.Valid {
		return &response.Data, ErrPreflightRejected
	}
	return &response.Data, nil
}

// Diff uses the same ephemeral preflight contract and returns proposed item
// outcomes. It makes no active writes or implicit deletions.
func Diff(ctx context.Context, operations Operations, frozen *Frozen) (*api.Preflight, error) {
	return Preflight(ctx, operations, frozen)
}

// Apply creates inactive staging only after Freeze has fully parsed and locally
// validated every source. It uploads bounded chunks, asks the server to validate
// the entire staged union, and activates only after that succeeds. The server is
// responsible for dependency ordering, authorization, and per-resource CAS.
// Repeating Apply with the same Frozen reuses its original creation ticket,
// including after an uncertain Create response or expiry. It never renews the
// ticket automatically; create a new Frozen only for an intentional new attempt.
func Apply(ctx context.Context, operations Operations, frozen *Frozen) (Result, error) {
	if frozen == nil {
		return Result{}, fmt.Errorf("frozen collection is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	release, err := frozen.beginApply()
	if err != nil {
		return Result{}, err
	}
	defer release()
	request, err := frozen.creationRequest()
	if err != nil {
		return Result{}, err
	}
	if frozen.Len() == 0 {
		return Result{Noop: true}, nil
	}
	if operations == nil {
		return Result{}, fmt.Errorf("operations client is required")
	}
	if request.AdmissionTicket == "" {
		prepared, err := operations.Prepare(ctx, api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(request.IdentityFormat), IdentityKey: request.IdentityKey, SourceFingerprint: request.SourceFingerprint, ContentDigest: request.ContentDigest, ItemCount: request.ItemCount, NormalizationProfile: request.NormalizationProfile})
		if err != nil {
			return Result{}, err
		}
		if prepared == nil {
			return Result{}, errors.New("empty collection admission response")
		}
		if err = frozen.retainAdmission(prepared.Data); err != nil {
			return Result{}, err
		}
		request, err = frozen.creationRequest()
		if err != nil {
			return Result{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	response, err := operations.Create(ctx, request)
	result := Result{}
	if response != nil {
		result.Operation = response.Data
		result.OperationID = response.Data.ID
		if result.OperationID == "" {
			result.OperationID = response.OperationID
		}
	}
	if err != nil {
		return result, err
	}
	if result.OperationID == "" {
		return result, fmt.Errorf("server did not issue an operation handle")
	}
	if !matchingOperation(result.Operation, result.OperationID, frozen) {
		return result, fmt.Errorf("server operation content identity mismatch")
	}
	if result.Operation.State == "validating" || result.Operation.State == "validated" || result.Operation.State == "rejected" {
		return awaitValidationAndActivate(ctx, operations, frozen, result)
	}
	if !uploadable(result.Operation.State) {
		return result, fmt.Errorf("created operation is not accepting uploads")
	}
	return uploadAndActivate(ctx, operations, frozen, result)
}

// Resume explicitly resumes the original operation only when frozen content
// matches it. Identical item uploads are replay-safe by the server contract;
// unknown transport outcomes are returned, never automatically retried here.
func Resume(ctx context.Context, operations Operations, id string, frozen *Frozen) (Result, error) {
	result := Result{OperationID: id}
	if operations == nil || frozen == nil || id == "" {
		return result, fmt.Errorf("operation handle, client, and frozen collection are required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	release, err := frozen.beginApply()
	if err != nil {
		return result, err
	}
	defer release()
	if _, err := frozen.creationRequest(); err != nil {
		return result, err
	}
	response, err := operations.Get(ctx, id)
	if err != nil {
		return result, err
	}
	if response == nil {
		return result, fmt.Errorf("empty operation response")
	}
	result.Operation = response.Data
	if !matchingOperation(result.Operation, id, frozen) {
		return result, fmt.Errorf("operation identity or frozen content changed")
	}
	if uploaded := result.Operation.Uploaded; uploaded != nil && (*uploaded < 0 || *uploaded > int64(frozen.Len())) {
		return result, fmt.Errorf("operation uploaded count is outside the frozen collection")
	}
	switch result.Operation.State {
	case "validating", "validated", "rejected":
		if result.Operation.Uploaded == nil {
			return result, ErrProgressUnavailable
		}
		if *result.Operation.Uploaded != int64(frozen.Len()) {
			return result, fmt.Errorf("validated operation upload count is incomplete")
		}
		return awaitValidationAndActivate(ctx, operations, frozen, result)
	case "staging", "uploading", "pending":
		if result.Operation.Uploaded == nil {
			return result, ErrProgressUnavailable
		}
		return uploadAndActivate(ctx, operations, frozen, result)
	default:
		return result, nil // Applying/terminal/unknown states are never reactivated implicitly.
	}
}

func uploadAndActivate(ctx context.Context, operations Operations, frozen *Frozen, result Result) (Result, error) {
	chunk := api.UploadRequest{}
	chunkBytes := len(`{"items":[]}`)
	flush := func() error {
		if len(chunk.Items) == 0 {
			return nil
		}
		response, err := operations.Upload(ctx, result.OperationID, chunk)
		if response != nil && err == nil {
			if !matchingOperation(response.Data, result.OperationID, frozen) {
				return fmt.Errorf("upload response identity mismatch")
			}
			result.Operation = response.Data
			if !uploadable(response.Data.State) && response.Data.State != "validated" {
				return fmt.Errorf("operation is no longer accepting uploads")
			}
		}
		if err != nil {
			return err
		}
		if response == nil {
			return fmt.Errorf("empty upload response")
		}
		chunk.Items = nil
		chunkBytes = len(`{"items":[]}`)
		return nil
	}
	err := frozen.Range(ctx, func(item Item) error {
		value := applyItem(item)
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		comma := 0
		if len(chunk.Items) > 0 {
			comma = 1
		}
		if len(chunk.Items) == MaxChunkItems || chunkBytes+len(raw)+comma > MaxChunkBytes {
			if err := flush(); err != nil {
				return err
			}
			comma = 0
			if chunkBytes+len(raw) > MaxChunkBytes {
				return fmt.Errorf("single encoded upload item exceeds request limit")
			}
		}
		chunk.Items = append(chunk.Items, value)
		chunkBytes += len(raw) + comma
		return nil
	})
	if err != nil {
		return result, err
	}
	if err = flush(); err != nil {
		return result, err
	}
	return validateAndActivate(ctx, operations, frozen, result)
}

func validateAndActivate(ctx context.Context, operations Operations, frozen *Frozen, result Result) (Result, error) {
	receipt, err := operations.Validate(ctx, result.OperationID)
	if err != nil {
		return result, err
	}
	if receipt == nil || !matchingOperation(receipt.Data, result.OperationID, frozen) {
		return result, fmt.Errorf("validation admission identity mismatch")
	}
	result.Operation = receipt.Data
	switch receipt.Data.State {
	case "validating", "validated", "rejected":
	default:
		return result, fmt.Errorf("validation was not admitted")
	}
	return awaitValidationAndActivate(ctx, operations, frozen, result)
}

func awaitValidationAndActivate(ctx context.Context, operations Operations, frozen *Frozen, result Result) (Result, error) {
	page, err := operations.WaitValidation(ctx, result.OperationID)
	if err != nil {
		return result, err
	}
	if page == nil || page.Data.OperationID != result.OperationID || page.Data.IdentityFormat != commitment.Format || page.Data.ContentDigest != frozen.Digest() || page.Data.ItemCount != int64(frozen.Len()) {
		return result, fmt.Errorf("validation content identity mismatch")
	}
	if err = api.ValidateValidationResultPage(page.Data); err != nil {
		return result, err
	}
	if len(page.Data.Items) > 0 && page.Data.Items[0].Ordinal != 1 {
		return result, fmt.Errorf("validation first page is incomplete")
	}
	summary := page.Data.Summary
	result.Validation = &summary
	if !summary.Valid {
		return result, ErrPreflightRejected
	}
	for _, row := range page.Data.Items {
		if row.Issue != "" || row.Change != "create" && row.Change != "update" && row.Change != "unchanged" {
			return result, fmt.Errorf("validation contains unsupported success observations")
		}
		original, readErr := frozen.Item(ctx, int(row.Ordinal-1))
		if readErr != nil {
			return result, readErr
		}
		if row.Kind != original.Resource.Kind || row.ID != original.Resource.Metadata.ID || row.Source != original.Position.Source.Token || row.SourceDocument != int64(original.Position.Source.Document) || row.SourceItem != int64(original.Position.Source.Item) {
			return result, fmt.Errorf("validation result item identity mismatch")
		}
	}
	// The first page's sealed successful summary covers the complete original
	// collection under the recognized protocol. Only returned rows are checked
	// here; remaining rows are not fetched or asserted individually inspected.
	if err := ctx.Err(); err != nil {
		return result, err
	}
	response, err := operations.Activate(ctx, result.OperationID)
	if err == nil && response != nil && matchingOperation(response.Data, result.OperationID, frozen) && response.Data.State != "applying" && !executionAdmitted(response.Data) {
		err = &cpra.AmbiguousError{Cause: errors.New("activation admission was not confirmed"), OperationID: result.OperationID}
	}
	if errors.Is(err, cpra.ErrAmbiguous) && ctx.Err() == nil {
		// Reconcile only the original content-bound handle. No activation or
		// earlier mutation is repeated after an uncertain response.
		observed, readErr := operations.Get(ctx, result.OperationID)
		if readErr == nil && observed != nil && matchingOperation(observed.Data, result.OperationID, frozen) {
			result.Operation = observed.Data
			if observed.Data.State == "applying" || executionAdmitted(observed.Data) {
				return result, nil
			}
		}
		return result, err
	}
	if response != nil && err == nil {
		if !matchingOperation(response.Data, result.OperationID, frozen) {
			return result, &cpra.AmbiguousError{Cause: errors.New("activation response identity mismatch"), OperationID: result.OperationID}
		}
		result.Operation = response.Data
	}
	if response == nil && err == nil {
		return result, &cpra.AmbiguousError{Cause: errors.New("empty activation response"), OperationID: result.OperationID}
	}
	return result, err
}

func executionAdmitted(operation api.Operation) bool {
	if operation.ExecutionResult == nil {
		return false
	}
	var err error
	if len(operation.Items) == 0 && operation.NextCursor == "" {
		err = api.ValidateExecutionResultMetadata(operation)
	} else {
		err = api.ValidateExecutionResult(operation)
	}
	if err != nil {
		return false
	}
	switch operation.ExecutionResult.State {
	case "pending", "ready", "expired":
		return true
	}
	return false
}

func matchingCount(count *int64, frozen *Frozen) bool {
	return count != nil && *count == int64(frozen.Len())
}

func matchingOperation(operation api.Operation, id string, frozen *Frozen) bool {
	return operation.ID == id && operation.IdentityFormat == commitment.Format && operation.NormalizationProfile == frozen.NormalizationProfile() && operation.ContentDigest == frozen.Digest() && matchingCount(operation.ItemCount, frozen) &&
		(operation.Uploaded == nil || *operation.Uploaded >= 0 && *operation.Uploaded <= int64(frozen.Len()))
}

func uploadable(state string) bool {
	return state == "staging" || state == "uploading" || state == "pending"
}

func applyItem(item Item) api.ApplyItem {
	return api.ApplyItem{ID: item.ID, ContentDigest: item.ContentDigest, Source: item.Position.Source.Token, Ordinal: int64(item.Position.Ordinal), SourceDocument: int64(item.Position.Source.Document), SourceItem: int64(item.Position.Source.Item), Resource: item.Resource}
}
