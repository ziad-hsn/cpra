package persistence

import (
	"context"
	"errors"
	"slices"
	"time"
)

// These are logical allocation ceilings, not RSS limits. Identity arrays are
// charged from declared counts before any rows are read; only one 32 MiB plan
// audit index is retained at a time. Outcome commitments are separately bounded.
const collectionExecutionIdentityAuditBytes = 10 << 20

type collectionExecutionRecovery struct {
	children    collectionChildLinks
	trees       map[string]*collectionExecutionTerminalTree
	commitments map[string]*collectionExecutionOutcomeCommitments
}

type collectionExecutionParentRecovery struct {
	progress    CollectionExecutionProgress
	tree        *collectionExecutionTerminalTree
	commitments *collectionExecutionOutcomeCommitments
}

// validateCollectionExecutionInventory audits one immutable image and its
// same-index frozen ledger. It does not observe current authorization/catalog
// state, repair evidence, enable a storage format or grant execution. The
// Existing outer input/plan/result namespace validators remain mandatory; in
// particular validateCollectionValidationRows verifies the sealed result rows.
// The caller closes the view BEFORE any ledger write; returned caches retain no
// transaction, ciphertext, original resources, or full guard arrays.
func validateCollectionExecutionInventory(ctx context.Context, i image, view *collectionLedgerView) (*collectionExecutionRecovery, error) {
	if ctx == nil || view == nil {
		return nil, ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(i.Collections) > maxCollectionOperations {
		return nil, ErrCollectionQuota
	}
	counts := make(map[string]uint64)
	parents := make([]string, 0, len(i.Collections))
	var accepted uint64
	var commitmentsBytes int64
	for id, s := range i.Collections {
		if id != s.ID {
			return nil, ErrCollectionInvalid
		}
		if s.ExecutionRetirement != nil {
			if err := collectionExecutionRetiredRecoveryHeader(i, s); err != nil {
				return nil, err
			}
			if s.Execution == nil || s.ExecutionRetirement.Sources != nil {
				parents = append(parents, id)
				continue
			}
		}
		if s.Execution == nil {
			if s.ExecutionResult != nil && s.ExecutionResult.Published != 0 {
				x, err := buildCollectionExecutionAuditIndex(ctx, s, view, defaultCollectionExecutionIndexLimits())
				if err != nil {
					return nil, err
				}
				if err := validateCollectionExecutionPublishedPrefix(ctx, s, view, x, nil); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := collectionExecutionRecoveryHeader(i, s); err != nil {
			return nil, err
		}
		epoch, _, _ := ParseOperationHandle(id)
		remainingAccepted := s.Execution.Accepted
		if s.ExecutionRetirement != nil {
			remainingAccepted -= s.ExecutionRetirement.Checkpoint.Progress.Accepted
		} else {
			cost, err := collectionExecutionCommitmentCost(s.Execution.Processed)
			if err != nil || cost > collectionExecutionCommitmentAuditBytes-commitmentsBytes {
				return nil, ErrCollectionQuota
			}
			commitmentsBytes += cost
		}
		accepted += remainingAccepted
		if accepted > uint64(collectionExecutionIdentityAuditBytes/16) {
			return nil, ErrCollectionQuota
		}
		counts[epoch] += remainingAccepted
		parents = append(parents, id)
	}
	// Two uint64 arrays per accepted row (child sequence and catalog token).
	sequences := make(map[string][]uint64, len(counts))
	for epoch, n := range counts {
		sequences[epoch] = make([]uint64, 0, int(n))
	}
	tokens := make([]uint64, 0, int(accepted))
	slices.Sort(parents)
	// Verify the entire namespace, including orphan rows and exact accounting,
	// before a per-parent point traversal. No callback reenters this view lock.
	if err := view.WalkExecution(ctx, func(r collectionExecutionRecord) error {
		b := collectionExecutionBinding(r)
		s, ok := i.Collections[b.OperationID]
		if !ok || s.Execution == nil || s.Execution.Binding != b {
			return ErrCollectionInvalid
		}
		retired := uint64(0)
		if retirement := s.ExecutionRetirement; retirement != nil {
			retired = retirement.Checkpoint.Progress.Processed
			if retirement.PreparedRemoved && r.Prepared != nil {
				return ErrCollectionInvalid
			}
		}
		switch {
		case r.Prepared != nil:
			if err := validateCatalogRecordImageExtensions(i, r.Prepared.Record, true); err != nil {
				return err
			}
			if s.Execution.Prepared == nil || r.Prepared.Ordinal != s.Execution.Processed+1 {
				return ErrCollectionInvalid
			}
		case r.Outcome != nil:
			if r.Outcome.Ordinal <= retired || r.Outcome.Ordinal > s.Execution.Processed {
				return ErrCollectionInvalid
			}
		case r.Terminal != nil:
			if r.Terminal.Ordinal <= retired || r.Terminal.Ordinal > s.Execution.Processed {
				return ErrCollectionInvalid
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result := &collectionExecutionRecovery{trees: make(map[string]*collectionExecutionTerminalTree, len(parents)), commitments: make(map[string]*collectionExecutionOutcomeCommitments, len(parents))}
	for _, id := range parents {
		s := i.Collections[id]
		observe := func(o CollectionItemOutcome) error {
			if o.Receipt == nil {
				return nil
			}
			epoch, sequence, _ := ParseOperationHandle(o.Receipt.ID)
			if len(sequences[epoch]) == cap(sequences[epoch]) || len(tokens) == cap(tokens) {
				return ErrCollectionInvalid
			}
			sequences[epoch] = append(sequences[epoch], sequence)
			tokens = append(tokens, o.MutationSequence)
			return nil
		}
		if s.ExecutionRetirement != nil {
			if err := validateCollectionExecutionRetiredParent(ctx, i, s, view, observe); err != nil {
				return nil, err
			}
			continue // Retired terminal parents must never install executor caches.
		}
		p, err := validateCollectionExecutionParent(ctx, i, s, view, observe)
		if err != nil {
			return nil, err
		}
		result.trees[id], result.commitments[id] = p.tree, p.commitments
	}
	for epoch, values := range sequences {
		if uint64(len(values)) != counts[epoch] {
			return nil, ErrCollectionInvalid
		}
		slices.Sort(values)
		for n := 1; n < len(values); n++ {
			if values[n] == values[n-1] {
				return nil, ErrCollectionInvalid
			}
		}
	}
	if uint64(len(tokens)) != accepted {
		return nil, ErrCollectionInvalid
	}
	slices.Sort(tokens)
	for n := 1; n < len(tokens); n++ {
		if tokens[n] == tokens[n-1] {
			return nil, ErrCollectionInvalid
		}
	}
	var err error
	result.children, err = buildCollectionChildLinks(ctx, i, view)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func collectionExecutionRecoveryHeader(i image, s CollectionState) error {
	if s.Execution == nil || s.validate() != nil || s.Execution.validateState(s) != nil || !collectionExecutionArtifactsComplete(s) || s.Plan.Header.ObservedIndex > i.Index {
		return ErrCollectionInvalid
	}
	return collectionExecutionRecoveryIdentity(i, s)
}

func collectionExecutionRecoveryIdentity(i image, s CollectionState) error {
	epoch, sequence, err := ParseOperationHandle(s.ID)
	resetting := i.Restore != nil && i.Restore.Phase != "complete"
	if err != nil || !resetting && (epoch == i.OperationEpoch && sequence > i.OperationHighWater || collectionLive(s.Phase) && epoch != i.OperationEpoch) {
		return ErrCollectionInvalid
	}
	if _, ok := i.Operations[s.ID]; ok {
		return ErrCollectionInvalid
	}
	if _, ok := i.OperationReservations[s.ID]; ok {
		return ErrCollectionInvalid
	}
	return nil
}

// validateCollectionExecutionParent verifies one complete original artifact and
// reconstructs its header without requiring consumed preparation records. It
// does not establish cross-parent uniqueness; the inventory wrapper does that.
// The caller separately verifies original result rows against their sealed
// descriptor. observe is a provisional audit callback, not an installation hook.
func validateCollectionExecutionParent(ctx context.Context, i image, s CollectionState, view *collectionLedgerView, observe func(CollectionItemOutcome) error) (*collectionExecutionParentRecovery, error) {
	if ctx == nil || view == nil || s.ExecutionRetirement != nil {
		return nil, ErrCollectionInvalid
	}
	if err := collectionExecutionRecoveryHeader(i, s); err != nil {
		return nil, err
	}
	x, err := buildCollectionExecutionAuditIndex(ctx, s, view, defaultCollectionExecutionIndexLimits())
	if err != nil {
		return nil, err
	}
	proof, err := rebuildCollectionExecutionParent(ctx, i, s, view, observe, x)
	if err != nil {
		return nil, err
	}
	if err := validateCollectionExecutionPublishedPrefix(ctx, s, view, x, proof); err != nil {
		return nil, err
	}
	return proof, nil
}

// validateCollectionExecutionParentWithIndex reuses the caller's fully verified
// normal original index, avoiding simultaneous retention of two 32 MiB indexes.
// The index, header and ledger must describe the same frozen generation. Audit
// indexes cannot enter this path because matches rejects them permanently.
func validateCollectionExecutionParentWithIndex(ctx context.Context, i image, s CollectionState, view *collectionLedgerView, observe func(CollectionItemOutcome) error, x *collectionExecutionIndex) (*collectionExecutionParentRecovery, error) {
	if ctx == nil || view == nil || s.ExecutionRetirement != nil || !x.matches(s) {
		return nil, ErrCollectionInvalid
	}
	if err := collectionExecutionRecoveryHeader(i, s); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	proof, err := rebuildCollectionExecutionParent(ctx, i, s, view, observe, x)
	if err != nil {
		return nil, err
	}
	if err := validateCollectionExecutionPublishedPrefix(ctx, s, view, x, proof); err != nil {
		return nil, err
	}
	return proof, nil
}

func rebuildCollectionExecutionParent(ctx context.Context, i image, s CollectionState, view *collectionLedgerView, observe func(CollectionItemOutcome) error, x *collectionExecutionIndex) (*collectionExecutionParentRecovery, error) {
	actual, err := NewCollectionExecutionProgress(s.Execution.Binding, s.ItemCount, s.Activation.At)
	if err != nil {
		return nil, err
	}
	cache, err := newCollectionExecutionOutcomeCommitments(actual.Binding)
	if err != nil {
		return nil, err
	}
	for ordinal := uint64(1); ordinal <= s.Execution.Processed; ordinal++ {
		r, raw, err := collectionExecutionRecoveryRecord(ctx, view, s.ID, collectionExecutionOutcomeSlot(ordinal))
		if err != nil {
			return nil, err
		}
		if r.Outcome == nil {
			return nil, ErrCollectionInvalid
		}
		o := *r.Outcome
		row, ok := x.row(ordinal)
		if !ok || !collectionExecutionOutcomeOriginal(i, s, row, o) {
			return nil, ErrCollectionInvalid
		}
		// Rebuild the authoritative chain directly. withOutcome is a live transition
		// requiring the historical prepared slot, already consumed in this image.
		actual.Processed++
		switch o.Decision {
		case "accepted":
			actual.Accepted++
		case "unchanged":
			actual.Unchanged++
		case "conflict":
			actual.Conflicts++
		case "dependencyBlocked":
			actual.DependencyBlocked++
		}
		actual.OutcomeBytes += int64(len(raw))
		actual.EncodedBytes += int64(len(raw))
		actual.ChargedBytes += collectionExecutionCharge(r, raw)
		actual.OutcomeDigest = collectionExecutionOutcomeNextDigest(actual.OutcomeDigest, raw)
		if o.At.After(actual.LastAt) {
			actual.LastAt = o.At
		}
		cache, err = cache.append(o)
		if err != nil {
			return nil, err
		}
		if observe != nil {
			if err := observe(o); err != nil {
				return nil, err
			}
		}
	}
	r, _, err := collectionExecutionRecoveryRecord(ctx, view, s.ID, "prepared")
	if err != nil {
		return nil, err
	}
	if (r.Prepared != nil) != (s.Execution.Prepared != nil) {
		return nil, ErrCollectionInvalid
	}
	if r.Prepared != nil {
		p := *r.Prepared
		row, ok := x.row(p.Ordinal)
		if !ok || p.Binding != actual.Binding || p.Ordinal != actual.Processed+1 || p.InputOrdinal != row.Row.InputOrdinal || p.RowDigest != row.RowDigest || p.Record.Key != row.Row.Key || !collectionExecutionDesiredTuple(row.Row, p.Record.UID, p.Record.Revision, p.Record.Generation) {
			return nil, ErrCollectionInvalid
		}
		actual, err = actual.withPrepared(p)
		if err != nil {
			return nil, err
		}
	}
	tree, err := newCollectionExecutionTerminalTree(s.ItemCount)
	if err != nil {
		return nil, err
	}
	for ordinal := uint64(1); ordinal <= actual.Processed; ordinal++ {
		r, _, err := collectionExecutionRecoveryRecord(ctx, view, s.ID, collectionExecutionTerminalSlot(ordinal))
		if err != nil {
			return nil, err
		}
		if r.Terminal == nil {
			continue
		}
		accepted, _, err := collectionExecutionRecoveryRecord(ctx, view, s.ID, collectionExecutionOutcomeSlot(ordinal))
		if err != nil || accepted.Outcome == nil {
			return nil, errors.Join(ErrCollectionInvalid, err)
		}
		proof, err := tree.Proof(ordinal)
		if err != nil {
			return nil, err
		}
		actual, err = actual.withTerminal(*accepted.Outcome, *r.Terminal, proof)
		if err != nil {
			return nil, err
		}
		if err := tree.Insert(*r.Terminal); err != nil {
			return nil, err
		}
	}
	if !collectionExecutionProgressEqual(actual, *s.Execution) || !cache.matches(actual.Binding, actual.Processed, actual.OutcomeDigest) || tree.Root() != actual.TerminalRoot {
		return nil, ErrCollectionInvalid
	}
	if err := collectionExecutionRecoveryStats(ctx, view, s.ID, actual); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &collectionExecutionParentRecovery{progress: actual, tree: tree, commitments: cache}, nil
}

func collectionExecutionOutcomeOriginal(i image, s CollectionState, row collectionExecutionRow, o CollectionItemOutcome) bool {
	r := row.Row
	if o.validate() != nil || o.Binding != s.Execution.Binding || o.Ordinal != r.Ordinal || o.InputOrdinal != r.InputOrdinal || o.RowDigest != row.RowDigest || o.Key != r.Key || o.Source != r.Source || o.SourceDocument != r.Document || o.SourceItem != r.Item || o.At.Before(s.Activation.At) || o.CommittedIndex > i.Index || o.CommittedIndex < s.Plan.Header.ObservedIndex {
		return false
	}
	switch o.Decision {
	case "accepted":
		if !collectionExecutionDesiredTuple(r, o.UID, o.Revision, o.Generation) || o.OldVersion != r.Target.OriginalRevision || o.Receipt.Actor != s.Actor || o.MutationSequence > i.CatalogMutationSequence {
			return false
		}
		parentEpoch, parentSeq, _ := ParseOperationHandle(s.ID)
		epoch, seq, err := ParseOperationHandle(o.Receipt.ID)
		if err != nil || epoch != parentEpoch || seq <= parentSeq || epoch == i.OperationEpoch && seq > i.OperationHighWater {
			return false
		}
		// Issued collection handles share the same allocation namespace as children.
		// A child can never impersonate ANY retained parent, including another epoch.
		if _, exists := i.Collections[o.Receipt.ID]; exists {
			return false
		}
	case "unchanged":
		if r.Change != "unchanged" || o.UID != r.Target.OriginalUID || o.Revision != r.Target.OriginalRevision || o.Generation != uint64(r.Target.OriginalGeneration) {
			return false
		}
	}
	return true
}

func collectionExecutionDesiredTuple(row CollectionPlanRow, uid, revision string, generation uint64) bool {
	if !catalogIdentifier(uid, 256) || !catalogIdentifier(revision, 256) {
		return false
	}
	switch row.Change {
	case "create":
		return row.Target.Absent && generation == 1
	case "update":
		return uid == row.Target.OriginalUID && revision != row.Target.OriginalRevision && generation >= uint64(row.Target.OriginalGeneration) && generation-uint64(row.Target.OriginalGeneration) <= 1
	default:
		return false
	}
}

func collectionExecutionProgressEqual(a, b CollectionExecutionProgress) bool {
	if !a.StartedAt.Equal(b.StartedAt) || !a.LastAt.Equal(b.LastAt) {
		return false
	}
	a.StartedAt, b.StartedAt, a.LastAt, b.LastAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if (a.Prepared == nil) != (b.Prepared == nil) {
		return false
	}
	if a.Prepared != nil {
		x, y := *a.Prepared, *b.Prepared
		if !x.At.Equal(y.At) {
			return false
		}
		x.At, y.At = time.Time{}, time.Time{}
		if x != y {
			return false
		}
	}
	a.Prepared, b.Prepared = nil, nil
	return a == b
}

func collectionExecutionRecoveryRecord(ctx context.Context, view *collectionLedgerView, op, slot string) (collectionExecutionRecord, []byte, error) {
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return collectionExecutionRecord{}, nil, err
	}
	if view.closed {
		view.mu.Unlock()
		return collectionExecutionRecord{}, nil, errCollectionLedgerClosed
	}
	borrowed, err := view.encodedExecution(op, slot)
	raw := append([]byte(nil), borrowed...)
	view.mu.Unlock()
	if err != nil {
		return collectionExecutionRecord{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return collectionExecutionRecord{}, nil, err
	}
	if len(raw) == 0 {
		return collectionExecutionRecord{}, nil, nil
	}
	r, err := decodeExecutionAt(op, slot, raw)
	return r, raw, err
}

func collectionExecutionRecoveryStats(ctx context.Context, view *collectionLedgerView, op string, p CollectionExecutionProgress) error {
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return err
	}
	defer view.mu.Unlock()
	if view.closed {
		return errCollectionLedgerClosed
	}
	actual, err := view.executionStat(op)
	if err != nil {
		return err
	}
	if p.Processed == 0 && p.Prepared == nil {
		if actual != (collectionExecutionStats{}) {
			return ErrCollectionInvalid
		}
	} else if actual.Binding != p.Binding || actual.Prepared != (p.Prepared != nil) || actual.Outcomes != p.Processed || actual.Terminals != p.ChildTerminals || actual.EncodedBytes != p.EncodedBytes || actual.TerminalBytes != p.TerminalBytes || actual.ChargedBytes != p.ChargedBytes || actual.TerminalCapacity != int64(p.Accepted)*collectionChildTerminalReserve {
		return ErrCollectionInvalid
	}
	return ctx.Err()
}
