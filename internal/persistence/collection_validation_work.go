package persistence

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

var ErrCollectionCoordinatorRegistered = errors.New("collection validation coordinator already registered")

// CollectionValidationWork is a bounded, detached observation of one original
// request. It contains no secret envelope or resource payload. An observation
// grants no execution authority: a coordinator still needs the durable claim
// and commit-time fences for this exact operation.
type CollectionValidationWork struct {
	OperationID     string
	UploadID        string
	Phase           string
	Request         CollectionValidationRequest
	Progress        CollectionCleanup
	ResultFinalized bool
	ExpiresAt       time.Time
}

// CollectionValidationWork lists every retained request, including terminal,
// old-epoch and expired headers. Those entries remain observations, not work to
// restart. It reads only the bounded FSM header map, never ledger/history rows,
// and never renews a deadline. The caller supplies a current observation time.
func (s *Store) CollectionValidationWork(ctx context.Context, at time.Time) ([]CollectionValidationWork, error) {
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return nil, ErrCollectionInvalid
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
	// Validate the complete selected set before cloning nested metadata. The
	// temporary headers borrow immutable FSM fields only while its lock is held.
	selectedHeaders := make([]selected, 0, len(s.fsm.image.Collections))
	for id, header := range s.fsm.image.Collections {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if header.ValidationRequest == nil {
			continue
		}
		_, sequence, err := ParseOperationHandle(id)
		if err != nil || header.ID != id || header.validate() != nil {
			return nil, ErrCollectionUnavailable
		}
		if at.Before(header.ActivityAt) || header.Activation != nil && at.Before(header.Activation.At) || !header.TerminalAt.IsZero() && at.Before(header.TerminalAt) {
			return nil, ErrCollectionConflict
		}
		selectedHeaders = append(selectedHeaders, selected{header, sequence})
	}
	slices.SortFunc(selectedHeaders, func(a, b selected) int {
		if compared := a.header.ValidationRequest.RequestedAt.Compare(b.header.ValidationRequest.RequestedAt); compared != 0 {
			return compared
		}
		if a.sequence < b.sequence {
			return -1
		}
		if a.sequence > b.sequence {
			return 1
		}
		// Restored terminal metadata can retain a sequence from another epoch.
		// Break that otherwise equal tie deterministically without changing it.
		if a.header.ID < b.header.ID {
			return -1
		}
		if a.header.ID > b.header.ID {
			return 1
		}
		return 0
	})
	work := make([]CollectionValidationWork, 0, len(selectedHeaders))
	for _, entry := range selectedHeaders {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header := entry.header
		work = append(work, CollectionValidationWork{OperationID: header.ID, UploadID: header.UploadID, Phase: header.Phase,
			Request: header.ValidationRequest.Clone(), Progress: collectionCleanupFor(header),
			ResultFinalized: header.Validation != nil && !header.Validation.FinalizedAt.IsZero(), ExpiresAt: header.ExpiresAt})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return work, nil
}

// RegisterCollectionValidationCoordinator permits one coordinator for the
// lifetime of this opened Store, including after cancellation of its run. There
// is deliberately no release/reacquire operation. A new Store after a stopped
// restart may register again; original durable claims still fence its work.
func (s *Store) RegisterCollectionValidationCoordinator(ctx context.Context, runID string) error {
	if ctx == nil || !validOperationEpoch(runID) {
		return ErrCollectionInvalid
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
	if s.validationRunID != "" {
		return ErrCollectionCoordinatorRegistered
	}
	s.validationRunID = runID
	return nil
}

// Caller holds Store.mu before FSM.mu. Recorded health, rather than a fresh
// filesystem probe, decides availability here. Work selection performs no I/O.
func (s *Store) collectionCoordinatorHealth() error {
	select {
	case <-s.stop:
		return ErrCollectionUnavailable
	default:
	}
	if s.administrative || s.opening || s.err != nil || s.fsm == nil || s.fsm.err != nil || s.fsm.bootstrapPending() ||
		s.fsm.restorePending() || s.fsm.image.Authentication != nil && s.fsm.image.Authentication.ResetRequired {
		return ErrCollectionUnavailable
	}
	if s.raft != nil && s.raft.State() != raft.Leader {
		return errors.Join(ErrCollectionUnavailable, raft.ErrNotLeader)
	}
	return nil
}

func collectionCoordinatorLock(ctx context.Context, mu *sync.RWMutex) error {
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
