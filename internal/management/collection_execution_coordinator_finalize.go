package management

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

type collectionExecutionFinalizationPending struct {
	command persistence.Command
	barrier bool
}

func (w *CollectionExecutionCoordinator) publishFinalization(condition string) {
	p := w.finalizing.command.CollectionExecute
	status := CollectionExecutionStatus{OperationID: p.Binding.OperationID, Condition: condition}
	if progress := p.Finalize.Progress; progress != nil {
		status.Processed, status.Accepted = progress.Processed, progress.Accepted
		status.PendingChildren = progress.Accepted - progress.ChildTerminals
	}
	w.mu.Lock()
	w.status = []CollectionExecutionStatus{status}
	w.mu.Unlock()
}

// finalizeNext owns one immutable command, including its first observation time.
// A missing work-list entry never releases an uncertain finalization: retry the
// exact command only after the FIFO prefix is confirmed, even if it already won.
func (w *CollectionExecutionCoordinator) finalizeNext(ctx context.Context) (bool, error) {
	if w.pending != nil {
		return false, nil
	}
	if w.finalizing != nil {
		if _, err := retryCollectionRead(ctx, w.pauseLeadership, func() (struct{}, error) {
			return struct{}{}, w.catalog.store.ControllerHealthContext(ctx)
		}); err != nil {
			return false, err
		}
	}
	if w.finalizing == nil {
		work, err := retryCollectionRead(ctx, w.pauseLeadership, func() ([]persistence.CollectionExecutionFinalizationWork, error) {
			return w.catalog.store.CollectionExecutionFinalizationWork(ctx, w.now())
		})
		if err != nil {
			return false, err
		}
		live := make(map[string]bool, len(work))
		for _, item := range work {
			live[item.OperationID] = true
		}
		for id := range w.finalizationRetryAt {
			if !live[id] {
				delete(w.finalizationRetryAt, id)
			}
		}
		for _, item := range work {
			at := w.now().UTC()
			if at.Before(item.EarliestAt) || time.Now().Before(w.finalizationRetryAt[item.OperationID]) {
				continue
			}
			fence := item.Fence.Clone()
			w.finalizing = &collectionExecutionFinalizationPending{command: persistence.Command{Kind: "collection_execute", At: at,
				CollectionExecute: &persistence.CollectionExecuteCommand{Action: "finalize", Binding: item.Binding, Finalize: &fence}}}
			break
		}
		if w.finalizing == nil {
			return false, nil
		}
	}
	w.publishFinalization("finalizing")
	err := w.submitFinalization(ctx)
	if err == nil {
		w.publishFinalization("finalized")
		delete(w.finalizationRetryAt, w.finalizing.command.CollectionExecute.Binding.OperationID)
		w.finalizing = nil
		return true, nil
	}
	switch {
	case persistence.IsLeadershipUnavailable(err):
		w.pauseLeadership(true)
		w.publishFinalization("leadershipUnavailable")
	case errors.Is(err, ErrOutcomeUnconfirmed):
		w.publishFinalization("commitUnconfirmed")
	case errors.Is(err, persistence.ErrCollectionQuota), errors.Is(err, persistence.ErrCatalogBusy):
		w.publishFinalization("finalizationBackpressure")
		if w.finalizationRetryAt == nil {
			w.finalizationRetryAt = make(map[string]time.Time)
		}
		w.finalizationRetryAt[w.finalizing.command.CollectionExecute.Binding.OperationID] = time.Now().Add(5 * time.Second)
		w.finalizing = nil // Confirmed rejection; allow other parents to progress.
	case errors.Is(err, persistence.ErrCollectionConflict):
		w.publishFinalization("viewChanged")
		w.finalizing = nil // A confirmed stop/progress race requires a fresh fence.
	default:
		return true, err
	}
	return true, nil
}

func (w *CollectionExecutionCoordinator) reconcileFinalizationBarrier(ctx context.Context) error {
	p := w.finalizing
	if p != nil && p.barrier {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := w.flush(bounded)
		cancel()
		if err != nil {
			return errors.Join(ErrOutcomeUnconfirmed, err)
		}
		p.barrier = false
	}
	return nil
}

func (w *CollectionExecutionCoordinator) submitFinalization(ctx context.Context) error {
	if err := w.reconcileFinalizationBarrier(ctx); err != nil {
		return err
	}
	p := w.finalizing
	if err := ctx.Err(); err != nil {
		return err
	}
	if !w.catalog.verified.Load() || w.catalog.failed.Load() {
		return ErrUnavailable
	}
	command := p.command
	results, err := w.submit(ctx, []persistence.Command{command})
	if err != nil || len(results) != 1 {
		p.barrier = true
		return errors.Join(ErrOutcomeUnconfirmed, err)
	}
	result := results[0]
	if result.Err != nil {
		return result.Err
	}
	if !result.Allowed || result.Collection == nil || result.Collection.ID != command.CollectionExecute.Binding.OperationID || result.Collection.ExecutionResult == nil {
		p.barrier = true
		return ErrOutcomeUnconfirmed
	}
	summary := result.Collection.ExecutionResult.Summary
	if summary.Binding != command.CollectionExecute.Binding || !sameFinalizationFence(summary.Fence, *command.CollectionExecute.Finalize) || summary.FinalizedAt.After(command.At) {
		p.barrier = true
		return ErrOutcomeUnconfirmed
	}
	return nil
}

func sameFinalizationFence(a, b persistence.CollectionExecutionFinalizeFence) bool {
	normalize := func(f persistence.CollectionExecutionFinalizeFence) persistence.CollectionExecutionFinalizeFence {
		f = f.Clone()
		f.TerminalAt = f.TerminalAt.UTC()
		if p := f.Progress; p != nil {
			p.StartedAt, p.LastAt = p.StartedAt.UTC(), p.LastAt.UTC()
			if p.Prepared != nil {
				p.Prepared.At = p.Prepared.At.UTC()
			}
		}
		return f
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}
