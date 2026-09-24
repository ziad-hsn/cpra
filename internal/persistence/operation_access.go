package persistence

import (
	"context"
	"errors"
	"strings"
	"time"
)

// OperationContext reads shared ordinary receipts with cancellable metadata and
// history-lock waits. It fixes the history upper bound to one committed index,
// releases owner locks before I/O, and rejects a changed restore epoch afterward.
// The supplied observation time decides reservation expiry without renewal.
func (s *Store) OperationContext(ctx context.Context, id string, at time.Time) (OperationReceipt, error) {
	empty := OperationReceipt{}
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return empty, ErrOperationReservation
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !catalogIdentifier(id, 256) {
		return empty, ErrOperationNotFound
	}
	typed := strings.HasPrefix(id, "op.")
	var epoch string
	var sequence uint64
	if typed {
		var err error
		epoch, sequence, err = ParseOperationHandle(id)
		if err != nil {
			return empty, ErrOperationNotFound
		}
	}
	var observedEpoch string
	var index, highWater uint64
	var history *HistoryStore
	var receipt OperationReceipt
	found := false
	err := s.withCollectionOperationMetadata(ctx, func(f *machine) error {
		observedEpoch, index, highWater, history = f.image.OperationEpoch, f.image.Index, f.image.OperationHighWater, f.history
		if typed && epoch != observedEpoch {
			return ErrOperationExpired
		}
		if typed && sequence > highWater {
			return ErrOperationNotFound
		}
		if current, ok := f.image.Operations[id]; ok {
			receipt, found = current, true
			return nil
		}
		if reservation, ok := f.image.OperationReservations[id]; ok {
			if !at.Before(reservation.ExpiresAt) {
				return ErrOperationExpired
			}
			receipt, found = reservation.OperationReceipt, true
		}
		return nil
	})
	if err != nil {
		return empty, operationAccessError(err)
	}
	var readErr error
	if !found {
		receipt, readErr = history.operationContext(ctx, id, &index)
	}
	if err := s.withCollectionOperationMetadata(ctx, func(f *machine) error {
		if f.image.OperationEpoch != observedEpoch {
			return ErrOperationExpired
		}
		if f.image.Index < index {
			return ErrHistoryUnavailable
		}
		return nil
	}); err != nil {
		return empty, operationAccessError(err)
	}
	if readErr != nil {
		if typed && sequence <= highWater && errors.Is(readErr, ErrOperationNotFound) {
			return empty, ErrOperationExpired
		}
		return empty, readErr
	}
	if receipt.Outcome == "reservation_expired" {
		return empty, ErrOperationExpired
	}
	return receipt, nil
}

func operationAccessError(err error) error {
	if errors.Is(err, ErrCollectionUnavailable) {
		return ErrHistoryUnavailable
	}
	return err
}
