package persistence

import (
	"errors"
	"fmt"
)

// preparedCollectionChildCommit belongs to one uninterrupted machine critical
// section. It captures all fallible work before any receipt or reservation is
// removed. In particular, it must never be hidden in deleteOperation.
type preparedCollectionChildCommit struct {
	records  []collectionExecutionRecord
	parents  map[string]CollectionState
	trees    map[string]*collectionExecutionTerminalTree
	children []string
}

func collectionCatalogTerminals(p *preparedCatalogMutation) []OperationReceipt {
	if p == nil || p.supersededID == "" {
		return nil
	}
	for _, event := range p.result.Events {
		if event.Operation != nil && event.Operation.ID == p.supersededID {
			return []OperationReceipt{*event.Operation}
		}
	}
	return nil
}

// prepareCollectionChildCommit reads only immutable accepted rows. A link is a
// lookup hint: the authoritative original outcome, pending receipt, parent
// binding and commitment are all checked before a terminal is prepared.
func (f *machine) prepareCollectionChildCommit(receipts []OperationReceipt) (*preparedCollectionChildCommit, error) {
	p := &preparedCollectionChildCommit{parents: make(map[string]CollectionState), trees: make(map[string]*collectionExecutionTerminalTree)}
	if len(receipts) == 0 {
		return p, nil
	}
	// An unbuilt/cleared link cache must never look like a fleet without
	// children. The bounded parent headers provide its authoritative size.
	var pending uint64
	for _, s := range f.image.Collections {
		if s.Execution == nil {
			continue
		}
		if s.Execution.validateState(s) != nil {
			return nil, ErrCollectionInvalid
		}
		pending += s.Execution.Accepted - s.Execution.ChildTerminals
		if pending > maxPendingCatalogOperations {
			return nil, ErrCollectionInvalid
		}
	}
	if pending != uint64(len(f.collectionChildren)) {
		return nil, ErrCollectionInvalid
	}
	if pending == 0 {
		return p, nil
	}
	if f.collections == nil || len(f.collectionChildren) > maxPendingCatalogOperations {
		return nil, ErrCollectionUnavailable
	}
	seen := make(map[string]bool)
	inputs := make([]collectionChildTerminalInput, 0, min(len(receipts), collectionLedgerBatchLimit))
	for _, receipt := range receipts {
		link, linked := f.collectionChildren[receipt.ID]
		if !linked {
			continue
		}
		if seen[receipt.ID] || len(inputs) == collectionLedgerBatchLimit {
			return nil, ErrCollectionInvalid
		}
		seen[receipt.ID] = true
		s, exists := p.parents[link.Binding.OperationID]
		if !exists {
			s, exists = f.image.Collections[link.Binding.OperationID]
			if !exists || s.Execution == nil || s.Execution.validateState(s) != nil {
				return nil, ErrCollectionInvalid
			}
			s = s.Clone()
			stats, err := f.collections.ExecutionStats(s.ID)
			if err != nil || stats.Binding != s.Execution.Binding || stats.Outcomes != s.Execution.Processed ||
				stats.Terminals != s.Execution.ChildTerminals || stats.Prepared != (s.Execution.Prepared != nil) ||
				stats.EncodedBytes != s.Execution.EncodedBytes || stats.ChargedBytes != s.Execution.ChargedBytes ||
				stats.TerminalBytes != s.Execution.TerminalBytes || stats.TerminalCapacity != int64(s.Execution.Accepted)*collectionChildTerminalReserve {
				return nil, errors.Join(err, ErrCollectionInvalid)
			}
		}
		if s.Execution.Binding != link.Binding || link.Ordinal > s.Execution.Processed {
			return nil, ErrCollectionInvalid
		}
		raw, found, err := f.collections.ExecutionRecord(s.ID, collectionExecutionOutcomeSlot(link.Ordinal))
		if err != nil || !found || raw.Outcome == nil {
			return nil, errors.Join(err, ErrCollectionInvalid)
		}
		accepted := *raw.Outcome
		certified := f.collectionOutcomeCommitments[s.ID]
		if !certified.matches(s.Execution.Binding, s.Execution.Processed, s.Execution.OutcomeDigest) || !certified.matchesRecord(raw) {
			return nil, ErrCollectionInvalid
		}
		pending, found := f.image.Operations[receipt.ID]
		if !found {
			return nil, ErrCollectionInvalid
		}
		original, err := collectionChildLinkFor(accepted, pending)
		if err != nil || original != link {
			return nil, errors.Join(err, ErrCollectionInvalid)
		}
		record, err := prepareCollectionChildTerminal(link, accepted, receipt)
		if err != nil {
			return nil, err
		}
		// A still-pending authoritative receipt cannot already have terminal
		// materialization. Ahead or contradictory ledger state is not a retry.
		if _, found, err := f.collections.ExecutionRecord(s.ID, fmt.Sprintf("terminal/%016x", link.Ordinal)); err != nil || found {
			return nil, errors.Join(err, ErrCollectionInvalid)
		}
		tree := p.trees[s.ID]
		if tree == nil {
			tree = f.collectionTerminalTrees[s.ID].Clone()
			if tree == nil && s.Execution.ChildTerminals == 0 {
				tree, err = newCollectionExecutionTerminalTree(s.Execution.ItemCount)
				if err != nil {
					return nil, err
				}
			}
		}
		if tree == nil || tree.Root() != s.Execution.TerminalRoot {
			return nil, ErrCollectionInvalid
		}
		proof, err := tree.Proof(link.Ordinal)
		if err != nil {
			return nil, err
		}
		next, err := s.Execution.withTerminal(accepted, *record.Terminal, proof)
		if err != nil {
			return nil, err
		}
		if err := tree.Insert(*record.Terminal); err != nil || tree.Root() != next.TerminalRoot {
			return nil, errors.Join(err, ErrCollectionInvalid)
		}
		s.Execution = &next
		if s.validate() != nil {
			return nil, ErrCollectionInvalid
		}
		p.parents[s.ID], p.trees[s.ID] = s, tree
		p.children = append(p.children, receipt.ID)
		inputs = append(inputs, collectionChildTerminalInput{Link: link, Accepted: accepted, Terminal: receipt})
	}
	var err error
	p.records, err = prepareCollectionChildTerminals(inputs)
	return p, err
}

func (f *machine) installCollectionChildCommit(p *preparedCollectionChildCommit) {
	for id, state := range p.parents {
		f.image.Collections[id] = state
	}
	if len(p.trees) != 0 && f.collectionTerminalTrees == nil {
		f.collectionTerminalTrees = make(map[string]*collectionExecutionTerminalTree)
	}
	for id, tree := range p.trees {
		f.collectionTerminalTrees[id] = tree
	}
	for _, id := range p.children {
		delete(f.collectionChildren, id)
	}
}

// persistCollectionChildTerminals is used before an ordinary completion,
// supersession or explicit restore retires a receipt. Atomic item acceptance
// combines p.records with its own outcome in ONE ledger transaction instead.
func (f *machine) persistCollectionChildTerminals(receipts []OperationReceipt) error {
	p, err := f.prepareCollectionChildCommit(receipts)
	if err != nil {
		return err
	}
	if len(p.records) == 0 {
		return nil
	}
	if err := f.collections.ApplyExecutionBatch(p.records); err != nil {
		return err
	}
	f.installCollectionChildCommit(p)
	return nil
}
