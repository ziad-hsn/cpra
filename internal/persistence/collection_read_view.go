package persistence

import (
	"bytes"
	"context"
	"encoding/binary"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// CollectionValidationView identifies one completely uploaded, inactive input.
// It holds neither a read transaction nor a lock between calls. Its identity is
// immutable; each call checks the current committed header before returning any
// ciphertext. This is an internal protected-data interface, not authorization.
// Callers must authorize identities before lookup or decryption and must never
// serialize the protected header or payload into an HTTP observation.
type CollectionValidationView struct {
	store        *Store
	header       CollectionState
	allowPartial bool // Set only by the protected upload-view constructor.
	attempt      *CollectionValidationRequestFence
}

// CollectionValidationView returns a protected header and a view of its exact
// completed inventory. The header's encrypted Secret is detached from the view
// and Store. Reads do not renew the upload or create operations. Supply a fresh
// observed time to every call, then Check again after decryption/graph work.
// Activation still needs its own atomic identity/version guards.
func (s *Store) CollectionValidationView(ctx context.Context, id string, at time.Time) (*CollectionValidationView, CollectionState, error) {
	return s.collectionReadView(ctx, id, at, false)
}

func (s *Store) collectionReadView(ctx context.Context, id string, at time.Time, allowPartial bool) (*CollectionValidationView, CollectionState, error) {
	return s.collectionReadViewForAttempt(ctx, id, at, allowPartial, nil)
}

// CollectionValidationAttemptView reads complete input only for the original
// claimed validation request. Every protected read checks that claim and current
// committed authority. Starting either artifact ends compilation access.
func (s *Store) CollectionValidationAttemptView(ctx context.Context, id string, fence CollectionValidationRequestFence, at time.Time) (*CollectionValidationView, CollectionState, error) {
	if fence.validate() != nil || fence.ClaimID == "" || fence.RunID == "" {
		return nil, CollectionState{}, ErrCollectionInvalid
	}
	return s.collectionReadViewForAttempt(ctx, id, at, false, &fence)
}

func (s *Store) collectionReadViewForAttempt(ctx context.Context, id string, at time.Time, allowPartial bool, attempt *CollectionValidationRequestFence) (*CollectionValidationView, CollectionState, error) {
	v := &CollectionValidationView{store: s, allowPartial: allowPartial, attempt: attempt}
	if _, _, err := ParseOperationHandle(id); err != nil {
		return nil, CollectionState{}, err
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return nil, CollectionState{}, err
	}
	header, err := v.current(id, at)
	if err == nil {
		v.header = header.Clone()
	}
	s.fsm.mu.RUnlock()
	if err != nil {
		return nil, CollectionState{}, err
	}
	if err := v.Check(ctx, at); err != nil {
		return nil, CollectionState{}, err
	}
	return v, v.header.Clone(), nil
}

// Check rechecks the current identity, phase, expiry and storage availability
// without reading ciphertext. It does not reserve a future activation.
func (v *CollectionValidationView) Check(ctx context.Context, at time.Time) error {
	return v.read(ctx, at, nil)
}

// Page returns at most 256 items and 4 MiB of encoded rows. A byte-limited page
// may be shorter than limit: continue after its last ordinal, until Uploaded.
// An expected missing row is unavailable, never an apparent end of input.
func (v *CollectionValidationView) Page(ctx context.Context, after uint64, limit int, at time.Time) ([]CollectionItem, error) {
	if limit < 1 || limit > collectionLedgerBatchLimit || after > v.header.Uploaded {
		return nil, ErrCollectionInvalid
	}
	var frames [][]byte
	err := v.read(ctx, at, func(ledger *collectionLedgerView) error {
		frames = make([][]byte, 0, min(uint64(limit), v.header.Uploaded-after))
		var size int
		for ordinal := after + 1; ordinal <= v.header.Uploaded && len(frames) < limit; ordinal++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := ledger.encodedItem(v.header.ID, ordinal)
			if err != nil || len(data) == 0 {
				return ErrCollectionUnavailable
			}
			if len(data) > collectionLedgerPageBytes-size {
				if len(frames) == 0 {
					// An expected first row must fit by itself. Returning an
					// empty successful page would hide corrupt durable input.
					return ErrCollectionUnavailable
				}
				break
			}
			frames = append(frames, bytes.Clone(data))
			size += len(data)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	items := make([]CollectionItem, 0, len(frames))
	for i, frame := range frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := decodeCollectionLedgerRow(frame)
		if err != nil || row.OperationID != v.header.ID || row.Item.Ordinal != after+uint64(i)+1 {
			return nil, ErrCollectionUnavailable
		}
		items = append(items, row.Item)
	}
	// Keys are known only after decoding outside the owner lock. Recheck the
	// immutable header and corresponding index entries in one short second read.
	if err := v.read(ctx, at, func(ledger *collectionLedgerView) error {
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			ordinal, ok, err := collectionReadIndex(ledger, v.header.ID, item.Key)
			if err != nil || !ok || ordinal != item.Ordinal {
				return ErrCollectionUnavailable
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return items, nil
}

// Item returns exactly one ordinal from the captured prefix. Out-of-range
// ordinals are invalid; a missing in-range ordinal is storage unavailability.
func (v *CollectionValidationView) Item(ctx context.Context, ordinal uint64, at time.Time) (CollectionItem, error) {
	if ordinal == 0 || ordinal > v.header.Uploaded {
		return CollectionItem{}, ErrCollectionInvalid
	}
	items, err := v.Page(ctx, ordinal-1, 1, at)
	if err != nil {
		return CollectionItem{}, err
	}
	if len(items) != 1 {
		return CollectionItem{}, ErrCollectionUnavailable
	}
	return items[0], nil
}

// Find looks up a resource through the ledger's unique identity index. Absence
// is ordinary; a dangling index or a mismatched row is storage unavailability.
func (v *CollectionValidationView) Find(ctx context.Context, key CatalogKey, at time.Time) (CollectionItem, bool, error) {
	if key.validate() != nil {
		return CollectionItem{}, false, ErrCollectionInvalid
	}
	var data []byte
	var ordinal uint64
	var found bool
	err := v.read(ctx, at, func(ledger *collectionLedgerView) error {
		// read has already verified a genuinely empty prefix. An untouched
		// collection has no per-operation bbolt buckets; absence is ordinary.
		if v.header.Uploaded == 0 {
			return nil
		}
		var err error
		ordinal, found, err = collectionReadIndex(ledger, v.header.ID, key)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if ordinal > v.header.Uploaded {
			return ErrCollectionUnavailable
		}
		frame, err := ledger.encodedItem(v.header.ID, ordinal)
		if err != nil || len(frame) == 0 || len(frame) > collectionLedgerPageBytes {
			return ErrCollectionUnavailable
		}
		data = bytes.Clone(frame)
		return nil
	})
	if err != nil || !found {
		return CollectionItem{}, false, err
	}
	row, err := decodeCollectionLedgerRow(data)
	if err != nil || row.OperationID != v.header.ID || row.Item.Ordinal != ordinal || row.Item.Key != key {
		return CollectionItem{}, false, ErrCollectionUnavailable
	}
	if err := v.Check(ctx, at); err != nil {
		return CollectionItem{}, false, err
	}
	return row.Item, true, nil
}

// current requires the FSM read lock. Activity/expiry may advance on an exact
// original upload retry; none of the immutable content identities may change.
func (v *CollectionValidationView) current(id string, at time.Time) (CollectionState, error) {
	if at.IsZero() {
		return CollectionState{}, ErrCollectionInvalid
	}
	s := v.store
	select {
	case <-s.stop:
		return CollectionState{}, ErrCollectionUnavailable
	default:
	}
	f := s.fsm
	if f.err != nil || f.bootstrapPending() || f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		return CollectionState{}, ErrCollectionUnavailable
	}
	epoch, sequence, err := ParseOperationHandle(id)
	if err != nil {
		return CollectionState{}, err
	}
	if epoch != f.image.OperationEpoch {
		return CollectionState{}, ErrOperationExpired
	}
	header, exists := f.image.Collections[id]
	if !exists {
		if sequence <= f.image.OperationHighWater {
			return CollectionState{}, ErrOperationExpired
		}
		return CollectionState{}, ErrOperationNotFound
	}
	if !at.Before(header.ExpiresAt) {
		return CollectionState{}, ErrOperationExpired
	}
	if v.attempt == nil {
		if header.Phase != "uploading" || header.ValidationRequest != nil {
			return CollectionState{}, ErrOperationExpired
		}
	} else {
		if header.Phase != "validating" || header.Plan != nil || header.Validation != nil ||
			!collectionValidationRequestFenceEqual(v.attempt, CollectionValidationRequestFenceFor(header)) {
			return CollectionState{}, ErrCollectionConflict
		}
		if at.Before(header.ActivityAt) {
			return CollectionState{}, ErrCollectionConflict
		}
		if err := f.checkOperatorAuthority(header.ValidationRequest.Authority, at); err != nil {
			return CollectionState{}, err
		}
	}
	if !v.allowPartial && header.Uploaded != header.ItemCount {
		return CollectionState{}, ErrCollectionConflict
	}
	if header.ID != id || header.RemovedRows != 0 || header.RemovedBytes != 0 || header.validate() != nil || f.collections == nil {
		return CollectionState{}, ErrCollectionUnavailable
	}
	original := v.header
	if original.ID != "" && (header.ID != original.ID || header.UploadID != original.UploadID ||
		header.ContentDigest != original.ContentDigest || header.IdentityFormat != original.IdentityFormat || header.NormalizationProfile != original.NormalizationProfile ||
		header.ItemCount != original.ItemCount || header.Uploaded != original.Uploaded ||
		header.ProgressDigest != original.ProgressDigest || header.EncodedBytes != original.EncodedBytes) {
		return CollectionState{}, ErrCollectionConflict
	}
	return header, nil
}

// read keeps FSM -> ledger lock order. All callbacks only inspect indexes and
// copy bounded raw frames: JSON decoding and KMS/provider work belong outside.
// Lock acquisition respects cancellation; an in-flight filesystem read itself
// is cooperative and cannot be forcibly interrupted by a Go context.
func (v *CollectionValidationView) read(ctx context.Context, at time.Time, visit func(*collectionLedgerView) error) error {
	if err := collectionReadLock(ctx, &v.store.fsm.mu); err != nil {
		return err
	}
	defer v.store.fsm.mu.RUnlock()
	header, err := v.current(v.header.ID, at)
	if err != nil {
		return err
	}
	l := v.store.fsm.collections
	if err := collectionReadLock(ctx, &l.mu); err != nil {
		return err
	}
	defer l.mu.RUnlock()
	if l.closed {
		return ErrCollectionUnavailable
	}
	read := func(ledger *collectionLedgerView) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := collectionReadTotals(ledger, header); err != nil {
			return err
		}
		if visit != nil {
			if err := visit(ledger); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	if l.db == nil {
		return read(&collectionLedgerView{rows: l.rows, keys: l.keys, operationBytes: l.operationBytes})
	}
	var readErr error
	if err := l.db.View(func(tx *bolt.Tx) error {
		readErr = read(&collectionLedgerView{tx: tx})
		return readErr
	}); err != nil {
		if readErr != nil {
			return readErr
		}
		return ErrCollectionUnavailable
	}
	return nil
}

func collectionReadTotals(v *collectionLedgerView, header CollectionState) error {
	if v.tx == nil {
		if uint64(len(v.rows[header.ID])) != header.Uploaded || uint64(len(v.keys[header.ID])) != header.Uploaded || v.operationBytes[header.ID] != header.EncodedBytes {
			return ErrCollectionUnavailable
		}
		return nil
	}
	records, keys := v.tx.Bucket(collectionLedgerRecords), v.tx.Bucket(collectionLedgerKeys)
	if records == nil || keys == nil {
		return ErrCollectionUnavailable
	}
	rows, index := records.Bucket([]byte(header.ID)), keys.Bucket([]byte(header.ID))
	if header.Uploaded == 0 && rows == nil && index == nil {
		return nil
	}
	if rows == nil || index == nil || rows.Sequence() != header.Uploaded || index.Sequence() != uint64(header.EncodedBytes) {
		return ErrCollectionUnavailable
	}
	if header.Uploaded == 0 {
		row, _ := rows.Cursor().First()
		key, _ := index.Cursor().First()
		if row != nil || key != nil {
			return ErrCollectionUnavailable
		}
	}
	return nil
}

// collectionReadIndex deliberately does not decode its resource row. Both
// returned ordinal and later copied row must agree before Find/Page can return.
func collectionReadIndex(v *collectionLedgerView, operation string, key CatalogKey) (uint64, bool, error) {
	if v.tx == nil {
		ordinal, exists := v.keys[operation][key]
		if exists && ordinal == 0 {
			return 0, false, ErrCollectionUnavailable
		}
		return ordinal, exists, nil
	}
	root := v.tx.Bucket(collectionLedgerKeys)
	if root == nil || root.Bucket([]byte(operation)) == nil {
		return 0, false, ErrCollectionUnavailable
	}
	data := root.Bucket([]byte(operation)).Get([]byte(key.indexKey()))
	if data == nil {
		return 0, false, nil
	}
	if len(data) != 8 || binary.BigEndian.Uint64(data) == 0 {
		return 0, false, ErrCollectionUnavailable
	}
	return binary.BigEndian.Uint64(data), true, nil
}

func collectionReadLock(ctx context.Context, mu *sync.RWMutex) error {
	if ctx == nil {
		return ErrCollectionInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if mu.TryRLock() {
			if err := ctx.Err(); err != nil {
				mu.RUnlock()
				return err
			}
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
