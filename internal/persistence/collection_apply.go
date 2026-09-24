package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"time"
)

func (f *machine) ensureCollectionLedger() error {
	if f.collections != nil {
		return nil
	}
	var err error
	if f.collectionDirectory == "" {
		f.collections, err = newMemoryCollectionLedger(maxCollectionLedgerBytes)
	} else {
		f.collections, err = openCollectionLedger(f.collectionDirectory, maxCollectionLedgerBytes)
	}
	return err
}

func collectionResult(s CollectionState) Result {
	copy := s.Clone()
	return Result{Allowed: true, Collection: &copy, CollectionID: s.ID}
}

func (f *machine) collectionStorageFailure(err error) Result {
	f.err = errors.Join(ErrCollectionUnavailable, err)
	return Result{Err: f.err}
}

func (f *machine) applyCollection(c CollectionCommand, at time.Time, format int) Result {
	if c.Action == "activation_admit" {
		return f.admitCollectionActivation(c, at)
	}
	if collectionValidationRequestAction(c.Action) {
		return f.applyCollectionValidationRequest(c, at)
	}
	if c.Action == "validation_publish" {
		return f.publishCollectionValidation(c, at)
	}
	if collectionValidationAction(c.Action) {
		return f.applyCollectionValidation(c, at)
	}
	if c.Action == "plan_begin" || c.Action == "plan_append" || c.Action == "plan_finalize" {
		return f.applyCollectionPlan(c, at)
	}
	if c.Action == "epoch" {
		return f.collectionEpoch(c.Epoch)
	}
	if c.Action == "cancel" {
		return f.cancelCollection(c, at)
	}
	if c.Action == "cleanup" {
		if collectionExecutionResultStorageFormat(format) && f.image.Collections[c.OperationID].Activation != nil {
			return Result{Err: ErrCollectionConflict}
		}
		return f.cleanupCollection(c, at)
	}
	if c.Action == "create" {
		if result, done := f.admitCollection(*c.Create, at); done {
			return result
		}
		if c.Create.Owner != nil {
			if err := f.checkOperatorAuthority(*c.Create.Owner, at); err != nil {
				return Result{Err: err}
			}
		}
		// A repeated internal command cannot allocate a second identity. An HTTP
		// retry must still reconcile its original frozen operation explicitly.
		for _, existing := range f.image.Collections {
			if existing.UploadID != c.Create.UploadID {
				continue
			}
			original := existing.Clone()
			original.ID, original.Uploaded, original.EncodedBytes = "", 0, 0
			original.ProgressDigest, original.ActivityAt = collectionInitialDigest(), original.CreatedAt
			original.ExpiresAt = original.CreatedAt.Add(CollectionInactivityLifetime)
			want, _ := json.Marshal(c.Create)
			got, _ := json.Marshal(original)
			if !bytes.Equal(want, got) || existing.Phase != "uploading" {
				return Result{Err: ErrCollectionConflict}
			}
			return collectionResult(existing)
		}
		if len(f.image.Collections) >= maxCollectionOperations || f.image.OperationHighWater == math.MaxUint64 {
			return Result{Err: ErrCollectionQuota}
		}
		if err := f.ensureCollectionLedger(); err != nil {
			return f.collectionStorageFailure(err)
		}
		if f.image.OperationEpoch == "" {
			f.image.OperationEpoch = c.Epoch
		}
		f.image.OperationHighWater++
		s := c.Create.Clone()
		s.ID = operationHandle(f.image.OperationEpoch, f.image.OperationHighWater)
		if f.image.Collections == nil {
			f.image.Collections = map[string]CollectionState{}
		}
		f.image.Collections[s.ID] = s
		if s.Admission != nil {
			if f.image.CollectionAdmissions == nil {
				f.image.CollectionAdmissions = make(map[string]CollectionAdmission)
			}
			f.image.CollectionAdmissions[s.UploadID] = collectionAdmissionFor(s)
		}
		f.image.Version = max(f.image.Version, CollectionFormatVersion)
		if s.NormalizationProfile != "" {
			f.image.Version = max(f.image.Version, CollectionReselectionFormatVersion)
		}
		return collectionResult(s)
	}

	epoch, seq, _ := ParseOperationHandle(c.OperationID)
	if epoch != f.image.OperationEpoch {
		return Result{Err: ErrOperationExpired}
	}
	s, ok := f.image.Collections[c.OperationID]
	if !ok {
		if seq <= f.image.OperationHighWater {
			return Result{Err: ErrOperationExpired}
		}
		return Result{Err: ErrOperationNotFound}
	}
	if s.UploadID != c.UploadID || s.Phase != "uploading" || at.Before(s.ActivityAt) {
		return Result{Err: ErrCollectionConflict}
	}
	if !at.Before(s.ExpiresAt) {
		return Result{Err: ErrOperationExpired}
	}
	if f.collections == nil {
		return f.collectionStorageFailure(ErrCollectionUnavailable)
	}
	item := c.Item
	if c.UploadFence != nil {
		if err := f.checkCollectionUploadFence(s, *c.UploadFence, at); err != nil {
			return Result{Err: err}
		}
	}
	if item.Ordinal > s.ItemCount || item.Ordinal > s.Uploaded+1 {
		return Result{Err: ErrCollectionConflict}
	}
	if item.Ordinal <= s.Uploaded {
		previous, exists, err := f.collections.Item(s.ID, item.Ordinal)
		if err != nil || !exists {
			return f.collectionStorageFailure(errors.Join(err, ErrCollectionUnavailable))
		}
		before, _ := json.Marshal(previous)
		after, _ := json.Marshal(item)
		if !bytes.Equal(before, after) {
			return Result{Err: ErrCollectionConflict}
		}
		// Only an exact accepted replay renews inactivity. Rejected attempts
		// cannot keep an abandoned upload alive or replace original ciphertext.
		s.ActivityAt, s.ExpiresAt = at, at.Add(CollectionInactivityLifetime)
		f.image.Collections[s.ID] = s
		return collectionResult(s)
	}
	if _, exists, err := f.collections.Find(s.ID, item.Key); err != nil {
		return f.collectionStorageFailure(err)
	} else if exists {
		return Result{Err: ErrCollectionConflict}
	}
	cost, err := collectionItemCost(s.ID, *item)
	if err != nil || cost > s.MaxEncodedBytes-s.EncodedBytes {
		return Result{Err: ErrCollectionQuota}
	}
	digest, err := collectionNextDigest(s.ProgressDigest, *item)
	if err != nil {
		return Result{Err: err}
	}
	if err := f.collections.Append(s.ID, *item); err != nil {
		if errors.Is(err, errCollectionLedgerQuota) {
			return Result{Err: ErrCollectionQuota}
		}
		// Precondition failures after a matching FSM prefix indicate damaged
		// materialization. No later command may continue against that ledger.
		return f.collectionStorageFailure(err)
	}
	s.Uploaded++
	if c.UploadFence != nil {
		f.image.Version = max(f.image.Version, CollectionReselectionFormatVersion)
	}
	s.EncodedBytes += cost
	s.ProgressDigest, s.ActivityAt, s.ExpiresAt = digest, at, at.Add(CollectionInactivityLifetime)
	f.image.Collections[s.ID] = s
	return collectionResult(s)
}

// CollectionGet returns protected metadata for internal orchestration. HTTP
// projections must omit Secret entirely. It does not activate uploaded input.
func (s *Store) CollectionGet(id string) (CollectionState, bool, error) {
	epoch, seq, err := ParseOperationHandle(id)
	if err != nil {
		return CollectionState{}, false, err
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if s.fsm.err != nil || s.fsm.bootstrapPending() || s.fsm.restorePending() {
		return CollectionState{}, false, ErrCollectionUnavailable
	}
	if epoch != s.fsm.image.OperationEpoch {
		return CollectionState{}, false, ErrOperationExpired
	}
	state, exists := s.fsm.image.Collections[id]
	if !exists && seq <= s.fsm.image.OperationHighWater {
		return CollectionState{}, false, ErrOperationExpired
	}
	return state.Clone(), exists, nil
}

// CollectionPage reads a bounded ciphertext page from the currently committed
// prefix. It never returns an ahead-of-replay materialization from an old run.
func (s *Store) CollectionPage(id string, after uint64, limit int) ([]CollectionItem, error) {
	if limit < 1 || limit > 256 {
		return nil, ErrCollectionInvalid
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	state, exists := s.fsm.image.Collections[id]
	if s.fsm.err != nil || s.fsm.restorePending() || s.fsm.bootstrapPending() {
		return nil, ErrCollectionUnavailable
	}
	epoch, _, err := ParseOperationHandle(id)
	if err != nil || epoch != s.fsm.image.OperationEpoch {
		return nil, ErrOperationExpired
	}
	if !exists || state.Phase != "uploading" || after > state.Uploaded {
		return nil, ErrCollectionConflict
	}
	if state.Uploaded == 0 {
		return []CollectionItem{}, nil
	}
	if s.fsm.collections == nil {
		return nil, ErrCollectionUnavailable
	}
	return s.fsm.collections.Page(id, after, limit)
}
