package collection

import (
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
	"strings"
	"time"
)

func reselectionHandle(s string, limit int) bool {
	if s == "" || s == "." || s == ".." || len(s) > limit {
		return false
	}
	for _, c := range []byte(s) {
		if c <= 32 || c >= 127 || strings.ContainsRune("/\\?#%", rune(c)) {
			return false
		}
	}
	return true
}
func reselectionUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for n, c := range []byte(s) {
		if n == 8 || n == 13 || n == 18 || n == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func reselectionDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Metadata is allowlisted and pointers detached; item/provider messages are omitted.
func reselectionOperation(r *cpra.Response[api.Operation], id string) (api.Operation, error) {
	if r == nil || r.Data.ID != id || r.OperationID != "" && r.OperationID != id {
		return api.Operation{}, ErrReselectionObservation
	}
	o := r.Data
	if o.IdentityFormat != commitment.Format || o.NormalizationProfile != FileNormalizationProfile {
		return api.Operation{}, ErrUnsupportedNormalization
	}
	if !reselectionDigest(o.ContentDigest) || o.ItemCount == nil || *o.ItemCount < 1 || *o.ItemCount > 10_000 || o.Uploaded == nil || *o.Uploaded < 0 || *o.Uploaded > *o.ItemCount || o.RetryAfterSeconds < 0 || o.RetryAfterSeconds > 86400 {
		return api.Operation{}, ErrReselectionObservation
	}
	switch o.State {
	case "pending", "staging", "uploading", "validating", "validated", "rejected", "interrupted", "applying", "committed", "completed", "partial", "failed", "canceled", "cancelled", "expired", "invalidated":
	default:
		return api.Operation{}, ErrReselectionObservation
	}
	out := api.Operation{ID: id, IdentityFormat: o.IdentityFormat, NormalizationProfile: o.NormalizationProfile, ContentDigest: o.ContentDigest, State: o.State, ItemCount: api.Pointer(*o.ItemCount), Uploaded: api.Pointer(*o.Uploaded), RetryAfterSeconds: o.RetryAfterSeconds}
	if o.Committed != nil {
		if *o.Committed < 0 || *o.Committed > *o.ItemCount {
			return api.Operation{}, ErrReselectionObservation
		}
		out.Committed = api.Pointer(*o.Committed)
	}
	if o.Applied != nil {
		if *o.Applied < 0 || *o.Applied > *o.ItemCount || o.Committed != nil && *o.Applied > *o.Committed {
			return api.Operation{}, ErrReselectionObservation
		}
		out.Applied = api.Pointer(*o.Applied)
	}
	if o.Validated != nil {
		out.Validated = api.Pointer(*o.Validated)
	}
	return out, nil
}
func reselectionPhase(phase api.CollectionReselectionAttemptPhase) int {
	switch phase {
	case "uploading":
		return 0
	case "verifying":
		return 1
	case "verified":
		return 2
	case "transferring":
		return 3
	case "completed":
		return 4
	case "failed":
		return 5
	}
	return -1
}
func reselectionObserve(result *ReselectionResult, r *cpra.Response[api.CollectionReselectionAttempt], attempt string, prior *api.CollectionReselectionAttempt) error {
	if r == nil || r.OperationID != "" && r.OperationID != result.OperationID {
		return ErrReselectionObservation
	}
	a := r.Data
	if a.OperationID != result.OperationID || !reselectionUUID(a.ID) || attempt != "" && a.ID != attempt || a.NormalizationProfile != FileNormalizationProfile || a.SourceCount < 1 || a.SourceCount > 1000 || a.SourcesCompleted < 0 || a.SourcesCompleted > a.SourceCount || a.RawBytes < 0 || a.RawBytes > reselectionRawBytes || a.NextOffset < 0 || a.NextOffset > a.RawBytes || a.OperationUploaded < *result.Operation.Uploaded || a.OperationUploaded > *result.Operation.ItemCount || a.ExpiresAt.IsZero() || reselectionPhase(a.Phase) < 0 || r.RetryAfter < 0 {
		return ErrReselectionObservation
	}
	if !time.Now().Before(a.ExpiresAt) {
		return cpra.ErrExpired
	}
	complete := a.SourcesCompleted == a.SourceCount
	if complete && (a.NextSource != 0 || a.NextOffset != 0) || !complete && a.NextSource != a.SourcesCompleted+1 || a.Phase != "uploading" && a.Phase != "failed" && !complete {
		return ErrReselectionObservation
	}
	if (a.Phase == "failed") != (a.ErrorCode != nil) {
		return ErrReselectionObservation
	}
	if a.ErrorCode != nil {
		switch *a.ErrorCode {
		case "input_mismatch", "authorization_changed", "operation_changed", "expired", "storage_unavailable", "quota_exceeded", "verification_failed", "transfer_failed":
		default:
			return ErrReselectionObservation
		}
		a.ErrorCode = api.Pointer(*a.ErrorCode)
	}
	if prior != nil && (a.ID != prior.ID || a.SourceCount != prior.SourceCount || !a.ExpiresAt.Equal(prior.ExpiresAt) || a.SourcesCompleted < prior.SourcesCompleted || a.RawBytes < prior.RawBytes || a.OperationUploaded < prior.OperationUploaded || reselectionPhase(a.Phase) < reselectionPhase(prior.Phase) || a.SourcesCompleted == prior.SourcesCompleted && a.NextOffset < prior.NextOffset || prior.Phase == "completed" && a.Phase != "completed") {
		return ErrReselectionObservation
	}
	result.Attempt = &a
	return nil
}
