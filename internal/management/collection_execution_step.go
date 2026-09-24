package management

import (
	"context"
	"errors"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// A pending command owns the only uncommitted encrypted candidate. It survives
// backpressure and uncertain replies without creating a new identity. It is
// single-owner state; status snapshots never expose it.
type collectionExecutionPending struct {
	command persistence.CollectionExecuteCommand
	barrier bool
}

func (w *CollectionExecutionCoordinator) clearPending() {
	if w.pending != nil && w.pending.command.Prepared != nil {
		clearStagedRecord(&w.pending.command.Prepared.Record)
	}
	w.pending = nil
}

func (w *CollectionExecutionCoordinator) prepareStep(ctx context.Context, item persistence.CollectionExecutionWork) error {
	command := persistence.CollectionExecuteCommand{Binding: item.Binding, CapabilitiesDigest: w.profile}
	if !item.Begun {
		command.Action = "begin"
	} else if item.Prepared != nil {
		// Preserve the exact retained candidate, but recheck a declared file
		// profile against its original input before deciding old staged work.
		// Unprofiled retries continue to require no encryption key access.
		p, err := newCollectionCandidatePreparation(ctx, w.catalog, item.OperationID, item.Actor, func(key persistence.CatalogKey) bool { return supportedKind(key.Kind) }, w.now)
		if err != nil {
			return err
		}
		defer p.close()
		if err := p.validateFileProfile(ctx, item.Prepared.Ordinal); err != nil {
			return err
		}
		command.Action, command.Ordinal, command.PreparedID = "decide", item.Prepared.Ordinal, item.Prepared.ID
	} else {
		p, err := newCollectionCandidatePreparation(ctx, w.catalog, item.OperationID, item.Actor, func(key persistence.CatalogKey) bool { return supportedKind(key.Kind) }, w.now)
		if err != nil {
			return err
		}
		defer p.close()
		row, _, err := p.view.Row(ctx, item.NextOrdinal, w.now())
		if err != nil {
			return err
		}
		command.Action, command.Ordinal = "decide", row.Ordinal
		if row.Change == "unchanged" {
			if err := p.validateFileProfile(ctx, row.Ordinal); err != nil {
				return err
			}
		} else {
			candidate, committed, err := p.prepare(ctx, row.Ordinal)
			switch {
			case errors.Is(err, persistence.ErrCatalogConflict):
				// Only an observed original-target conflict permits this path.
				// The FSM independently proves it before recording any outcome.
			case err != nil:
				return err
			case committed:
				command.PreparedID = candidate.ID
				clearStagedRecord(&candidate.Record)
			default:
				command.Action, command.Prepared = "prepare", &candidate
			}
		}
	}
	w.pending = &collectionExecutionPending{command: command}
	return nil
}

// reconcileBarrier must succeed before interpreting absence or releasing an
// uncertain candidate. Store.Flush joins the FIFO submission prefix, including
// a write whose original caller stopped waiting before its result was known.
func (w *CollectionExecutionCoordinator) reconcileBarrier(ctx context.Context) error {
	if w.pending == nil || !w.pending.barrier {
		return nil
	}
	barrierContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.flush(barrierContext); err != nil {
		return errors.Join(ErrOutcomeUnconfirmed, err)
	}
	w.pending.barrier = false
	return nil
}

func (w *CollectionExecutionCoordinator) submitStep(ctx context.Context, actor string) error {
	if err := w.reconcileBarrier(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !w.catalog.verified.Load() || w.catalog.failed.Load() {
		return ErrUnavailable
	}
	at := w.now().UTC()
	authority, err := w.catalog.store.ObserveOperatorAuthority(ctx, actor, at)
	if err != nil {
		return err
	}
	if !w.catalog.verified.Load() || w.catalog.failed.Load() {
		return ErrUnavailable
	}
	command := w.pending.command
	command.Authority = authority // Refresh authority, never the original intent.
	results, err := w.submit(ctx, []persistence.Command{{Kind: "collection_execute", At: at, CollectionExecute: &command}})
	if err != nil || len(results) != 1 {
		// Conservatively reconcile even errors without the admission sentinel.
		// No retry, fresh identity or absence read occurs before this barrier.
		w.pending.barrier = true
		return errors.Join(ErrOutcomeUnconfirmed, err)
	}
	result := results[0]
	if result.Err != nil {
		return result.Err
	}
	if !result.Allowed || result.Collection == nil || result.Collection.ID != command.Binding.OperationID || result.Collection.Execution == nil || result.Collection.Execution.Binding != command.Binding {
		w.pending.barrier = true
		return ErrOutcomeUnconfirmed
	}
	w.clearPending()
	return nil
}
