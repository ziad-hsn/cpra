package management

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// collectionValidationArtifact is a frozen, metadata-only observation of one
// original input inventory. Valid never grants execution: successful activation
// additionally requires the companion immutable plan and current authority.
// Preparation and close have one owner. Once prepared, emit reads only frozen
// bytes and never retains the source, private inventory key, or resource bodies.
type collectionValidationArtifact struct {
	header      collectionPlanHeader
	valid       bool
	summaryOnly bool
	issue       string // Global deterministic failure; never attributed to an arbitrary input.
	descriptor  persistence.CollectionValidationDescriptor
	encoded     []byte
	closed      bool
}

func prepareCollectionValidationArtifact(ctx context.Context, source *collectionValidationSource, result CollectionValidation, validationErr error) (*collectionValidationArtifact, error) {
	return prepareCollectionValidationArtifactBounded(ctx, source, result, validationErr, persistence.CollectionValidationResultMaxBytes)
}

func prepareCollectionValidationArtifactBounded(ctx context.Context, source *collectionValidationSource, result CollectionValidation, validationErr error, maxBytes uint64) (_ *collectionValidationArtifact, resultErr error) {
	if ctx == nil || maxBytes == 0 || maxBytes > persistence.CollectionValidationResultMaxBytes || len(result.Items) > maxValidationGraph {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	issue, err := collectionValidationArtifactIssue(result, validationErr)
	if err != nil {
		return nil, err
	}
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	head := source.head
	// Use the context-aware protected read instead of CollectionGet's owner
	// lock so cancellation remains responsive while durable writes are busy.
	current, err := source.currentHeader(ctx)
	if err != nil {
		return nil, err
	}
	if current.ID != head.ID || current.UploadID != head.UploadID || current.Actor != head.Actor ||
		current.IdentityFormat != head.IdentityFormat || current.ContentDigest != head.ContentDigest || current.ItemCount != head.ItemCount ||
		current.Uploaded != head.Uploaded || current.ProgressDigest != head.ProgressDigest || current.EncodedBytes != head.EncodedBytes {
		return nil, persistence.ErrCollectionConflict
	}
	if uint64(len(result.Items)) > head.ItemCount || result.Valid && uint64(len(result.Items)) != head.ItemCount {
		return nil, ErrValidation
	}
	if result.Valid {
		// A success must be the complete compiler result, not an otherwise
		// plausible collection of individually classified resources.
		if len(result.Items) > maxValidationGraph || len(result.Order) != len(result.Items) {
			return nil, ErrValidation
		}
		ordered := make(map[persistence.CatalogKey]bool, len(result.Order))
		for _, key := range result.Order {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if ordered[key] {
				return nil, ErrValidation
			}
			ordered[key] = true
		}
		for _, item := range result.Items {
			if !ordered[item.Key] {
				return nil, ErrValidation
			}
			delete(ordered, item.Key)
		}
	}
	a := &collectionValidationArtifact{header: collectionPlanHeader{OperationID: head.ID, UploadID: head.UploadID, Actor: head.Actor,
		IdentityFormat: head.IdentityFormat, ContentDigest: head.ContentDigest, InputProgressDigest: head.ProgressDigest,
		ItemCount: head.ItemCount, ObservedIndex: result.Index}, valid: result.Valid, issue: issue,
		descriptor: persistence.CollectionValidationDescriptor{Digest: persistence.CollectionValidationInitialDigest()}}
	defer func() {
		if resultErr != nil {
			a.close()
		}
	}()
	if head.ItemCount > maxValidationGraph {
		// A declared inventory can exceed the graph validator's 10k input
		// limit. A full notEvaluated list could itself exceed 32MiB. Preserve
		// this explicit bounded disposition rather than truncate that list.
		if result.Valid || issue != "validationLimit" || !errors.Is(validationErr, ErrGraphLimit) || len(result.Items) != 0 {
			return nil, ErrValidation
		}
		a.summaryOnly = true
		if err := source.check(ctx); err != nil {
			return nil, err
		}
		return a, nil
	}
	for after := uint64(0); after < head.ItemCount; {
		page, err := source.view.Page(ctx, after, 256, source.now())
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return nil, ErrUnavailable
		}
		for _, original := range page {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if original.Ordinal != after+1 || original.Ordinal > head.ItemCount {
				return nil, ErrUnavailable
			}
			if !source.canRead(original.Key) {
				return nil, errCollectionReadDenied
			}
			item := persistence.CollectionValidationItem{Ordinal: original.Ordinal, Key: original.Key, Source: original.Source,
				Document: original.SourceDocument, Item: original.SourceItem, Issue: "notEvaluated"}
			if original.Ordinal <= uint64(len(result.Items)) {
				observed := result.Items[original.Ordinal-1]
				if observed.Key != item.Key || observed.SourceID != item.Source || observed.ItemID != fmt.Sprintf("item.%020d", original.Ordinal) ||
					observed.Change == "" && observed.Issue == "" || observed.Change == "" && (observed.UID != "" || observed.ResourceVersion != "") ||
					(observed.Change == "update" || observed.Change == "unchanged") && (observed.UID == "" || observed.ResourceVersion == "") ||
					result.Valid && (observed.Change == "" || observed.Issue != "") {
					return nil, ErrValidation
				}
				item.Change, item.Issue = observed.Change, observed.Issue
				item.UID, item.ResourceVersion = observed.UID, observed.ResourceVersion
			}
			raw, err := persistence.CollectionValidationItemEncoding(item)
			if err != nil {
				return nil, ErrValidation
			}
			digest, cost, err := persistence.CollectionValidationNextDigest(a.descriptor.Digest, item)
			if err != nil || cost > maxBytes-a.descriptor.Bytes {
				return nil, ErrGraphLimit
			}
			// The durable descriptor bounds encoded bytes. Cap backing-array
			// growth too; no unbounded fleet-wide typed result is accumulated.
			need := len(a.encoded) + int(cost)
			if need > cap(a.encoded) {
				next := min(int(maxBytes), max(need, max(4096, cap(a.encoded)*2)))
				grown := make([]byte, len(a.encoded), next)
				copy(grown, a.encoded)
				clear(a.encoded)
				a.encoded = grown
			}
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
			a.encoded = append(a.encoded, length[:]...)
			a.encoded = append(a.encoded, raw...)
			a.descriptor.Count++
			a.descriptor.Bytes += cost
			a.descriptor.Digest = digest
			after = original.Ordinal
		}
	}
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if a.descriptor.Count != head.ItemCount || a.descriptor.Bytes != uint64(len(a.encoded)) {
		return nil, ErrUnavailable
	}
	return a, nil
}

// Classify only known deterministic compiler outcomes. No string from an error
// is copied or returned. Transient/authority failures take precedence even when
// joined with a deterministic validation sentinel.
func collectionValidationArtifactIssue(result CollectionValidation, err error) (string, error) {
	for _, transient := range []error{context.Canceled, context.DeadlineExceeded, errCollectionReadDenied, ErrUnavailable,
		persistence.ErrCollectionUnavailable, persistence.ErrOperationExpired, persistence.ErrCollectionConflict, persistence.ErrOperationNotFound} {
		if errors.Is(err, transient) {
			return "", transient
		}
	}
	for _, item := range result.Items {
		if item.Issue == "readDenied" {
			return "", errCollectionReadDenied
		}
		if item.Issue == "unavailable" {
			return "", ErrUnavailable
		}
		if !persistence.CollectionValidationIssue(item.Issue) {
			return "", ErrValidation
		}
	}
	if err == nil {
		if !result.Valid {
			return "", ErrValidation
		}
		return "", nil
	}
	if result.Valid {
		return "", ErrValidation
	}
	// A join containing a known validation error and an unknown storage or
	// provider error is still unavailable. Unwrap ordinary diagnostic wrappers,
	// but accept only a bounded tree whose every leaf is a known sentinel.
	remaining := 64
	if !collectionValidationDeterministicError(err, &remaining) {
		return "", ErrUnavailable
	}
	switch {
	case errors.Is(err, ErrGraphLimit):
		return "validationLimit", nil
	case errors.Is(err, persistence.ErrCatalogConflict):
		return "conflict", nil
	case errors.Is(err, persistence.ErrCatalogDependency):
		return "missingReference", nil
	case errors.Is(err, ErrValidation):
		return "invalidGraph", nil
	default:
		return "", ErrUnavailable
	}
}

func collectionValidationDeterministicError(err error, remaining *int) bool {
	if err == nil || *remaining == 0 {
		return false
	}
	*remaining--
	switch err {
	case ErrGraphLimit, persistence.ErrCatalogConflict, persistence.ErrCatalogDependency, ErrValidation:
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		children := wrapped.Unwrap()
		if len(children) == 0 || len(children) > *remaining {
			return false
		}
		for _, child := range children {
			if !collectionValidationDeterministicError(child, remaining) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return collectionValidationDeterministicError(wrapped.Unwrap(), remaining)
	default:
		return false
	}
}

// emit offers detached batches of at most 256 items and 4MiB of canonical item
// JSON. Caller mutation cannot alter an exact retry. A callback error may leave
// an unpublished prefix at the caller; it never changes the frozen artifact.
func (a *collectionValidationArtifact) emit(ctx context.Context, appendBatch func([]persistence.CollectionValidationItem) error) error {
	if a == nil || a.closed || ctx == nil || appendBatch == nil {
		return ErrValidation
	}
	var after int
	for after < len(a.encoded) {
		batch := make([]persistence.CollectionValidationItem, 0, 256)
		var size int
		for after < len(a.encoded) && len(batch) < 256 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(a.encoded)-after < 4 {
				return ErrValidation
			}
			n := int(binary.BigEndian.Uint32(a.encoded[after : after+4]))
			if n <= 0 || n > persistence.CollectionValidationItemMaxBytes || n > len(a.encoded)-after-4 {
				return ErrValidation
			}
			if size+n > 4<<20 {
				break
			}
			var item persistence.CollectionValidationItem
			if json.Unmarshal(a.encoded[after+4:after+4+n], &item) != nil {
				return ErrValidation
			}
			batch = append(batch, item)
			after += 4 + n
			size += n
		}
		if err := appendBatch(batch); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (a *collectionValidationArtifact) close() {
	if a != nil {
		clear(a.encoded)
		a.encoded = nil
		a.header = collectionPlanHeader{}
		a.descriptor = persistence.CollectionValidationDescriptor{}
		a.valid, a.summaryOnly, a.closed, a.issue = false, false, true, ""
	}
}
