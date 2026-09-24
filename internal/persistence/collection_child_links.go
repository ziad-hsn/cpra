package persistence

import (
	"context"
	"time"
)

// collectionChildLink is disposable derived metadata, never authority or an
// execution grant. The accepted row and ordinary receipt remain authoritative.
// Callers hold the machine lock while preparing/installing a link or terminal.
// No helper in this file changes the machine, ledger, history or supplied maps.
type collectionChildLink struct {
	Binding       CollectionExecutionBinding
	Ordinal       uint64
	RowDigest     string
	ChildID       string
	Key           CatalogKey
	UID, Revision string
}

type collectionChildLinks map[string]collectionChildLink

func collectionChildLinkFor(accepted CollectionItemOutcome, pending OperationReceipt) (collectionChildLink, error) {
	if accepted.validate() != nil || accepted.Decision != "accepted" || accepted.Receipt == nil || pending.validate() != nil || pending.Subject != "" || pending.State != "committed" || !collectionChildReceiptsEqual(*accepted.Receipt, pending) {
		return collectionChildLink{}, ErrCollectionInvalid
	}
	return collectionChildLink{Binding: accepted.Binding, Ordinal: accepted.Ordinal, RowDigest: accepted.RowDigest, ChildID: pending.ID, Key: accepted.Key, UID: accepted.UID, Revision: accepted.Revision}, nil
}

// prepareCollectionChildLink validates a bounded insertion without installing
// it. A duplicate original link can be reconciled even when the index is full;
// a different original parent/item cannot take ownership of an existing child.
func prepareCollectionChildLink(links collectionChildLinks, accepted CollectionItemOutcome, pending OperationReceipt) (collectionChildLink, error) {
	if len(links) > maxPendingCatalogOperations {
		return collectionChildLink{}, ErrCatalogBusy
	}
	link, err := collectionChildLinkFor(accepted, pending)
	if err != nil {
		return collectionChildLink{}, err
	}
	if old, exists := links[link.ChildID]; exists {
		if old != link {
			return collectionChildLink{}, ErrCollectionConflict
		}
		return link, nil
	}
	if len(links) == maxPendingCatalogOperations {
		return collectionChildLink{}, ErrCatalogBusy
	}
	return link, nil
}

func collectionChildReceiptsEqual(a, b OperationReceipt) bool {
	// time.Time can retain a location/monotonic representation which is not part
	// of the durable observation. Every other receipt field is exact.
	if !a.At.Equal(b.At) || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	a.At, b.At = time.Time{}, time.Time{}
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return a == b
}

// prepareCollectionChildTerminal prepares the compact immutable observation
// before applyOperation, supersession or explicit restore deletes the pending
// ordinary receipt. The caller must atomically store it before installing that
// in-memory deletion. A retained link alone cannot stand in for the original
// accepted row, whose exact receipt supplies all immutable terminal metadata.
func prepareCollectionChildTerminal(link collectionChildLink, accepted CollectionItemOutcome, terminal OperationReceipt) (collectionExecutionRecord, error) {
	if accepted.Receipt == nil {
		return collectionExecutionRecord{}, ErrCollectionInvalid
	}
	expected, err := collectionChildLinkFor(accepted, *accepted.Receipt)
	if err != nil || expected != link {
		return collectionExecutionRecord{}, ErrCollectionConflict
	}
	observation, err := collectionChildObservationFor(accepted, terminal)
	if err != nil {
		return collectionExecutionRecord{}, err
	}
	record := collectionExecutionRecord{Version: collectionExecutionRecordVersion, Terminal: &observation}
	if _, err := collectionExecutionEncoding(record); err != nil {
		return collectionExecutionRecord{}, err
	}
	return record, nil
}

type collectionChildTerminalInput struct {
	Link     collectionChildLink
	Accepted CollectionItemOutcome
	Terminal OperationReceipt
}

// prepareCollectionChildTerminals is all-or-nothing preparation, including
// children of different parents. It performs no I/O and retains no input slice
// or receipt pointers. Duplicate children are rejected before any install.
func prepareCollectionChildTerminals(inputs []collectionChildTerminalInput) ([]collectionExecutionRecord, error) {
	if len(inputs) > collectionLedgerBatchLimit {
		return nil, errCollectionLedgerQuota
	}
	records := make([]collectionExecutionRecord, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	var encoded int
	for _, input := range inputs {
		if _, exists := seen[input.Link.ChildID]; exists {
			return nil, ErrCollectionConflict
		}
		record, err := prepareCollectionChildTerminal(input.Link, input.Accepted, input.Terminal)
		if err != nil {
			return nil, err
		}
		raw, err := collectionExecutionEncoding(record)
		if err != nil {
			return nil, err
		}
		if len(raw) > collectionLedgerBatchBytes-encoded {
			return nil, errCollectionLedgerQuota
		}
		encoded += len(raw)
		seen[input.Link.ChildID] = struct{}{}
		records = append(records, record)
	}
	return records, nil
}

// buildCollectionChildLinks reconstructs only unresolved children. The caller
// supplies one validated image and its same-index frozen ledger, holds their
// ownership stable, and closes the view before any following ledger write.
// The returned index retains neither ciphertext, a read transaction, nor all
// historical outcomes. An outcome's terminal is looked up while WalkExecution
// already holds the view lock, avoiding public-method lock reentry and avoiding
// an unbounded provisional map while outcomes precede terminals in the stream.
// Parent progress commitments are validated by the surrounding snapshot/FSM
// integration; this helper enforces the original parent and pending-receipt
// links. It does not require a live parent/current epoch: canceled or explicitly
// invalidated parents must preserve unresolved child evidence too.
func buildCollectionChildLinks(ctx context.Context, i image, view *collectionLedgerView) (collectionChildLinks, error) {
	if ctx == nil || view == nil {
		return nil, ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(i.Operations)+len(i.OperationReservations) > maxPendingCatalogOperations {
		return nil, ErrCatalogBusy
	}
	links := make(collectionChildLinks)
	err := view.WalkExecution(ctx, func(record collectionExecutionRecord) error {
		binding := collectionExecutionBinding(record)
		state, exists := i.Collections[binding.OperationID]
		if !exists || state.ID != binding.OperationID {
			return errCollectionLedgerCorrupt
		}
		original, err := collectionExecutionBindingFor(state)
		if err != nil || original != binding || state.Activation.PlanID != binding.PlanID || state.Activation.PlanDescriptor != state.Plan.Descriptor {
			return errCollectionLedgerCorrupt
		}
		ordinal := uint64(0)
		switch {
		case record.Prepared != nil:
			ordinal = record.Prepared.Ordinal
		case record.Outcome != nil:
			ordinal = record.Outcome.Ordinal
		case record.Terminal != nil:
			ordinal = record.Terminal.Ordinal
		}
		if ordinal == 0 || ordinal > state.ItemCount {
			return errCollectionLedgerCorrupt
		}
		if retired := state.ExecutionRetirement; retired != nil {
			if retired.validateState(state) != nil || state.Execution == nil || retired.Checkpoint == nil ||
				record.Prepared != nil && retired.PreparedRemoved ||
				record.Prepared == nil && (ordinal <= retired.Checkpoint.Progress.Processed || ordinal > state.Execution.Processed) {
				return errCollectionLedgerCorrupt
			}
		}
		if record.Outcome == nil || record.Outcome.Receipt == nil {
			return nil
		}
		accepted := *record.Outcome
		childID := accepted.Receipt.ID
		if accepted.At.Before(state.Activation.At) {
			return errCollectionLedgerCorrupt
		}
		if _, reserved := i.OperationReservations[childID]; reserved {
			return errCollectionLedgerCorrupt
		}
		raw, err := view.encodedExecution(binding.OperationID, collectionExecutionTerminalSlot(accepted.Ordinal))
		if err != nil {
			return err
		}
		pending, present := i.Operations[childID]
		if raw != nil {
			terminal, err := decodeExecutionAt(binding.OperationID, collectionExecutionTerminalSlot(accepted.Ordinal), raw)
			if err != nil || terminal.Terminal == nil || !terminal.Terminal.matches(accepted) || present {
				return errCollectionLedgerCorrupt
			}
			return nil
		}
		if state.ExecutionRetirement != nil {
			return errCollectionLedgerCorrupt // A retired parent cannot regain an unresolved child.
		}
		if !present {
			return errCollectionLedgerCorrupt
		}
		if _, duplicate := links[childID]; duplicate {
			return errCollectionLedgerCorrupt
		}
		link, err := prepareCollectionChildLink(links, accepted, pending)
		if err != nil {
			return errCollectionLedgerCorrupt
		}
		links[childID] = link
		return nil
	})
	if err != nil {
		return nil, err
	}
	return links, nil
}

func collectionExecutionTerminalSlot(ordinal uint64) string {
	return "terminal/" + collectionExecutionOutcomeSlot(ordinal)[len("outcome/"):]
}
