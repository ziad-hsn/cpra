package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
)

func (f *machine) discardCollectionPlanPrefix(id string) {
	if p := f.planPrefixes[id]; p != nil {
		p.close()
		delete(f.planPrefixes, id)
	}
}

// A prefix cache is a disposable parser index, never execution authority. A
// restored store reconstructs it once from the exact committed ledger prefix.
// Rejected attempts discard it, because parser validation may partially advance
// private indexes before detecting a later invalid field in the same fragment.
func (f *machine) collectionPlanPrefixFor(s CollectionState) (*collectionPlanPrefix, error) {
	if p := f.planPrefixes[s.ID]; p != nil {
		if err := p.matches(s.Plan, false); err == nil {
			return p, nil
		}
		f.discardCollectionPlanPrefix(s.ID)
	}
	p := newCollectionPlanPrefix(s.Plan.Header)
	for after := uint64(0); after < s.Plan.UploadedFragments; {
		page, err := f.collections.PlanPage(s.ID, after, 256)
		if err != nil || len(page) == 0 {
			p.close()
			return nil, ErrCollectionUnavailable
		}
		for _, part := range page {
			if part.Ordinal > s.Plan.UploadedFragments {
				p.close()
				return nil, ErrCollectionInvalid
			}
			if err := f.collectionPlanInput(s, part.Fragment.Row); err != nil {
				p.close()
				return nil, err
			}
			if err := p.add(context.Background(), s.ID, part); err != nil {
				p.close()
				return nil, err
			}
			after = part.Ordinal
		}
	}
	count, used, err := f.collections.PlanStats(s.ID)
	if err != nil || count != s.Plan.UploadedFragments || used != s.Plan.EncodedBytes || p.matches(s.Plan, false) != nil {
		p.close()
		return nil, ErrCollectionUnavailable
	}
	if f.planPrefixes == nil {
		f.planPrefixes = make(map[string]*collectionPlanPrefix)
	}
	f.planPrefixes[s.ID] = p
	return p, nil
}

// An untrusted proposed row outside the original inventory is a conflict, not
// evidence of a broken store. An absent in-range committed row is corruption.
// Cache reconstruction uses the same check, but treats any mismatch in already
// committed fragments as storage failure at its caller.
func (f *machine) collectionPlanInput(s CollectionState, row *CollectionPlanRow) error {
	if row == nil {
		return nil
	}
	if row.InputOrdinal == 0 || row.InputOrdinal > s.ItemCount {
		return ErrCollectionConflict
	}
	items, err := f.collections.Page(s.ID, row.InputOrdinal-1, 1)
	if err != nil || len(items) != 1 || items[0].Ordinal != row.InputOrdinal {
		return ErrCollectionUnavailable
	}
	if !collectionPlanInputMatches(*row, items[0]) {
		return ErrCollectionConflict
	}
	return nil
}

func (f *machine) applyCollectionPlan(c CollectionCommand, at time.Time) Result {
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
	if s.Validation != nil {
		return Result{Err: ErrCollectionConflict} // A captured verdict cannot acquire a different plan.
	}
	if s.UploadID != c.UploadID || !collectionInactive(s.Phase) || at.Before(s.ActivityAt) {
		return Result{Err: ErrCollectionConflict}
	}
	if c.Action == "plan_finalize" && s.Phase == "validated" && s.Plan != nil && *c.PlanFinalize == collectionPlanFence(s) {
		return collectionResult(s) // Original immutable verdict; no renewed deadline.
	}
	if !at.Before(s.ExpiresAt) {
		return Result{Err: ErrOperationExpired}
	}
	if f.collections == nil {
		return f.collectionStorageFailure(ErrCollectionUnavailable)
	}
	switch c.Action {
	case "plan_begin":
		want := c.PlanBegin
		if s.Plan != nil {
			if s.Plan.Header != want.Header || s.Plan.Descriptor != want.Descriptor {
				return Result{Err: ErrCollectionConflict}
			}
			return collectionResult(s)
		}
		if (s.Phase != "uploading" && !(s.Phase == "validating" && s.ValidationRequest != nil)) || s.Uploaded != s.ItemCount || want.Header.ObservedIndex > f.image.Index {
			return Result{Err: ErrCollectionConflict}
		}
		s.Plan = &CollectionPlanState{Header: want.Header, Descriptor: want.Descriptor,
			ArtifactBytes: uint64(len(collectionPlanMagic)), ProgressDigest: collectionPlanInitialDigest(), BegunAt: at}
		s.Phase, s.ActivityAt, s.ExpiresAt = "validating", at, at.Add(CollectionInactivityLifetime)
		if s.validate() != nil {
			return Result{Err: ErrCollectionConflict}
		}
		count, used, err := f.collections.PlanStats(s.ID)
		if err != nil || count != 0 || used != 0 {
			return f.collectionStorageFailure(ErrCollectionUnavailable)
		}
		f.image.Collections[s.ID] = s
		f.image.Version = max(f.image.Version, CollectionPlanFormatVersion)
		return collectionResult(s)
	case "plan_append":
		if s.Phase != "validating" || s.Plan == nil || s.Plan.Header.PlanID != c.PlanID {
			return Result{Err: ErrCollectionConflict}
		}
		part, plan := c.PlanFragment, s.Plan
		if part.Ordinal > plan.Descriptor.Fragments || part.Ordinal > plan.UploadedFragments+1 {
			return Result{Err: ErrCollectionConflict}
		}
		if part.Ordinal <= plan.UploadedFragments {
			page, err := f.collections.PlanPage(s.ID, part.Ordinal-1, 1)
			if err != nil || len(page) != 1 || page[0].Ordinal != part.Ordinal {
				return f.collectionStorageFailure(ErrCollectionUnavailable)
			}
			old, _ := json.Marshal(page[0])
			next, _ := json.Marshal(part)
			if !bytes.Equal(old, next) {
				return Result{Err: ErrCollectionConflict}
			}
			s.ActivityAt, s.ExpiresAt = at, at.Add(CollectionInactivityLifetime)
			f.image.Collections[s.ID] = s
			return collectionResult(s)
		}
		prefix, err := f.collectionPlanPrefixFor(s)
		if err != nil {
			return f.collectionStorageFailure(err)
		}
		if err := f.collectionPlanInput(s, part.Fragment.Row); err != nil {
			if errors.Is(err, ErrCollectionConflict) {
				return Result{Err: err}
			}
			return f.collectionStorageFailure(err)
		}
		if err := prefix.add(context.Background(), s.ID, *part); err != nil {
			f.discardCollectionPlanPrefix(s.ID)
			return Result{Err: err}
		}
		plan.UploadedFragments, plan.ArtifactBytes = prefix.state.fragments, prefix.state.bytes
		plan.EncodedBytes, plan.ProgressDigest = prefix.encoded, prefix.progress
		if err := prefix.matches(plan, false); err != nil || s.validate() != nil {
			f.discardCollectionPlanPrefix(s.ID)
			return Result{Err: ErrCollectionConflict}
		}
		if err := f.collections.AppendPlanFragments(s.ID, []CollectionPlanLedgerFragment{*part}); err != nil {
			f.discardCollectionPlanPrefix(s.ID)
			if errors.Is(err, errCollectionLedgerQuota) {
				return Result{Err: ErrCollectionQuota}
			}
			return f.collectionStorageFailure(err)
		}
		s.ActivityAt, s.ExpiresAt = at, at.Add(CollectionInactivityLifetime)
		f.image.Collections[s.ID] = s
		return collectionResult(s)
	case "plan_finalize":
		if s.Phase != "validating" || s.Plan == nil || *c.PlanFinalize != collectionPlanFence(s) ||
			s.Plan.UploadedFragments != s.Plan.Descriptor.Fragments || s.Plan.ArtifactBytes != s.Plan.Descriptor.Bytes {
			return Result{Err: ErrCollectionConflict}
		}
		// Every committed fragment already passed incremental codec validation,
		// including the full descriptor at its final footer. The manager also
		// verifies a captured immutable stream outside Apply before this fence.
		s.Phase, s.ActivityAt, s.ExpiresAt = "validated", at, at.Add(CollectionInactivityLifetime)
		s.Plan.FinalizedAt = at
		if s.validate() != nil {
			return f.collectionStorageFailure(ErrCollectionInvalid)
		}
		f.image.Collections[s.ID] = s
		f.discardCollectionPlanPrefix(s.ID)
		return collectionResult(s)
	default:
		return Result{Err: ErrCollectionInvalid}
	}
}
