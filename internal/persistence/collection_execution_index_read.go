package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

// The frozen view has its own lock, never a Store/FSM lock. Copy bounded frames
// under that lock and decode/hash afterward. Callers must release their view;
// this index does not pin it or retain its transaction beyond construction.
func collectionExecutionViewLock(ctx context.Context, mu *sync.Mutex) error {
	if ctx == nil {
		return ErrCollectionPlanInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if mu.TryLock() {
			if err := ctx.Err(); err != nil {
				mu.Unlock()
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

func collectionExecutionFrame(ctx context.Context, view *collectionLedgerView, op string, ordinal uint64, plan bool) ([]byte, error) {
	if view == nil {
		return nil, ErrCollectionPlanInvalid
	}
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return nil, err
	}
	defer view.mu.Unlock()
	if view.closed {
		return nil, errCollectionLedgerClosed
	}
	var raw []byte
	var err error
	if plan {
		raw, err = view.encodedPlanPart(op, ordinal)
	} else {
		raw, err = view.encodedItem(op, ordinal)
	}
	if err != nil || len(raw) == 0 {
		return nil, errors.Join(ErrCollectionPlanInvalid, err)
	}
	return append([]byte(nil), raw...), ctx.Err()
}

func collectionExecutionPart(ctx context.Context, view *collectionLedgerView, op string, ordinal uint64) (CollectionPlanLedgerFragment, error) {
	raw, err := collectionExecutionFrame(ctx, view, op, ordinal, true)
	if err != nil {
		return CollectionPlanLedgerFragment{}, err
	}
	row, err := decodeCollectionPlanLedgerRow(raw)
	if err != nil || row.OperationID != op || row.Part.Ordinal != ordinal {
		return CollectionPlanLedgerFragment{}, errors.Join(ErrCollectionPlanInvalid, err)
	}
	if err := ctx.Err(); err != nil {
		return CollectionPlanLedgerFragment{}, err
	}
	return row.Part, nil
}

func collectionExecutionInputItem(ctx context.Context, view *collectionLedgerView, op string, ordinal uint64) (CollectionItem, error) {
	item, _, err := collectionExecutionInputItemHash(ctx, view, op, ordinal)
	return item, err
}

// The hash binds ciphertext, MAC and all coordinates to the complete original
// verification. A reused index cannot accept a different canonical frame merely
// because its key/source metadata still matches the original plan row.
func collectionExecutionInputItemHash(ctx context.Context, view *collectionLedgerView, op string, ordinal uint64) (CollectionItem, [sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	raw, err := collectionExecutionFrame(ctx, view, op, ordinal, false)
	if err != nil {
		return CollectionItem{}, digest, err
	}
	row, err := decodeCollectionLedgerRow(raw)
	if err != nil || row.OperationID != op || row.Item.Ordinal != ordinal {
		return CollectionItem{}, digest, errors.Join(ErrCollectionPlanInvalid, err)
	}
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return CollectionItem{}, digest, err
	}
	defer view.mu.Unlock()
	if view.closed {
		return CollectionItem{}, digest, errCollectionLedgerClosed
	}
	indexed, exists, err := collectionReadIndex(view, op, row.Item.Key)
	if err != nil || !exists || indexed != ordinal {
		return CollectionItem{}, digest, errors.Join(ErrCollectionPlanInvalid, err)
	}
	if err := ctx.Err(); err != nil {
		return CollectionItem{}, digest, err
	}
	return row.Item, sha256.Sum256(raw), nil
}

func collectionExecutionPlanStats(ctx context.Context, view *collectionLedgerView, op string, count uint64, size int64) error {
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return err
	}
	defer view.mu.Unlock()
	if view.closed {
		return errCollectionLedgerClosed
	}
	actual, used, err := view.planStats(op)
	if err != nil || actual != count || used != size {
		return errors.Join(ErrCollectionPlanInvalid, err)
	}
	return ctx.Err()
}

func collectionExecutionInputStats(ctx context.Context, view *collectionLedgerView, s CollectionState) error {
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return err
	}
	defer view.mu.Unlock()
	if view.closed {
		return errCollectionLedgerClosed
	}
	if view.tx == nil {
		if uint64(len(view.rows[s.ID])) != s.ItemCount || len(view.keys[s.ID]) != len(view.rows[s.ID]) || view.operationBytes[s.ID] != s.EncodedBytes {
			return ErrCollectionPlanInvalid
		}
	} else {
		rows, keys := view.tx.Bucket(collectionLedgerRecords), view.tx.Bucket(collectionLedgerKeys)
		if rows == nil || keys == nil {
			return ErrCollectionPlanInvalid
		}
		r, k := rows.Bucket([]byte(s.ID)), keys.Bucket([]byte(s.ID))
		if r == nil || k == nil || r.Sequence() != s.ItemCount || k.Sequence() != uint64(s.EncodedBytes) {
			return ErrCollectionPlanInvalid
		}
	}
	return ctx.Err()
}

// Recompute the input commitment in ORIGINAL upload order, not execution order.
// The one transient decoded encrypted record is discarded each iteration.
func collectionExecutionInput(ctx context.Context, view *collectionLedgerView, s CollectionState, spend func(uint64) error) error {
	if err := collectionExecutionInputStats(ctx, view, s); err != nil {
		return err
	}
	digest, used := collectionInitialDigest(), int64(0)
	for ordinal := uint64(1); ordinal <= s.ItemCount; ordinal++ {
		if err := spend(1); err != nil {
			return err
		}
		item, err := collectionExecutionInputItem(ctx, view, s.ID, ordinal)
		if err != nil {
			return err
		}
		cost, err := collectionItemCost(s.ID, item)
		if err != nil || cost < 0 || cost > s.EncodedBytes-used {
			return errors.Join(ErrCollectionPlanInvalid, err)
		}
		used += cost
		digest, err = collectionNextDigest(digest, item)
		if err != nil {
			return err
		}
	}
	if used != s.EncodedBytes || digest != s.ProgressDigest {
		return ErrCollectionPlanInvalid
	}
	return ctx.Err()
}
