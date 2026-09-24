package persistence

import (
	"context"
	"errors"
	"slices"
	"time"
)

var ErrCollectionExecutionCoordinatorRegistered = errors.New("collection execution coordinator already registered")

// CollectionExecutionPreparedWork identifies the original committed candidate.
// It contains no ciphertext, provider configuration or replacement conditions.
type CollectionExecutionPreparedWork struct {
	ID           string
	Ordinal      uint64
	InputOrdinal uint64
	RowDigest    string
}

// CollectionExecutionWork is a detached metadata observation, not an execution
// grant. A coordinator must obtain current authority and recheck the original
// binding, profile and progress at each durable execution command.
type CollectionExecutionWork struct {
	OperationID        string
	Actor              string
	Binding            CollectionExecutionBinding
	CapabilitiesDigest string
	ItemCount          uint64
	Begun              bool
	Processed          uint64
	Accepted           uint64
	ChildTerminals     uint64
	NextOrdinal        uint64
	ActivationAt       time.Time
	LastAt             time.Time
	Prepared           *CollectionExecutionPreparedWork
}

// CollectionExecutionWork selects only applying parents in the current epoch.
// It inspects at most 64 retained headers, with no ledger/history access and no
// deadline renewal. Former upload expiry does not expire admitted execution.
// ActivationAt/LastAt may be after the caller's observation when the clock has
// moved backwards; the coordinator must wait for that parent without blocking
// other parents. No current-principal or provider access is granted here.
func (s *Store) CollectionExecutionWork(ctx context.Context, at time.Time) ([]CollectionExecutionWork, error) {
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return nil, ErrCollectionInvalid
	}
	if s == nil {
		return nil, ErrCollectionUnavailable
	}
	if err := collectionReadLock(ctx, &s.mu); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	if s.fsm == nil {
		return nil, ErrCollectionUnavailable
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return nil, err
	}
	defer s.fsm.mu.RUnlock()
	if err := s.collectionCoordinatorHealth(); err != nil {
		return nil, err
	}
	if len(s.fsm.image.Collections) > maxCollectionOperations {
		return nil, ErrCollectionUnavailable
	}
	type selected struct {
		header   CollectionState
		sequence uint64
	}
	// Borrow only while the owner lock is held. Validate before cloning any
	// nested metadata so corruption never returns a partially usable work list.
	selectedHeaders := make([]selected, 0, len(s.fsm.image.Collections))
	for id, header := range s.fsm.image.Collections {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if header.Phase != "applying" {
			continue
		}
		epoch, sequence, err := ParseOperationHandle(id)
		if err != nil || id != header.ID || !collectionActivationStorageFormat(s.fsm.image.Version) ||
			header.Execution != nil && !collectionExecutionStorageFormat(s.fsm.image.Version) || header.validate() != nil {
			return nil, ErrCollectionUnavailable
		}
		if epoch != s.fsm.image.OperationEpoch {
			continue
		}
		if sequence > s.fsm.image.OperationHighWater {
			return nil, ErrCollectionUnavailable
		}
		selectedHeaders = append(selectedHeaders, selected{header: header, sequence: sequence})
	}
	slices.SortFunc(selectedHeaders, func(a, b selected) int {
		if compared := a.header.Activation.At.Compare(b.header.Activation.At); compared != 0 {
			return compared
		}
		if a.sequence < b.sequence {
			return -1
		}
		if a.sequence > b.sequence {
			return 1
		}
		return 0
	})
	work := make([]CollectionExecutionWork, 0, len(selectedHeaders))
	for _, entry := range selectedHeaders {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header := entry.header
		binding, err := collectionExecutionBindingFor(header)
		if err != nil {
			return nil, ErrCollectionUnavailable
		}
		item := CollectionExecutionWork{OperationID: header.ID, Actor: header.Actor, Binding: binding,
			CapabilitiesDigest: header.Activation.CapabilitiesDigest, ItemCount: header.ItemCount,
			ActivationAt: header.Activation.At, LastAt: header.Activation.At, NextOrdinal: 1}
		if progress := header.Execution; progress != nil {
			item.Begun, item.Processed, item.Accepted, item.ChildTerminals = true, progress.Processed, progress.Accepted, progress.ChildTerminals
			item.LastAt = progress.LastAt
			item.NextOrdinal = 0
			if progress.Processed < header.ItemCount {
				item.NextOrdinal = progress.Processed + 1
			}
			if prepared := progress.Prepared; prepared != nil {
				item.Prepared = &CollectionExecutionPreparedWork{ID: prepared.ID, Ordinal: prepared.Ordinal,
					InputOrdinal: prepared.InputOrdinal, RowDigest: prepared.RowDigest}
			}
		}
		work = append(work, item)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return work, nil
}

// RegisterCollectionExecutionCoordinator permits one coordinator for this
// opened Store's lifetime. Cancellation does not release its slot. Registration
// is process-local; reopening a stopped Store permits a new coordinator while
// original durable execution bindings continue to fence its commands.
func (s *Store) RegisterCollectionExecutionCoordinator(ctx context.Context, runID string) error {
	if ctx == nil || !validOperationEpoch(runID) {
		return ErrCollectionInvalid
	}
	if s == nil {
		return ErrCollectionUnavailable
	}
	if err := collectionCoordinatorLock(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.executionRunID != "" {
		return ErrCollectionExecutionCoordinatorRegistered
	}
	s.executionRunID = runID
	return nil
}
