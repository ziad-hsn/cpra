package persistence

import (
	"errors"
	"time"
)

func collectionValidationInputMatches(v CollectionValidationItem, i CollectionItem) bool {
	return v.Ordinal == i.Ordinal && v.Key == i.Key && v.Source == i.Source && v.Document == i.SourceDocument && v.Item == i.SourceItem
}

func collectionValidationPlanItem(row CollectionPlanRow) CollectionValidationItem {
	return CollectionValidationItem{Ordinal: row.InputOrdinal, Key: row.Key, Source: row.Source, Document: row.Document,
		Item: row.Item, Change: row.Change, UID: row.Target.OriginalUID, ResourceVersion: row.Target.OriginalRevision}
}

func (f *machine) discardCollectionValidationPlan(id string) { delete(f.validationPlanItems, id) }

// A derived immutable index prevents a scan of the complete plan for every
// appended result. It is rebuilt from committed fragments after restore, never
// from current catalog resources or newly generated guards.
func (f *machine) validationPlanFor(s CollectionState) (map[uint64]CollectionValidationItem, error) {
	if entries := f.validationPlanItems[s.ID]; entries != nil {
		return entries, nil
	}
	if s.Plan == nil || s.Plan.FinalizedAt.IsZero() || s.Plan.RemovedFragments != 0 {
		return nil, ErrCollectionInvalid
	}
	entries := make(map[uint64]CollectionValidationItem)
	for after := uint64(0); after < s.Plan.UploadedFragments; {
		parts, err := f.collections.PlanPage(s.ID, after, 256)
		if err != nil || len(parts) == 0 {
			return nil, ErrCollectionUnavailable
		}
		for _, part := range parts {
			if part.Ordinal != after+1 || part.Ordinal > s.Plan.UploadedFragments {
				return nil, ErrCollectionInvalid
			}
			if row := part.Fragment.Row; row != nil {
				if _, exists := entries[row.InputOrdinal]; exists || row.InputOrdinal == 0 || row.InputOrdinal > s.ItemCount || len(entries) >= CollectionValidationMaxItems {
					return nil, ErrCollectionInvalid
				}
				entries[row.InputOrdinal] = collectionValidationPlanItem(*row)
			}
			after = part.Ordinal
		}
	}
	if uint64(len(entries)) != s.ItemCount {
		return nil, ErrCollectionInvalid
	}
	if f.validationPlanItems == nil {
		f.validationPlanItems = make(map[string]map[uint64]CollectionValidationItem)
	}
	f.validationPlanItems[s.ID] = entries
	return entries, nil
}

func (f *machine) applyCollectionValidation(c CollectionCommand, at time.Time) Result {
	epoch, seq, _ := ParseOperationHandle(c.OperationID)
	if epoch != f.image.OperationEpoch {
		return Result{Err: ErrOperationExpired}
	}
	original, exists := f.image.Collections[c.OperationID]
	if !exists {
		if seq <= f.image.OperationHighWater {
			return Result{Err: ErrOperationExpired}
		}
		return Result{Err: ErrOperationNotFound}
	}
	s := original.Clone()
	if err := f.checkCollectionValidationRequestFence(s, c, at); err != nil {
		return Result{Err: err}
	}
	if s.UploadID != c.UploadID || !collectionInactive(s.Phase) || at.Before(s.ActivityAt) {
		return Result{Err: ErrCollectionConflict}
	}
	if !at.Before(s.ExpiresAt) {
		return Result{Err: ErrOperationExpired}
	}
	if f.collections == nil {
		return f.collectionStorageFailure(ErrCollectionUnavailable)
	}
	if c.Action == "validation_begin" {
		want := c.ValidationBegin
		if request := s.ValidationRequest; request != nil && (want.Header.Authority != request.Authority || want.Header.CapabilitiesDigest != request.CapabilitiesDigest) {
			return Result{Err: ErrCollectionConflict}
		}
		if want.Header.Authority.Actor != s.Actor || s.Owner == nil || s.Owner.Epoch != want.Header.Authority.Epoch {
			return Result{Err: ErrOperatorAuthorityDenied}
		}
		if err := f.checkOperatorAuthority(want.Header.Authority, at); err != nil {
			return Result{Err: err}
		}
		if s.Validation != nil {
			if s.Validation.Header != want.Header || s.Validation.Descriptor != want.Descriptor {
				return Result{Err: ErrCollectionConflict}
			}
			return collectionResult(s)
		}
		if s.Uploaded != s.ItemCount || want.Header.InputProgressDigest != s.ProgressDigest || want.Header.ItemCount != s.ItemCount ||
			want.Header.Valid && s.Phase != "validated" || !want.Header.Valid && ((s.Phase != "uploading" && !(s.Phase == "validating" && s.ValidationRequest != nil)) || s.Plan != nil) {
			return Result{Err: ErrCollectionConflict}
		}
		if want.Header.Valid {
			if s.Plan == nil || s.Plan.FinalizedAt.IsZero() || s.Plan.Header.PlanID != want.Header.PlanID || s.Plan.Descriptor.Digest != want.Header.PlanDigest {
				return Result{Err: ErrCollectionConflict}
			}
			if _, err := f.validationPlanFor(s); err != nil {
				return f.collectionStorageFailure(err)
			}
		} else {
			s.Phase = "validating"
		}
		s.Validation = &CollectionValidationState{Header: want.Header, Descriptor: want.Descriptor, ProgressDigest: CollectionValidationInitialDigest(), BegunAt: at}
		s.ActivityAt, s.ExpiresAt = at, at.Add(CollectionInactivityLifetime)
		if s.validate() != nil {
			return Result{Err: ErrCollectionConflict}
		}
		count, used, err := f.collections.ValidationStats(s.ID)
		if err != nil || count != 0 || used != 0 {
			return f.collectionStorageFailure(ErrCollectionUnavailable)
		}
		f.image.Collections[s.ID] = s
		f.image.Version = max(f.image.Version, CollectionValidationFormatVersion)
		return collectionResult(s)
	}
	v := s.Validation
	if v == nil || v.Header.ResultID != c.ValidationID {
		return Result{Err: ErrCollectionConflict}
	}
	if err := f.checkOperatorAuthority(v.Header.Authority, at); err != nil {
		return Result{Err: err}
	}
	if c.Action == "validation_finalize" {
		if v.Uploaded != v.Descriptor.Count || v.ResultBytes != v.Descriptor.Bytes || v.ProgressDigest != v.Descriptor.Digest {
			return Result{Err: ErrCollectionConflict}
		}
		if !v.FinalizedAt.IsZero() {
			return collectionResult(s)
		}
		v.FinalizedAt = at
		s.ActivityAt, s.ExpiresAt = at, at.Add(CollectionInactivityLifetime)
		if !v.Header.Valid {
			s.Phase = "rejected"
		}
		if s.validate() != nil {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		f.image.Collections[s.ID] = s
		f.discardCollectionValidationPlan(s.ID)
		return collectionResult(s)
	}
	if c.Action != "validation_append" || !v.FinalizedAt.IsZero() {
		return Result{Err: ErrCollectionConflict}
	}
	var expected map[uint64]CollectionValidationItem
	if v.Header.Valid {
		var err error
		expected, err = f.validationPlanFor(s)
		if err != nil {
			return f.collectionStorageFailure(err)
		}
	}
	pending := make([]CollectionValidationItem, 0, len(c.ValidationItems))
	for _, item := range c.ValidationItems {
		if item.Ordinal == 0 || item.Ordinal > v.Descriptor.Count || item.Ordinal > v.Uploaded+1 {
			return Result{Err: ErrCollectionConflict}
		}
		if item.Ordinal <= v.Uploaded {
			prior, err := f.collections.ValidationPage(s.ID, item.Ordinal-1, 1)
			if err != nil || len(prior) != 1 || prior[0].Ordinal != item.Ordinal {
				return f.collectionStorageFailure(ErrCollectionUnavailable)
			}
			if prior[0] != item {
				return Result{Err: ErrCollectionConflict}
			}
			continue
		}
		input, exists, err := f.collections.Item(s.ID, item.Ordinal)
		if err != nil || !exists {
			return f.collectionStorageFailure(ErrCollectionUnavailable)
		}
		if !collectionValidationInputMatches(item, input) || v.Header.Valid && expected[item.Ordinal] != item {
			return Result{Err: ErrCollectionConflict}
		}
		digest, cost, err := CollectionValidationNextDigest(v.ProgressDigest, item)
		if err != nil || cost > v.Descriptor.Bytes-v.ResultBytes {
			return Result{Err: ErrCollectionConflict}
		}
		encoded, err := collectionValidationLedgerCost(s.ID, item)
		if err != nil || encoded > maxCollectionLedgerBytes-v.EncodedBytes {
			return Result{Err: ErrCollectionQuota}
		}
		v.Uploaded++
		v.ResultBytes += cost
		v.EncodedBytes += encoded
		v.ProgressDigest = digest
		pending = append(pending, item)
	}
	if s.validate() != nil {
		return Result{Err: ErrCollectionConflict}
	}
	if len(pending) == 0 {
		return collectionResult(s)
	} // Exact replay cannot extend retention.
	if err := f.collections.AppendValidationItems(s.ID, pending); err != nil {
		if errors.Is(err, errCollectionLedgerQuota) {
			return Result{Err: ErrCollectionQuota}
		}
		return f.collectionStorageFailure(err)
	}
	s.ActivityAt, s.ExpiresAt = at, at.Add(CollectionInactivityLifetime)
	f.image.Collections[s.ID] = s
	return collectionResult(s)
}
