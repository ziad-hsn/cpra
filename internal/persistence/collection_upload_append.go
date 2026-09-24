package persistence

import (
	"bytes"
	"context"
	"time"
)

// VerifyAppend verifies that this captured upload is exactly one original row
// beyond prior. The caller must already have verified prior's encrypted prefix
// and must supply the exact pending ciphertext retained before submission. This
// is an incremental commitment check, not a replacement for VerifyPrefix.
//
// Only the appended frame is copied; encoding and hashing happen outside owner
// locks. The protected read neither decrypts nor authorizes data, renews expiry,
// or reserves the upload against later commands. Elapsed time counts toward
// expiry, including time spent waiting for a lock.
func (v *CollectionUploadView) VerifyAppend(ctx context.Context, prior CollectionState, pending CollectionItem, at time.Time) error {
	if ctx == nil || v == nil || v.view == nil || v.view.store == nil || v.view.store.fsm == nil || at.IsZero() {
		return ErrCollectionInvalid
	}
	started := time.Now()
	currentTime := func() time.Time { return at.Add(time.Since(started)) }
	if err := ctx.Err(); err != nil {
		return err
	}
	if prior.validate() != nil || !collectionUploadAppendShape(prior) || pending.validate() != nil {
		return ErrCollectionInvalid
	}
	head := v.view.header
	if !collectionUploadAppendIdentity(prior, head) || head.Uploaded != prior.Uploaded+1 || pending.Ordinal != head.Uploaded ||
		head.ActivityAt.Before(prior.ActivityAt) || head.ExpiresAt.Before(prior.ExpiresAt) {
		return ErrCollectionConflict
	}
	if err := v.verifyAppendCurrent(ctx, currentTime, head); err != nil {
		return err
	}
	expected, err := collectionItemEncoding(head.ID, pending)
	if err != nil {
		return ErrCollectionInvalid
	}
	cost := int64(len(expected)) // collectionItemCost uses the same canonical row.
	digest, err := collectionNextDigest(prior.ProgressDigest, pending)
	if err != nil {
		return ErrCollectionInvalid
	}
	if head.EncodedBytes < prior.EncodedBytes || head.EncodedBytes-prior.EncodedBytes != cost || head.ProgressDigest != digest {
		return ErrCollectionUnavailable
	}
	var stored []byte
	if err := v.view.read(ctx, currentTime(), func(ledger *collectionLedgerView) error {
		// Native totals are sequence metadata. Check physical prefix edges too,
		// without traversing the already-certified original prefix.
		if ledger.tx != nil {
			rows := ledger.tx.Bucket(collectionLedgerRecords).Bucket([]byte(head.ID))
			first, _ := rows.Cursor().First()
			last, _ := rows.Cursor().Last()
			if !bytes.Equal(first, collectionOrdinal(1)) || !bytes.Equal(last, collectionOrdinal(head.Uploaded)) {
				return ErrCollectionUnavailable
			}
		}
		ordinal, found, err := collectionReadIndex(ledger, head.ID, pending.Key)
		if err != nil || !found || ordinal != pending.Ordinal {
			return ErrCollectionUnavailable
		}
		frame, err := ledger.encodedItem(head.ID, pending.Ordinal)
		if err != nil || len(frame) == 0 || len(frame) > collectionLedgerMaxFrame {
			return ErrCollectionUnavailable
		}
		stored = bytes.Clone(frame)
		return nil
	}); err != nil {
		return err
	}
	if !bytes.Equal(expected, stored) {
		return ErrCollectionUnavailable
	}
	return v.verifyAppendCurrent(ctx, currentTime, head)
}

// verifyAppendCurrent evaluates time after lock acquisition. Unlike a timestamp
// sampled before a potentially blocked read, this fences expiry during the wait.
func (v *CollectionUploadView) verifyAppendCurrent(ctx context.Context, now func() time.Time, original CollectionState) error {
	f := v.view.store.fsm
	if err := collectionReadLock(ctx, &f.mu); err != nil {
		return err
	}
	defer f.mu.RUnlock()
	head, err := v.view.current(original.ID, now())
	if err != nil {
		return err
	}
	if !collectionUploadAppendIdentity(original, head) || head.ActivityAt.Before(original.ActivityAt) || head.ExpiresAt.Before(original.ExpiresAt) {
		return ErrCollectionConflict
	}
	return ctx.Err()
}

func collectionUploadAppendShape(h CollectionState) bool {
	return h.Phase == "uploading" && h.RemovedRows == 0 && h.RemovedBytes == 0 && h.TerminalAt.IsZero() && h.InvalidatedByRestore == "" &&
		h.ExecutionRetirement == nil && h.ExecutionResult == nil && h.Execution == nil && h.Activation == nil &&
		h.Cancellation == nil && h.Plan == nil && h.Validation == nil && h.ValidationRequest == nil
}

func collectionUploadAppendIdentity(a, b CollectionState) bool {
	if !collectionUploadAppendShape(a) || !collectionUploadAppendShape(b) || a.ID != b.ID || a.UploadID != b.UploadID ||
		a.Actor != b.Actor || a.NormalizationProfile != b.NormalizationProfile || a.IdentityFormat != b.IdentityFormat ||
		a.ContentDigest != b.ContentDigest || a.ItemCount != b.ItemCount || a.MaxEncodedBytes != b.MaxEncodedBytes || !a.CreatedAt.Equal(b.CreatedAt) ||
		(a.Owner == nil) != (b.Owner == nil) || a.Owner != nil && *a.Owner != *b.Owner ||
		(a.Admission == nil) != (b.Admission == nil) {
		return false
	}
	if a.Admission != nil && (a.Admission.Epoch != b.Admission.Epoch || a.Admission.RequestDigest != b.Admission.RequestDigest || !a.Admission.ExpiresAt.Equal(b.Admission.ExpiresAt)) {
		return false
	}
	return a.Secret.Format == b.Secret.Format && a.Secret.KeyID == b.Secret.KeyID && bytes.Equal(a.Secret.WrappedKey, b.Secret.WrappedKey) &&
		bytes.Equal(a.Secret.Nonce, b.Secret.Nonce) && bytes.Equal(a.Secret.Ciphertext, b.Secret.Ciphertext)
}
