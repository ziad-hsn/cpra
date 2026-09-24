package persistence

import (
	"context"
	"time"
)

// CollectionUploadView identifies an inactive upload's exact captured prefix,
// including a zero-length prefix. It neither decrypts nor authorizes the data.
// Callers must authorize the operation and referenced identities before using
// this protected interface. It is not a complete-input validation capability.
type CollectionUploadView struct {
	view *CollectionValidationView
}

// CollectionUploadView returns a detached protected header and a view bounded by
// the captured Uploaded count. Appending an item invalidates this view: construct
// a fresh one and reconcile the original retry identity. An exact ciphertext
// retry may extend activity without invalidating its unchanged captured prefix.
// Reads themselves never renew expiry or create/modify an operation.
func (s *Store) CollectionUploadView(ctx context.Context, id string, at time.Time) (*CollectionUploadView, CollectionState, error) {
	view, header, err := s.collectionReadView(ctx, id, at, true)
	if err != nil {
		return nil, CollectionState{}, err
	}
	return &CollectionUploadView{view: view}, header, nil
}

// Check rechecks prefix identity, current phase/expiry and storage availability.
// Supply current observed time and check again after any preparation work; this
// read is not a reservation against later upload or cancellation commands.
func (v *CollectionUploadView) Check(ctx context.Context, at time.Time) error {
	return v.view.Check(ctx, at)
}

// Page returns at most 256 items and 4 MiB, ending at the captured Uploaded count.
// It returns detached ciphertext, never a decoded resource or provider secret.
func (v *CollectionUploadView) Page(ctx context.Context, after uint64, limit int, at time.Time) ([]CollectionItem, error) {
	return v.view.Page(ctx, after, limit, at)
}

// Item returns the original stored ciphertext at an uploaded ordinal. The next
// not-yet-uploaded ordinal is out of range, not a missing durable record.
func (v *CollectionUploadView) Item(ctx context.Context, ordinal uint64, at time.Time) (CollectionItem, error) {
	return v.view.Item(ctx, ordinal, at)
}

// Find uses the checked unique identity index within the captured prefix.
// An empty prefix and an absent key both return found=false without an error.
func (v *CollectionUploadView) Find(ctx context.Context, key CatalogKey, at time.Time) (CollectionItem, bool, error) {
	return v.view.Find(ctx, key, at)
}
