package persistence

import (
	"context"
	"slices"
	"strings"
	"time"
)

// CollectionExecutionFinalizationWork observes settled original facts. It is
// neither an execution grant nor evidence that retained item results exist.
type CollectionExecutionFinalizationWork struct {
	OperationID string
	Binding     CollectionExecutionBinding
	Fence       CollectionExecutionFinalizeFence
	EarliestAt  time.Time
}

// CollectionExecutionFinalizationWork inspects at most 64 retained headers.
// It performs no ledger/history I/O and needs no actor or capability observation.
// Old-epoch stopped parents may retain known results but cannot resume execution.
func (s *Store) CollectionExecutionFinalizationWork(ctx context.Context, at time.Time) ([]CollectionExecutionFinalizationWork, error) {
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return nil, ErrCollectionInvalid
	}
	if s == nil {
		return nil, ErrCollectionUnavailable
	}
	unlock, err := s.lockStateContext(ctx, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := s.collectionCoordinatorHealth(); err != nil {
		return nil, err
	}
	f := s.fsm
	if len(f.image.Collections) > maxCollectionOperations {
		return nil, ErrCollectionUnavailable
	}
	work := make([]CollectionExecutionFinalizationWork, 0, len(f.image.Collections))
	for id, head := range f.image.Collections {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if head.Activation == nil {
			continue
		}
		epoch, sequence, err := ParseOperationHandle(id)
		if err != nil || head.ID != id || !collectionActivationStorageFormat(f.image.Version) ||
			head.Execution != nil && !collectionExecutionStorageFormat(f.image.Version) || head.validate() != nil ||
			epoch == f.image.OperationEpoch && sequence > f.image.OperationHighWater {
			return nil, ErrCollectionUnavailable
		}
		if head.ExecutionResult != nil || head.Phase == "applying" && epoch != f.image.OperationEpoch {
			continue
		}
		fence := CollectionExecutionFinalizeFenceFor(head)
		if fence == nil {
			return nil, ErrCollectionUnavailable
		}
		if _, eligible := collectionExecutionOutcome(*fence); !eligible {
			continue
		}
		binding, err := collectionExecutionBindingFor(head)
		if err != nil {
			return nil, ErrCollectionUnavailable
		}
		earliest := head.Activation.At
		if head.TerminalAt.After(earliest) {
			earliest = head.TerminalAt
		}
		if head.Execution != nil && head.Execution.LastAt.After(earliest) {
			earliest = head.Execution.LastAt
		}
		work = append(work, CollectionExecutionFinalizationWork{OperationID: id, Binding: binding, Fence: *fence, EarliestAt: earliest})
	}
	slices.SortFunc(work, func(a, b CollectionExecutionFinalizationWork) int {
		if order := a.EarliestAt.Compare(b.EarliestAt); order != 0 {
			return order
		}
		return strings.Compare(a.OperationID, b.OperationID)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return work, nil
}
