package persistence

import (
	"context"
	"errors"
	"time"
)

// CollectionOperationAs describes one collection to its original actor. Active
// ownership is checked before history access. After header retirement, a bounded
// original anchor or terminal-receipt metadata lookup recovers that owner; result rows,
// encrypted input and provider configuration are never read by this method.
//
// An elapsed staging deadline is an expired observation, not a newly committed
// terminal event. Retained validation results have their own read interface and
// retention lifetime. Callers must independently authorize the current actor.
func (s *Store) CollectionOperationAs(ctx context.Context, id, actor string, at time.Time) (CollectionReceipt, error) {
	return s.collectionOperationAs(ctx, id, actor, at, nil)
}

func (s *Store) collectionOperationAs(ctx context.Context, id, actor string, at time.Time, observation *CollectionOperationObservation) (CollectionReceipt, error) {
	started := time.Now()
	empty := CollectionReceipt{}
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return empty, ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !catalogIdentifier(actor, 128) {
		return empty, ErrOperationNotFound
	}
	epoch, sequence, err := ParseOperationHandle(id)
	if err != nil {
		return empty, ErrOperationNotFound
	}
	var captured collectionOperationAccess
	if err := s.withCollectionOperationMetadata(ctx, func(f *machine) error {
		if epoch != f.image.OperationEpoch {
			return ErrOperationExpired
		}
		if sequence > f.image.OperationHighWater {
			return ErrOperationNotFound
		}
		captured.epoch, captured.index, captured.highWater, captured.history = epoch, f.image.Index, f.image.OperationHighWater, f.history
		if header, exists := f.image.Collections[id]; exists {
			if header.Actor != actor {
				return ErrOperationNotFound
			}
			if header.ID != id || header.validate() != nil {
				return ErrCollectionUnavailable
			}
			if at.Before(header.ActivityAt) || header.Activation != nil && at.Before(header.Activation.At) || !header.TerminalAt.IsZero() && at.Before(header.TerminalAt) ||
				header.Execution != nil && at.Before(header.Execution.LastAt) || header.ExecutionResult != nil && at.Before(header.ExecutionResult.Summary.FinalizedAt) {
				return ErrCollectionConflict
			}
			if collectionLive(header.Phase) || header.Activation != nil {
				result := collectionOperationObservationFor(header, at)
				captured.observation = &result
			} else {
				r := collectionReceiptFor(header)
				captured.expected = &r
			}
		}
		return nil
	}); err != nil {
		return empty, err
	}
	// Only detached receipt metadata crosses this boundary. Never hold Store or
	// FSM locks while acquiring history locks or reading a history segment.
	var receipt CollectionReceipt
	var readErr error
	receiptLookup := false
	if captured.observation != nil {
		receipt = *captured.observation
		if o := receipt.ExecutionObservation; o != nil && o.Summary != nil && o.State != "expired" {
			if captured.history == nil {
				readErr = ErrHistoryUnavailable
			} else if err := collectionReadLock(ctx, &captured.history.mu); err != nil {
				readErr = err
			} else {
				readErr = captured.history.verifyCollectionExecutionObservationLocked(ctx, o, captured.index, at)
				captured.history.mu.RUnlock()
			}
		}
		if receipt.Phase == "validated" || receipt.Phase == "rejected" {
			if receipt.Validation == nil {
				readErr = ErrHistoryUnavailable
			} else {
				// The final ordinal checks the original sealed summary/progress
				// without reading any result items.
				_, readErr = captured.history.collectionValidationPage(ctx, *receipt.Validation,
					captured.index, receipt.Validation.Descriptor.Count, 1, at)
			}
		}
	} else {
		if captured.expected == nil {
			// Execution finalization has its own retention lifetime. Recover it
			// before consulting an earlier, possibly expired cancellation receipt.
			var retained collectionRetainedExecution
			retained, readErr = captured.history.collectionRetainedExecution(ctx, id, actor, captured.index, at)
			if readErr == nil {
				receipt = retained.observation()
			}
		}
		if captured.expected != nil || errors.Is(readErr, errCollectionExecutionAbsent) {
			// No execution evidence: preserve the existing inactive collection
			// receipt lookup, including original owner and validation semantics.
			receiptLookup = true
			receipt, readErr = captured.history.collectionReceiptExpected(ctx, id, captured.index, at, captured.expected)
		}
	}
	if err := s.withCollectionOperationMetadata(ctx, func(f *machine) error {
		if captured.epoch != f.image.OperationEpoch {
			return ErrOperationExpired
		}
		if sequence > f.image.OperationHighWater {
			return ErrOperationNotFound
		}
		if f.image.Index < captured.index || f.history != captured.history {
			return ErrCollectionUnavailable
		}
		if head, exists := f.image.Collections[id]; exists {
			if head.Actor != actor {
				return ErrOperationNotFound
			}
			if head.ID != id || head.validate() != nil {
				return ErrCollectionUnavailable
			}
			if o := receipt.ExecutionObservation; o != nil && o.Summary != nil {
				current := head.ExecutionResult
				if current == nil || !collectionExecutionSummariesEqual(&current.Summary, o.Summary) {
					return ErrCollectionUnavailable
				}
				if o.Descriptor != nil && (!current.HistorySealed || collectionExecutionReceiptFor(*current).Descriptor != *o.Descriptor) {
					return ErrHistoryUnavailable
				}
				if !current.HistoryExpiredAt.IsZero() || f.history.retentionCutoffReached(o.Summary.FinalizedAt) {
					o.State = "expired"
				}
			}
		} else if captured.observation != nil && captured.observation.Activation != nil {
			// A captured sealed original result survives source retirement. A
			// provisional header must never acquire availability from its absence.
			o := receipt.ExecutionObservation
			if o == nil || o.Summary == nil || o.Descriptor == nil || o.State != "ready" && o.State != "expired" {
				return ErrCollectionUnavailable
			}
		}
		if o := receipt.ExecutionObservation; o != nil && o.Summary != nil &&
			(!at.Add(time.Since(started)).Before(o.Summary.FinalizedAt.AddDate(0, 0, 30)) || f.history.retentionCutoffReached(o.Summary.FinalizedAt)) {
			o.State = "expired"
		}
		return nil
	}); err != nil {
		return empty, err
	}
	if readErr != nil {
		if errors.Is(readErr, ErrOperationNotFound) && receiptLookup {
			return empty, ErrOperationExpired
		}
		return empty, readErr
	}
	if receipt.Actor != actor {
		return empty, ErrOperationNotFound
	}
	if captured.expected != nil && !collectionReceiptsEqual(receipt, *captured.expected) {
		return empty, ErrHistoryUnavailable
	}
	if at.Before(receipt.ActivityAt) || receipt.Activation != nil && at.Before(receipt.Activation.At) || !receipt.TerminalAt.IsZero() && at.Before(receipt.TerminalAt) {
		return empty, ErrCollectionConflict
	}
	if receipt.Validation != nil && !at.Before(receipt.Validation.FinalizedAt.AddDate(0, 0, 30)) {
		receipt.Validation = nil
	}
	if o := receipt.ExecutionObservation; o != nil && o.Summary != nil && !at.Add(time.Since(started)).Before(o.Summary.FinalizedAt.AddDate(0, 0, 30)) {
		o.State = "expired"
	}
	if observation != nil {
		*observation = CollectionOperationObservation{store: s, receipt: receipt.Clone(), access: captured, at: at, started: started}
		observation.access.observation, observation.access.expected = nil, nil
	}
	return receipt, nil
}

// Captured metadata contains no executable state or protected input envelope.
// All nested receipt values are detached by the projection helpers.
type collectionOperationAccess struct {
	epoch       string
	index       uint64
	highWater   uint64
	history     *HistoryStore
	observation *CollectionReceipt
	expected    *CollectionReceipt
}

// read runs only metadata inspection while the ordered Store/FSM locks are held.
// It must not acquire a history lock, perform disk I/O or call an external service.
func (s *Store) withCollectionOperationMetadata(ctx context.Context, read func(*machine) error) error {
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if s.fsm == nil {
		return ErrCollectionUnavailable
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return err
	}
	defer s.fsm.mu.RUnlock()
	if err := s.collectionCoordinatorHealth(); err != nil {
		return err
	}
	if err := read(s.fsm); err != nil {
		return err
	}
	return ctx.Err()
}
