package persistence

import (
	"context"
	"errors"
	"io"
	"runtime"
	"time"
)

type actionCompactionResult struct {
	ledger   *collectionLedger
	err      error
	duration time.Duration
}
type actionCompaction struct {
	source *collectionLedger
	fence  image
	done   chan actionCompactionResult
	cancel context.CancelFunc
}

func actionCompactionFence(state image) image {
	fence := image{Version: state.Version}
	if state.Collections != nil {
		fence.Collections = make(map[string]CollectionState, len(state.Collections))
		for id, collection := range state.Collections {
			fence.Collections[id] = collection.Clone()
		}
	}
	if state.ActionStorage != nil {
		progress := *state.ActionStorage
		fence.ActionStorage = &progress
	}
	return fence
}

// A replacement is private until a short publication boundary verifies that
// every copied ledger namespace still has the captured committed identity.
// Concurrent health-only observations do not invalidate that identity.
func (s *Store) beginActionCompaction() error {
	if s.actionCompaction != nil || s.collectionGenerations == nil || s.raft == nil {
		return nil
	}
	if !s.lastActionCompactionAttempt.IsZero() && time.Since(s.lastActionCompactionAttempt) < 5*time.Minute {
		return nil
	}
	f := s.fsm
	f.mu.RLock()
	if f.collections == nil || f.collections.db == nil || f.image.ActionStorage == nil || f.image.ActionStorage.Retired <= s.compactedActions {
		f.mu.RUnlock()
		return nil
	}
	source := f.collections
	fence := actionCompactionFence(f.image)
	view, err := source.Freeze()
	f.mu.RUnlock()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.maintenanceContext)
	s.lastActionCompactionAttempt = time.Now()
	operation := &actionCompaction{source: source, fence: fence, done: make(chan actionCompactionResult, 1), cancel: cancel}
	s.actionCompaction = operation
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		defer view.Close()
		started := time.Now()
		owner := s.collectionGenerations
		owner.mu.Lock()
		candidate, err := openCollectionLedger(owner.directory, maxCollectionLedgerBytes)
		if candidate != nil {
			owner.live.Store(candidate.generation, candidate)
		}
		owner.mu.Unlock()
		if err == nil {
			err = copyCollectionGeneration(ctx, fence, view, candidate)
		}
		if err != nil && candidate != nil {
			err = errors.Join(err, candidate.Close())
			candidate = nil
		}
		operation.done <- actionCompactionResult{ledger: candidate, err: err, duration: time.Since(started)}
	}()
	return nil
}

func copyCollectionGeneration(ctx context.Context, i image, view *collectionLedgerView, target *collectionLedger) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		w := &cooperativeLedgerWriter{ctx: ctx, writer: writer, until: time.Now().Add(25 * time.Millisecond)}
		err := writeCollectionSnapshotRows(w, i.Version, view)
		_ = writer.CloseWithError(err)
		done <- err
	}()
	readErr := importCollectionLedger(reader, target)
	if readErr == nil && collectionPlanStorageFormat(i.Version) {
		readErr = importCollectionPlanLedger(reader, target)
	}
	if readErr == nil && collectionValidationStorageFormat(i.Version) {
		readErr = importCollectionValidationLedger(reader, target)
	}
	if readErr == nil && collectionExecutionStorageFormat(i.Version) {
		offsets := map[string]uint64{}
		for id, state := range i.Collections {
			if r := state.ExecutionRetirement; r != nil && r.Checkpoint != nil {
				offsets[id] = r.Checkpoint.Progress.Processed
			}
		}
		readErr = importCollectionExecutionLedgerWithRetirement(reader, target, offsets)
	}
	if readErr == nil && i.ActionStorage != nil && i.ActionStorage.Sequence != 0 {
		_, readErr = importActionLedger(reader, target)
	}
	if readErr == nil {
		var extra [1]byte
		if n, err := reader.Read(extra[:]); n != 0 || err != io.EOF {
			readErr = errCollectionLedgerCorrupt
		}
	}
	_ = reader.CloseWithError(readErr)
	writeErr := <-done
	return errors.Join(readErr, writeErr, ctx.Err())
}

type cooperativeLedgerWriter struct {
	ctx    context.Context
	writer io.Writer
	until  time.Time
}

func (w *cooperativeLedgerWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if time.Now().After(w.until) {
		runtime.Gosched()
		w.until = time.Now().Add(25 * time.Millisecond)
	}
	return w.writer.Write(p)
}

func (s *Store) finishActionCompaction() (string, error) {
	operation := s.actionCompaction
	if operation == nil {
		return "", nil
	}
	var result actionCompactionResult
	select {
	case result = <-operation.done:
	default:
		return "copying", nil
	}
	s.actionCompaction = nil
	defer operation.cancel()
	if result.err != nil {
		return "copy_failed", result.err
	}
	candidate := result.ledger
	keep := false
	defer func() {
		if !keep && candidate != nil {
			_ = candidate.Close()
		}
	}()
	f := s.fsm
	f.transition.Lock()
	defer f.transition.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "storage_unavailable", f.err
	}
	if f.collections != operation.source || !sameLedgerState(operation.fence, f.image) {
		return "state_changed", nil
	}
	if err := s.collectionGenerations.publish(candidate); err != nil {
		return "publication_failed", err
	}
	old := f.collections
	f.collections = candidate
	s.collectionGenerations.track(candidate)
	f.collectionExecutionIndex, f.collectionExecutionLedger = nil, nil
	f.collectionPublicationIndex, f.collectionPublicationLedger = nil, nil
	f.collectionSourceCertificate = nil
	for _, prefix := range f.planPrefixes {
		prefix.close()
	}
	f.planPrefixes = nil
	keep = true
	s.compactedActions = operation.fence.ActionStorage.Retired
	return "", old.Close()
}

func (s *Store) stopActionCompaction() {
	if operation := s.actionCompaction; operation != nil {
		operation.cancel()
		result := <-operation.done
		if result.ledger != nil {
			_ = result.ledger.Close()
		}
		s.actionCompaction = nil
	}
}

func (s *Store) maintainActions(at time.Time, after *string) error {
	condition, err := s.finishActionCompaction()
	if err != nil {
		return err
	}
	if s.actionCompaction != nil {
		return s.sampleActionAccounting(0, 0, condition)
	}
	selection, err := s.actionRetirementCandidates(at, *after)
	if err != nil {
		return err
	}
	*after = selection.after
	if selection.command != nil {
		ctx, cancel := context.WithTimeout(s.maintenanceContext, 5*time.Second)
		results, err := s.Submit(ctx, []Command{{Kind: "action_retire", At: at, ActionRetirement: selection.command}})
		cancel()
		if err != nil {
			if IsLeadershipUnavailable(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCommitUnconfirmed) {
				return nil
			}
			return err
		}
		for _, result := range results {
			if result.Err != nil && !errors.Is(result.Err, errActionRetirementChanged) {
				return result.Err
			}
		}
	}
	if err := s.beginActionCompaction(); err != nil {
		return err
	}
	if s.actionCompaction != nil {
		condition = "copying"
	}
	return s.sampleActionAccounting(selection.blocked, selection.blockedBytes, condition)
}
