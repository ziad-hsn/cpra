package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// CollectionExecutionPreparationView owns a bounded detached next-item capture.
// Construction verifies one same-index frozen input/plan/result/execution
// generation, then closes its read transaction before any live-state recheck.
// The single-owner holder carries protected ciphertext, never an execution or
// auth grant, and retains neither a database transaction nor a full plan index.
type CollectionExecutionPreparationView struct {
	store        *Store
	head         CollectionState
	authority    OperatorAuthority
	capabilities string
	ledger       *collectionLedgerView
	catalog      CatalogView
	index        *collectionExecutionIndex
	generation   *collectionLedger
	cachedIndex  bool
	closed       bool
	row          CollectionPlanRow
	rowDigest    string
	input        CollectionItem
	target       CatalogRecord
	targetExists bool
	prepared     *CollectionPreparedItem
}

func (CollectionExecutionPreparationView) String() string {
	return "protected collection execution preparation view (contents omitted)"
}
func (v CollectionExecutionPreparationView) GoString() string { return v.String() }
func (v CollectionExecutionPreparationView) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte(v.String()))
}
func (CollectionExecutionPreparationView) MarshalJSON() ([]byte, error) {
	return nil, errors.New("protected collection preparation view cannot be serialized")
}

func (l *collectionLedger) freezeContext(ctx context.Context) (*collectionLedgerView, error) {
	if err := collectionCoordinatorLock(ctx, &l.mu); err != nil {
		return nil, err
	}
	defer l.mu.Unlock()
	v, err := l.freezeLocked()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		_ = v.Close()
		return nil, err
	}
	return v, nil
}

func (s *Store) CollectionExecutionPreparationView(ctx context.Context, id string, authority OperatorAuthority, capabilities string, at time.Time) (*CollectionExecutionPreparationView, error) {
	if ctx == nil || s == nil || !bootstrapHash(capabilities) || authority.validate() != nil {
		return nil, ErrCollectionInvalid
	}
	if _, _, err := ParseOperationHandle(id); err != nil {
		return nil, err
	}
	v := &CollectionExecutionPreparationView{store: s, authority: authority, capabilities: capabilities}
	var captured image
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return nil, err
	}
	if s.fsm == nil {
		s.mu.RUnlock()
		return nil, ErrCollectionUnavailable
	}
	if err := collectionCoordinatorLock(ctx, &s.fsm.mu); err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	head, err := v.current(id, at)
	if err == nil {
		v.head = head.Clone()
		v.generation = s.fsm.collections
		if s.fsm.collectionExecutionLedger == v.generation && s.fsm.collectionExecutionIndex != nil && s.fsm.collectionExecutionIndex.matches(head) {
			v.index, v.cachedIndex = s.fsm.collectionExecutionIndex, true
			err = collectionPreparationCaches(s.fsm, head)
		}
	}
	if err == nil {
		original := s.fsm.image
		captured = image{Index: original.Index, CatalogMutationSequence: original.CatalogMutationSequence, OperationEpoch: original.OperationEpoch, OperationHighWater: original.OperationHighWater, Collections: make(map[string]CollectionState), Operations: make(map[string]OperationReceipt), OperationReservations: make(map[string]OperationReservation)}
		for id := range original.Collections {
			captured.Collections[id] = CollectionState{ID: id}
		}
		if r, ok := original.Operations[head.ID]; ok {
			captured.Operations[head.ID] = r
		}
		if r, ok := original.OperationReservations[head.ID]; ok {
			captured.OperationReservations[head.ID] = r
		}
		if original.Restore != nil {
			r := *original.Restore
			captured.Restore = &r
		}
		if s.fsm.catalog == nil {
			s.fsm.rebuildCatalogIndexes()
		}
		v.catalog = CatalogView{tree: s.fsm.catalog.Clone(), Index: s.fsm.image.Index, Cursor: CatalogCursor{owner: s.fsm, epoch: s.fsm.catalogEpoch, position: s.fsm.catalogSequence}}
		v.ledger, err = s.fsm.collections.freezeContext(ctx)
	}
	s.fsm.mu.Unlock()
	s.mu.RUnlock()
	if err != nil {
		_ = v.Close()
		return nil, err
	}
	if v.cachedIndex {
		err = v.verifyCachedProgress(ctx)
	} else {
		v.index, err = buildCollectionExecutionIndex(ctx, v.head, v.ledger, defaultCollectionExecutionIndexLimits())
		if err == nil {
			err = verifyCollectionExecutionResults(ctx, v.head, v.index, v.ledger)
		}
		if err == nil {
			err = v.verifyProgress(ctx, captured)
		}
	}
	if err == nil {
		err = v.detachNext(ctx)
	}
	// A concurrent writer may hold f.mu while waiting for a database remap. Never
	// reacquire f.mu until the pinned transaction is closed, including on errors.
	if v.ledger != nil {
		closeErr := v.ledger.Close()
		v.ledger = nil
		if err == nil {
			err = closeErr
		}
	}
	v.index = nil
	v.catalog = CatalogView{}
	if err == nil {
		err = v.Check(ctx, at)
	}
	if err != nil {
		_ = v.Close()
		return nil, err
	}
	return v, nil
}

// current is called under Store and FSM ownership. Unlike staging views, an
// admitted applying operation is not expired by its original upload deadline.
func (v *CollectionExecutionPreparationView) current(id string, at time.Time) (CollectionState, error) {
	s := v.store
	select {
	case <-s.stop:
		return CollectionState{}, ErrCollectionUnavailable
	default:
	}
	if s.fsm == nil || s.fsm.collections == nil {
		return CollectionState{}, ErrCollectionUnavailable
	}
	f := s.fsm
	if v.generation != nil && v.generation != f.collections {
		return CollectionState{}, ErrCollectionUnavailable
	}
	if err := s.collectionCoordinatorHealth(); err != nil {
		return CollectionState{}, err
	}
	if err := f.checkOperatorAuthority(v.authority, at); err != nil {
		return CollectionState{}, err
	}
	epoch, seq, err := ParseOperationHandle(id)
	if err != nil {
		return CollectionState{}, err
	}
	if epoch != f.image.OperationEpoch {
		return CollectionState{}, ErrOperationExpired
	}
	head, ok := f.image.Collections[id]
	if !ok {
		if seq <= f.image.OperationHighWater {
			return CollectionState{}, ErrOperationExpired
		}
		return CollectionState{}, ErrOperationNotFound
	}
	if head.Actor != v.authority.Actor || head.Owner == nil || head.Owner.Epoch != v.authority.Epoch {
		return CollectionState{}, ErrOperationNotFound
	}
	if head.Phase != "applying" || head.Activation == nil || head.Activation.CapabilitiesDigest != v.capabilities || at.Before(head.Activation.At) {
		return CollectionState{}, ErrCollectionConflict
	}
	if head.validate() != nil || !collectionExecutionEligible(head) {
		return CollectionState{}, ErrCollectionUnavailable
	}
	if v.head.ID != "" {
		if !collectionPreparationSameInput(v.head, head) {
			return CollectionState{}, ErrCollectionConflict
		}
		// An unrelated child's terminal observation may advance independently.
		// Such advancement must match the current authoritative caches, as must
		// every live recheck made by a holder captured through the hot path.
		if v.cachedIndex || !reflect.DeepEqual(head, v.head) {
			if err := collectionPreparationCaches(f, head); err != nil {
				return CollectionState{}, err
			}
		}
	}
	return head, nil
}

func (v *CollectionExecutionPreparationView) Header() CollectionState {
	if v == nil || v.closed {
		return CollectionState{}
	}
	return v.head.Clone()
}
func (v *CollectionExecutionPreparationView) Close() error {
	if v == nil || v.closed {
		return nil
	}
	v.closed = true
	var err error
	if v.ledger != nil {
		err = v.ledger.Close()
	}
	v.ledger = nil
	v.index = nil
	v.generation = nil
	v.catalog = CatalogView{}
	clearCollectionPreparationItem(&v.input)
	clearCollectionPreparationRecord(&v.target)
	if v.prepared != nil {
		clearCollectionPreparationRecord(&v.prepared.Record)
	}
	v.prepared = nil
	v.row = CollectionPlanRow{}
	v.rowDigest = ""
	v.head = CollectionState{}
	v.store = nil
	return err
}
func (v *CollectionExecutionPreparationView) Check(ctx context.Context, at time.Time) error {
	if ctx == nil {
		return ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.closed || v.store == nil {
		return ErrCollectionUnavailable
	}
	s := v.store
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return err
	}
	defer s.fsm.mu.RUnlock()
	_, err := v.current(v.head.ID, at)
	if err != nil {
		return err
	}
	return ctx.Err()
}
func (v *CollectionExecutionPreparationView) nextOrdinal() uint64 {
	if v.head.Execution == nil {
		return 1
	}
	return v.head.Execution.Processed + 1
}
func (v *CollectionExecutionPreparationView) Row(ctx context.Context, ordinal uint64, at time.Time) (CollectionPlanRow, string, error) {
	if err := v.Check(ctx, at); err != nil {
		return CollectionPlanRow{}, "", err
	}
	if ordinal != v.nextOrdinal() {
		return CollectionPlanRow{}, "", ErrCollectionConflict
	}
	if v.row.Ordinal != ordinal {
		return CollectionPlanRow{}, "", ErrCollectionInvalid
	}
	return collectionExecutionCloneRow(v.row), v.rowDigest, nil
}
func (v *CollectionExecutionPreparationView) Input(ctx context.Context, ordinal uint64, at time.Time) (CollectionItem, error) {
	if _, _, err := v.Row(ctx, ordinal, at); err != nil {
		return CollectionItem{}, err
	}
	return v.input.Clone(), nil
}

func (v *CollectionExecutionPreparationView) Target(ctx context.Context, ordinal uint64, at time.Time) (CatalogRecord, bool, error) {
	row, _, err := v.Row(ctx, ordinal, at)
	if err != nil {
		return CatalogRecord{}, false, err
	}
	captured, exists := v.target, v.targetExists
	matches := func(r CatalogRecord, ok bool) bool {
		return row.Target.Absent && !ok || !row.Target.Absent && ok && r.UID == row.Target.OriginalUID && r.Revision == row.Target.OriginalRevision && r.Generation == uint64(row.Target.OriginalGeneration)
	}
	if !matches(captured, exists) {
		return CatalogRecord{}, false, ErrCatalogConflict
	}
	s := v.store
	if err = collectionReadLock(ctx, &s.mu); err != nil {
		return CatalogRecord{}, false, err
	}
	defer s.mu.RUnlock()
	if err = collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return CatalogRecord{}, false, err
	}
	defer s.fsm.mu.RUnlock()
	if _, err = v.current(v.head.ID, at); err != nil {
		return CatalogRecord{}, false, err
	}
	current, ok := s.fsm.image.Catalog[row.Key.indexKey()]
	ok = ok && !current.Removed
	if !matches(current, ok) {
		return CatalogRecord{}, false, ErrCatalogConflict
	}
	if err := ctx.Err(); err != nil {
		return CatalogRecord{}, false, err
	}
	return captured.Clone(), exists, nil
}
func (v *CollectionExecutionPreparationView) ExistingPrepared(ctx context.Context, at time.Time) (CollectionPreparedItem, bool, error) {
	if err := v.Check(ctx, at); err != nil {
		return CollectionPreparedItem{}, false, err
	}
	if v.prepared == nil {
		return CollectionPreparedItem{}, false, nil
	}
	return v.prepared.Clone(), true, nil
}

// detachNext runs only against the captured immutable generation, without any
// Store/FSM reacquisition. The entire source/index is validated before capture.
func (v *CollectionExecutionPreparationView) detachNext(ctx context.Context) error {
	ordinal := v.nextOrdinal()
	if ordinal > v.head.ItemCount {
		return ctx.Err()
	}
	row, ok := v.index.row(ordinal)
	if !ok {
		return ErrCollectionUnavailable
	}
	if err := v.index.walkRow(ctx, v.ledger, ordinal, func(CollectionPlanFragment) error { return nil }); err != nil {
		return err
	}
	input, digest, err := collectionExecutionInputItemHash(ctx, v.ledger, v.head.ID, row.Row.InputOrdinal)
	if err != nil {
		return err
	}
	if digest != row.InputFrameDigest || !collectionPlanInputMatches(row.Row, input) {
		clearCollectionPreparationItem(&input)
		return ErrCollectionUnavailable
	}
	v.row, v.rowDigest, v.input = row.Row, row.RowDigest, input
	v.target, v.targetExists = v.catalog.Get(row.Row.Key)
	record, _, err := collectionExecutionRecoveryRecord(ctx, v.ledger, v.head.ID, "prepared")
	if err != nil {
		return err
	}
	expected := v.head.Execution
	if expected == nil || expected.Prepared == nil {
		if record.Prepared != nil {
			return ErrCollectionUnavailable
		}
		return ctx.Err()
	}
	if record.Prepared == nil {
		return ErrCollectionUnavailable
	}
	commitment, err := collectionExecutionPreparedCommitment(*record.Prepared)
	if err != nil || !reflect.DeepEqual(commitment, *expected.Prepared) {
		return ErrCollectionUnavailable
	}
	value := record.Prepared.Clone()
	v.prepared = &value
	return ctx.Err()
}
func clearCollectionPreparationRecord(record *CatalogRecord) {
	clear(record.Payload.Ciphertext)
	clear(record.Payload.WrappedKey)
	clear(record.Payload.Nonce)
	*record = CatalogRecord{}
}
func clearCollectionPreparationItem(item *CollectionItem) {
	clear(item.Payload.Ciphertext)
	clear(item.Payload.WrappedKey)
	clear(item.Payload.Nonce)
	*item = CollectionItem{}
}

func (v *CollectionExecutionPreparationView) verifyProgress(ctx context.Context, captured image) error {
	if v.head.Execution == nil {
		stats, err := v.ledger.ExecutionStats(v.head.ID)
		if err != nil || stats != (collectionExecutionStats{}) {
			return ErrCollectionUnavailable
		}
		return ctx.Err()
	}
	_, err := validateCollectionExecutionParentWithIndex(ctx, captured, v.head, v.ledger, nil, v.index)
	if err != nil {
		return errors.Join(ErrCollectionUnavailable, err)
	}
	return nil
}

// verifyCollectionExecutionResults reuses the verified original row index. It retains only an
// input-order permutation (at most 80 KiB), and one bounded canonical result.
// This is distinct from history retention: activation's original finalized
// result remains in the pinned staging generation even after history expiry.
func verifyCollectionExecutionResults(ctx context.Context, s CollectionState, index *collectionExecutionIndex, view *collectionLedgerView) error {
	if ctx == nil || index == nil || view == nil || s.Validation == nil || s.ItemCount > CollectionValidationMaxItems || uint64(len(index.rows)) != s.ItemCount {
		return ErrCollectionUnavailable
	}
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return err
	}
	count, encoded, err := view.validationStats(s.ID)
	view.mu.Unlock()
	if err != nil || count != s.ItemCount || encoded != s.Validation.EncodedBytes {
		return ErrCollectionUnavailable
	}
	positions := make([]uint64, int(s.ItemCount))
	for n, r := range index.rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		ordinal := r.Row.InputOrdinal
		if ordinal == 0 || ordinal > s.ItemCount || positions[ordinal-1] != 0 {
			return ErrCollectionUnavailable
		}
		positions[ordinal-1] = uint64(n + 1)
	}
	descriptor := CollectionValidationDescriptor{Digest: CollectionValidationInitialDigest()}
	var actualEncoded int64
	for ordinal := uint64(1); ordinal <= s.ItemCount; ordinal++ {
		if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
			return err
		}
		borrowed, err := view.encodedValidationItem(s.ID, ordinal)
		raw := append([]byte(nil), borrowed...)
		view.mu.Unlock()
		if err != nil {
			return ErrCollectionUnavailable
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		decoded, err := decodeCollectionValidationLedgerRow(raw)
		if err != nil || decoded.OperationID != s.ID || decoded.Item.Ordinal != ordinal || positions[ordinal-1] == 0 {
			return ErrCollectionUnavailable
		}
		expected := collectionValidationPlanItem(index.rows[positions[ordinal-1]-1].Row)
		if decoded.Item != expected {
			return ErrCollectionUnavailable
		}
		digest, cost, err := CollectionValidationNextDigest(descriptor.Digest, decoded.Item)
		if err != nil || cost > s.Validation.Descriptor.Bytes-descriptor.Bytes || int64(len(raw)) > encoded-actualEncoded {
			return ErrCollectionUnavailable
		}
		descriptor.Count++
		descriptor.Bytes += cost
		descriptor.Digest = digest
		actualEncoded += int64(len(raw))
	}
	if descriptor != s.Validation.Descriptor || actualEncoded != encoded {
		return ErrCollectionUnavailable
	}
	return ctx.Err()
}
