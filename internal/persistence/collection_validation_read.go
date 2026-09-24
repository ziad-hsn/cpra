package persistence

import (
	"context"
	"time"

	"github.com/hashicorp/raft"
)

// CollectionValidationPage reads the sealed original result with bounded
// ordinal pagination. Callers must separately authorize its original owner and
// resources; this protected storage method is not an HTTP endpoint. It never
// recompiles, decrypts provider inputs, opens jobs or extends retention.
func (s *Store) CollectionValidationPage(ctx context.Context, id string, after uint64, limit int, at time.Time) (CollectionValidationHistoryPage, error) {
	if ctx == nil || at.IsZero() || limit < 0 || limit > 500 || after > CollectionValidationMaxItems {
		return CollectionValidationHistoryPage{}, ErrCollectionInvalid
	}
	var original *CollectionValidationReceipt
	err := s.withAuthenticationRead(ctx, func(f *machine) error {
		epoch, _, parseErr := ParseOperationHandle(id)
		if parseErr != nil {
			return ErrOperationNotFound
		}
		if epoch != f.image.OperationEpoch {
			return ErrOperationExpired
		}
		if head, ok := f.image.Collections[id]; ok {
			if head.validate() != nil {
				return ErrCollectionUnavailable
			}
			if head.Validation != nil && !head.Validation.HistoryExpiredAt.IsZero() {
				return ErrOperationExpired
			}
			original = collectionValidationReceiptFor(head.Validation)
			if original == nil {
				return ErrCollectionConflict
			}
		}
		return nil
	})
	if err != nil {
		return CollectionValidationHistoryPage{}, err
	}
	if original == nil {
		receipt, err := s.CollectionReceipt(ctx, id, at)
		if err != nil {
			return CollectionValidationHistoryPage{}, err
		}
		if receipt.Validation == nil {
			return CollectionValidationHistoryPage{}, ErrCollectionConflict
		}
		original = receipt.Validation
	}
	expected := *original
	var page CollectionValidationHistoryPage
	err = s.withAuthenticationRead(ctx, func(f *machine) error {
		if f.bootstrapPending() || f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired || s.raft != nil && s.raft.State() != raft.Leader {
			return ErrCollectionUnavailable
		}
		epoch, _, _ := ParseOperationHandle(id)
		if epoch != f.image.OperationEpoch {
			return ErrOperationExpired
		}
		// Publication can persist expiry after the first protected read. Do not
		// let a backward supplied clock or compact receipt drop that fact.
		if head, ok := f.image.Collections[id]; ok {
			if head.ID != id || head.validate() != nil {
				return ErrCollectionUnavailable
			}
			if head.Validation != nil && !head.Validation.HistoryExpiredAt.IsZero() {
				return ErrOperationExpired
			}
		}
		var readErr error
		page, readErr = f.history.collectionValidationPage(ctx, expected, f.image.Index, after, limit, at)
		return readErr
	})
	if err != nil {
		return CollectionValidationHistoryPage{}, err
	}
	return page, nil
}
