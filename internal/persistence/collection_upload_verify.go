package persistence

import (
	"context"
	"time"
)

// VerifyPrefix recomputes the captured prefix's exact encrypted-row commitment
// and encoded byte count. Equivalent plaintext re-encrypted into a different
// envelope is not the original committed prefix. Verification retains only one
// bounded page (at most 256 rows and 4 MiB), and hashes outside the owner locks.
//
// This protected read neither authorizes nor decrypts input and performs no
// durable writes. The caller must check current authority around it. Supply the
// current observation time; elapsed verification time counts toward expiry.
// Success does not reserve the prefix against a later append or cancellation.
func (v *CollectionUploadView) VerifyPrefix(ctx context.Context, at time.Time) error {
	if ctx == nil || v == nil || v.view == nil || at.IsZero() {
		return ErrCollectionInvalid
	}
	started := time.Now()
	currentTime := func() time.Time { return at.Add(time.Since(started)) }
	if err := v.Check(ctx, currentTime()); err != nil {
		return err
	}
	head := v.view.header
	digest, used := collectionInitialDigest(), int64(0)
	var after uint64
	for after < head.Uploaded {
		page, err := v.Page(ctx, after, collectionLedgerBatchLimit, currentTime())
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return ErrCollectionUnavailable
		}
		for _, item := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			if item.Ordinal != after+1 || item.Ordinal > head.Uploaded {
				return ErrCollectionUnavailable
			}
			cost, err := collectionItemCost(head.ID, item)
			if err != nil || cost < 0 || cost > head.EncodedBytes-used {
				return ErrCollectionUnavailable
			}
			used += cost
			digest, err = collectionNextDigest(digest, item)
			if err != nil {
				return ErrCollectionUnavailable
			}
			after = item.Ordinal
		}
		if err := v.Check(ctx, currentTime()); err != nil {
			return err
		}
	}
	if used != head.EncodedBytes || digest != head.ProgressDigest {
		return ErrCollectionUnavailable
	}
	return v.verifyAppendCurrent(ctx, currentTime, head)
}
