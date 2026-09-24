package persistence

import (
	"context"
	"errors"
	"time"
)

// One maintenance turn proposes one bounded deletion. The command fence, not
// this observation, decides which original prefix may be removed on replay.
func (s *Store) maintainCollectionExecutionRetirement(at time.Time) (bool, error) {
	s.fsm.mu.RLock()
	f := s.fsm
	if f.err != nil || f.bootstrapPending() || f.restorePending() || f.image.Authentication != nil && f.image.Authentication.ResetRequired {
		s.fsm.mu.RUnlock()
		return false, nil
	}
	if len(f.image.Collections) > maxCollectionOperations {
		s.fsm.mu.RUnlock()
		return false, ErrCollectionUnavailable
	}
	var selected CollectionState
	for _, head := range f.image.Collections {
		r := head.ExecutionResult
		if r == nil || !collectionTerminal(head.Phase) || !r.HistorySealed && r.HistoryExpiredAt.IsZero() || at.Before(r.Summary.FinalizedAt) ||
			!at.Before(r.Summary.FinalizedAt.AddDate(0, 0, 30)) && r.HistoryExpiredAt.IsZero() {
			continue
		}
		if head.validate() != nil {
			s.fsm.mu.RUnlock()
			return false, ErrCollectionUnavailable
		}
		if retirement := head.ExecutionRetirement; retirement != nil {
			if at.Before(retirement.UpdatedAt) || retirement.Sources != nil && at.Before(retirement.Sources.UpdatedAt) {
				continue
			}
		}
		if selected.ID == "" || head.ID < selected.ID {
			selected = head
		}
	}
	selected = selected.Clone()
	index, history := f.image.Index, f.history
	s.fsm.mu.RUnlock()
	if selected.ID == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Availability admission reads only the retained anchor/seal metadata. The
	// FSM replays committed facts without depending on current local retention.
	if selected.ExecutionResult.HistoryExpiredAt.IsZero() {
		receipt := collectionExecutionReceiptFor(*selected.ExecutionResult)
		if _, err := history.collectionExecutionPage(ctx, receipt, index, receipt.Descriptor.Count, 1, at); err != nil {
			if errors.Is(err, ErrOperationExpired) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return true, nil
			}
			return true, err
		}
	}
	command := CollectionExecuteCommand{Action: "retire", Binding: selected.ExecutionResult.Summary.Binding, Retirement: CollectionExecutionRetirementFenceFor(selected)}
	if selected.ExecutionRetirement != nil && selected.ExecutionRetirement.complete(selected) {
		v := collectionValidationReceiptFor(selected.Validation)
		if v == nil {
			return true, ErrCollectionUnavailable
		}
		if _, err := history.collectionValidationPage(ctx, *v, index, v.Descriptor.Count, 1, at); err != nil && !errors.Is(err, ErrOperationExpired) {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return true, nil
			}
			return true, err
		}
		command.Action, command.Retirement = "retire_sources", nil
		command.SourceRetirement = CollectionExecutionSourceRetirementFenceFor(selected)
	}
	results, err := s.Submit(ctx, []Command{{Kind: "collection_execute", CollectionExecute: &command, At: at}})
	if err != nil {
		if IsLeadershipUnavailable(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return true, nil
		}
		select {
		case <-s.stop:
			return true, nil
		default:
			return true, err
		}
	}
	if len(results) != 1 {
		return true, ErrCollectionUnavailable
	}
	r := results[0]
	if errors.Is(r.Err, ErrCollectionConflict) || errors.Is(r.Err, ErrOperationNotFound) || errors.Is(r.Err, ErrAuthenticationResetRequired) || errors.Is(r.Err, ErrBootstrapPending) {
		return true, nil
	}
	if r.Err != nil {
		return true, r.Err
	}
	if !r.Allowed {
		return true, ErrCollectionUnavailable
	}
	return true, nil
}
