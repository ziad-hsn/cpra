package cpra

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/internal/transport"
)

// MaxReselectionSourcePartBytes is the raw binary body ceiling for one source
// part. Whole-attempt limits remain authoritative on the server.
const MaxReselectionSourcePartBytes = 1 << 20

const reselectionResponseBytes = 16 << 10

var errReselectionRequest = errors.New("invalid collection reselection request")
var errReselectionResponse = errors.New("invalid collection reselection response")

// ReselectionSourcePart identifies one exact raw-file part. Data includes comments
// and unused source text; send it only to the configured authenticated origin.
// Source is one-based, Offset counts raw bytes, and End closes that source.
// Callers retain ownership of Data and must not modify it during the request.
type ReselectionSourcePart struct {
	Source int64
	Offset int64
	End    bool
	Data   []byte
}

func (ReselectionSourcePart) String() string {
	return "private reselection source part (input omitted)"
}
func (p ReselectionSourcePart) GoString() string           { return p.String() }
func (p ReselectionSourcePart) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(p.String())) }
func (ReselectionSourcePart) MarshalJSON() ([]byte, error) {
	return nil, errors.New("raw reselection source parts cannot be serialized as JSON")
}

func reselectionIDs(id, attempt string, requireAttempt bool) error {
	if len(id) > 128 || validID(id) != nil {
		return errReselectionRequest
	}
	if requireAttempt && !reselectionAttemptID(attempt) {
		return errReselectionRequest
	}
	return nil
}

func reselectionAttemptID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range []byte(id) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// CreateReselection requests a disposable, caller-owned attempt under the
// original operation. It does not upload, verify, renew or activate that operation.
// Discover server support before use; this candidate contract is additive.
func (s *OperationsService) CreateReselection(ctx context.Context, id string, req api.CollectionReselectionCreateRequest) (*Response[api.CollectionReselectionAttempt], error) {
	if err := reselectionIDs(id, "", false); err != nil {
		return nil, err
	}
	if req.SourceCount < 1 || req.SourceCount > 1000 || req.NormalizationProfile != "cpra.file.base.v1" {
		return nil, errReselectionRequest
	}
	raw, err := s.c.generated.CreateCollectionReselection(ctx, id, req)
	r, err := reselectionResponse(s.c, raw, err, id, "", http.StatusCreated, true)
	if err == nil && (r.Data.SourceCount != req.SourceCount || r.Data.NormalizationProfile != req.NormalizationProfile ||
		r.Data.Phase != "uploading" || r.Data.SourcesCompleted != 0 || r.Data.RawBytes != 0 || r.Data.NextSource != 1 || r.Data.NextOffset != 0) {
		return r, &AmbiguousError{Cause: errReselectionResponse, OperationID: id}
	}
	return r, err
}

// GetReselection reads one owned attempt without renewing either expiry. An
// attempt disappears after server restart or explicit discard; the original
// operation must then be reconciled separately. This method never retries writes.
func (s *OperationsService) GetReselection(ctx context.Context, id, attempt string) (*Response[api.CollectionReselectionAttempt], error) {
	if err := reselectionIDs(id, attempt, true); err != nil {
		return nil, err
	}
	raw, err := s.c.generated.GetCollectionReselection(ctx, id, attempt)
	return reselectionResponse(s.c, raw, err, id, attempt, http.StatusOK, false)
}

// UploadReselectionSource stages one bounded binary part, with no filename,
// multipart metadata, JSON wrapper or automatic retry. Only the server can
// confirm an identical retry of the last accepted part after a lost reply.
func (s *OperationsService) UploadReselectionSource(ctx context.Context, id, attempt string, part ReselectionSourcePart) (*Response[api.CollectionReselectionAttempt], error) {
	if err := reselectionIDs(id, attempt, true); err != nil {
		return nil, err
	}
	if part.Source < 1 || part.Source > 1000 || part.Offset < 0 || part.Offset > 64<<20 ||
		len(part.Data) > MaxReselectionSourcePartBytes || int64(len(part.Data)) > (64<<20)-part.Offset || len(part.Data) == 0 && !part.End {
		return nil, errReselectionRequest
	}
	params := &transport.UploadCollectionReselectionSourceParams{Offset: part.Offset, End: part.End}
	raw, err := s.c.generated.UploadCollectionReselectionSourceWithBody(ctx, id, attempt, part.Source, params, "application/octet-stream", bytes.NewReader(part.Data))
	r, err := reselectionResponse(s.c, raw, err, id, attempt, http.StatusOK, true)
	if err == nil {
		through := part.Offset + int64(len(part.Data))
		covered := r.Data.SourcesCompleted >= part.Source || !part.End && r.Data.NextSource == part.Source && r.Data.NextOffset >= through
		if r.Data.SourceCount < part.Source || r.Data.RawBytes < through || !covered {
			return r, &AmbiguousError{Cause: errReselectionResponse, OperationID: id}
		}
	}
	return r, err
}

// VerifyReselection admits asynchronous proof of all original raw sources and
// normalized resources. Poll GetReselection explicitly. A 202 response is not a
// verification verdict and this method never starts remainder upload.
func (s *OperationsService) VerifyReselection(ctx context.Context, id, attempt string) (*Response[api.CollectionReselectionAttempt], error) {
	if err := reselectionIDs(id, attempt, true); err != nil {
		return nil, err
	}
	raw, err := s.c.generated.VerifyCollectionReselection(ctx, id, attempt)
	r, err := reselectionResponse(s.c, raw, err, id, attempt, http.StatusAccepted, true)
	if err == nil && r.Data.Phase == "uploading" {
		return r, &AmbiguousError{Cause: errReselectionResponse, OperationID: id}
	}
	return r, err
}

// ResumeReselection admits asynchronous upload of the verified missing suffix
// to the original operation. It does not validate or activate configuration.
// Cancellation stops this HTTP wait, not an already admitted server operation.
func (s *OperationsService) ResumeReselection(ctx context.Context, id, attempt string) (*Response[api.CollectionReselectionAttempt], error) {
	if err := reselectionIDs(id, attempt, true); err != nil {
		return nil, err
	}
	raw, err := s.c.generated.ResumeCollectionReselection(ctx, id, attempt)
	r, err := reselectionResponse(s.c, raw, err, id, attempt, http.StatusAccepted, true)
	if err == nil && r.Data.Phase != "transferring" && r.Data.Phase != "completed" && r.Data.Phase != "failed" {
		return r, &AmbiguousError{Cause: errReselectionResponse, OperationID: id}
	}
	return r, err
}

// DiscardReselection removes only this owned disposable attempt. It never
// cancels the original operation or removes any already committed input row.
func (s *OperationsService) DiscardReselection(ctx context.Context, id, attempt string) (*Response[struct{}], error) {
	if err := reselectionIDs(id, attempt, true); err != nil {
		return nil, err
	}
	raw, err := s.c.generated.DiscardCollectionReselection(ctx, id, attempt)
	if err != nil || raw == nil || raw.StatusCode != http.StatusNoContent {
		r, resultErr := responseBounded[struct{}](s.c, raw, err, true, reselectionResponseBytes)
		if resultErr == nil {
			resultErr = errReselectionResponse
		}
		return r, reselectionMutationError(resultErr, id)
	}
	defer raw.Body.Close()
	r := &Response[struct{}]{RequestID: raw.Header.Get("X-Request-ID"), OperationID: id, StatusCode: raw.StatusCode}
	if got := raw.Header.Get("X-Operation-ID"); got != "" && got != id {
		return r, &AmbiguousError{Cause: errReselectionResponse, OperationID: id}
	}
	body, err := boundedRead(raw.Body, 1)
	if err != nil || len(body) != 0 {
		return r, &AmbiguousError{Cause: errReselectionResponse, OperationID: id}
	}
	return r, nil
}

func reselectionMutationError(err error, id string) error {
	if errors.Is(err, ErrAmbiguous) || errors.Is(err, errReselectionResponse) {
		return &AmbiguousError{Cause: err, OperationID: id}
	}
	return err
}

func reselectionResponse(c *Client, raw *http.Response, requestErr error, id, attempt string, status int, mutation bool) (*Response[api.CollectionReselectionAttempt], error) {
	r, err := responseBounded[api.CollectionReselectionAttempt](c, raw, requestErr, mutation, reselectionResponseBytes)
	if r != nil {
		if r.OperationID != "" && r.OperationID != id {
			err = errReselectionResponse
		}
		r.OperationID = id
	}
	if err == nil && (r.StatusCode != status || r.Data.OperationID != id || attempt != "" && r.Data.ID != attempt || validateReselectionObservation(r.Data) != nil) {
		err = errReselectionResponse
	}
	if mutation {
		err = reselectionMutationError(err, id)
	}
	return r, err
}

func validateReselectionObservation(a api.CollectionReselectionAttempt) error {
	if !reselectionAttemptID(a.ID) || a.NormalizationProfile != "cpra.file.base.v1" || a.ExpiresAt.IsZero() ||
		a.SourceCount < 1 || a.SourceCount > 1000 || a.SourcesCompleted < 0 || a.SourcesCompleted > a.SourceCount ||
		a.RawBytes < 0 || a.RawBytes > 64<<20 || a.NextOffset < 0 || a.NextOffset > a.RawBytes || a.OperationUploaded < 0 || a.OperationUploaded > 10_000 {
		return errReselectionResponse
	}
	complete := a.SourcesCompleted == a.SourceCount
	if complete && (a.NextSource != 0 || a.NextOffset != 0) || !complete && a.NextSource != a.SourcesCompleted+1 {
		return errReselectionResponse
	}
	switch a.Phase {
	case "uploading", "failed":
	case "verifying", "verified", "transferring", "completed":
		if !complete {
			return errReselectionResponse
		}
	default:
		return errReselectionResponse
	}
	if a.Phase == "failed" && a.ErrorCode == nil || a.Phase != "failed" && a.ErrorCode != nil {
		return errReselectionResponse
	}
	if a.ErrorCode != nil {
		switch *a.ErrorCode {
		case "input_mismatch", "authorization_changed", "operation_changed", "expired", "storage_unavailable", "quota_exceeded", "verification_failed", "transfer_failed":
		default:
			return errReselectionResponse
		}
	}
	return nil
}
