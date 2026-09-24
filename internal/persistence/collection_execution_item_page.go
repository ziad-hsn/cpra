package persistence

import (
	"context"
	"encoding/hex"
	"errors"
)

type collectionExecutionItemPage struct {
	Items []CollectionExecutionItem
	Next  uint64 // Last included original input ordinal; after when exhausted.
	Bytes int64  // Sum of canonical item JSON lengths, without history framing.
}

// collectionExecutionItemIndex is an immutable, disposable certificate of one
// original finalized generation. The machine separately binds it to the ledger
// identity and discards it before namespace retirement or another plan index.
// Its logical budget includes the original index, commitments and terminal tree.
// It owns no frozen reader, ciphertext or plaintext result map.
type collectionExecutionItemIndex struct {
	summary CollectionExecutionSummary
	index   *collectionExecutionIndex
	proof   *collectionExecutionParentRecovery
}

func (x *collectionExecutionItemIndex) matches(head CollectionState) bool {
	if x == nil || x.index == nil || head.ExecutionResult == nil || head.Validation == nil || !collectionExecutionArtifactsComplete(head) ||
		!collectionExecutionSummariesEqual(&x.summary, &head.ExecutionResult.Summary) {
		return false
	}
	i := x.index
	return i.auditOnly && i.binding == collectionPlanFence(head) && i.resultID == head.Validation.Header.ResultID &&
		i.result == head.Validation.Descriptor && i.capabilities == head.Validation.Header.CapabilitiesDigest && i.activationID == head.Activation.ID
}

// buildCollectionExecutionItemPage consumes and closes the caller's same-index
// frozen view, including on error. No Store/FSM lock may be held during this
// call. The detached image needs only allocation/index scalars and Restore;
// global handle uniqueness was certified at admission/finalization/recovery.
// Source retirement is not supported by this reader. The caller rechecks live
// health, generation and original summary after this function closes the view.
// cached is reusable only when the caller has also checked ledger identity.
func buildCollectionExecutionItemPage(ctx context.Context, captured image, head CollectionState, view *collectionLedgerView, cached *collectionExecutionItemIndex, after uint64, limit int) (page collectionExecutionItemPage, verified *collectionExecutionItemIndex, err error) {
	if view == nil {
		return page, nil, ErrCollectionInvalid
	}
	defer func() {
		if closeErr := view.Close(); closeErr != nil {
			// A quota is recoverable only if the source transaction closed
			// successfully. Preserve close failure as storage unavailability.
			err = errors.Join(ErrCollectionUnavailable, err, closeErr)
		}
		if err != nil {
			page, verified = collectionExecutionItemPage{}, nil
		}
	}()
	if ctx == nil || limit < 1 || limit > collectionExecutionItemPageRows || after > head.ItemCount || head.ExecutionResult == nil || head.validate() != nil {
		return page, nil, ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return page, nil, err
	}
	if cached != nil {
		if !cached.matches(head) {
			return page, nil, ErrCollectionConflict
		}
		verified = cached
	} else {
		limits := defaultCollectionExecutionIndexLimits()
		commitmentBytes, err := collectionExecutionCommitmentCost(head.ItemCount)
		if err != nil {
			return page, nil, err
		}
		// A sparse tree has at most this many logical nodes. Charge 128 bytes
		// each for links/hash/allocation overhead plus fixed summary ownership.
		terminals := uint64(0)
		if head.Execution != nil {
			terminals = head.Execution.ChildTerminals
		}
		nodes := min(terminals*(collectionExecutionTerminalDepth+1), uint64(2*collectionExecutionTerminalLeaves-1))
		limits.Bytes -= commitmentBytes + int64(nodes)*128 + 16<<10
		x, err := buildCollectionExecutionAuditIndex(ctx, head, view, limits)
		if err != nil {
			return page, nil, err
		}
		if err := verifyCollectionExecutionResults(ctx, head, x, view); err != nil {
			return page, nil, err
		}
		var proof *collectionExecutionParentRecovery
		if head.Execution == nil {
			stats, err := view.ExecutionStats(head.ID)
			if err != nil || stats != (collectionExecutionStats{}) {
				return page, nil, errors.Join(ErrCollectionInvalid, err)
			}
		} else {
			if err := collectionExecutionRecoveryHeader(captured, head); err != nil {
				return page, nil, err
			}
			proof, err = rebuildCollectionExecutionParent(ctx, captured, head, view, nil, x)
			if err != nil {
				return page, nil, err
			}
		}
		verified = &collectionExecutionItemIndex{summary: head.ExecutionResult.Summary.Clone(), index: x, proof: proof}
	}
	page, err = collectionExecutionItemsFromVerified(ctx, head, view, verified.index, verified.proof, after, limit, collectionExecutionItemPageBytes)
	return page, verified, err
}

// The verified index and proof cache describe this exact frozen generation.
// Re-read selected original frames and commitments before joining a bounded
// page; neither a ledger record nor a matching counter is authority by itself.
func collectionExecutionItemsFromVerified(ctx context.Context, head CollectionState, view *collectionLedgerView, index *collectionExecutionIndex, proof *collectionExecutionParentRecovery, after uint64, limit int, maxBytes int64) (collectionExecutionItemPage, error) {
	page := collectionExecutionItemPage{Next: after}
	if ctx == nil || view == nil || index == nil || head.ExecutionResult == nil || limit < 1 || limit > collectionExecutionItemPageRows || maxBytes < 1 || maxBytes > collectionExecutionItemPageBytes || after > head.ItemCount {
		return collectionExecutionItemPage{}, ErrCollectionInvalid
	}
	summary := head.ExecutionResult.Summary
	processed := uint64(0)
	if p := summary.Fence.Progress; p != nil {
		processed = p.Processed
		if proof == nil || !collectionExecutionProgressEqual(proof.progress, *p) || !proof.commitments.matches(summary.Binding, processed, p.OutcomeDigest) || proof.tree.Root() != p.TerminalRoot {
			return collectionExecutionItemPage{}, ErrCollectionInvalid
		}
	}
	for inputOrdinal := after + 1; inputOrdinal <= head.ItemCount && len(page.Items) < limit; inputOrdinal++ {
		input, inputDigest, err := collectionExecutionInputItemHash(ctx, view, head.ID, inputOrdinal)
		if err != nil {
			return collectionExecutionItemPage{}, err
		}
		planOrdinal, found := index.rowOrdinal(input.Key)
		row, ok := index.row(planOrdinal)
		matches := found && ok && row.Row.InputOrdinal == inputOrdinal && inputDigest == row.InputFrameDigest && collectionPlanInputMatches(row.Row, input)
		clearCollectionPreparationItem(&input)
		if !matches {
			return collectionExecutionItemPage{}, ErrCollectionInvalid
		}
		if err := index.walkRow(ctx, view, planOrdinal, func(CollectionPlanFragment) error { return nil }); err != nil {
			return collectionExecutionItemPage{}, err
		}
		item := CollectionExecutionItem{Version: collectionExecutionItemVersion, Binding: summary.Binding,
			InputOrdinal: inputOrdinal, PlanOrdinal: planOrdinal, Key: row.Row.Key, Source: row.Row.Source,
			SourceDocument: row.Row.Document, SourceItem: row.Row.Item, OriginalUID: row.Row.Target.OriginalUID, OldVersion: row.Row.Target.OriginalRevision,
			Decision: "unattempted"}
		terminal, terminalRaw, err := collectionExecutionRecoveryRecord(ctx, view, head.ID, collectionExecutionTerminalSlot(planOrdinal))
		if err != nil {
			return collectionExecutionItemPage{}, err
		}
		if planOrdinal <= processed {
			record, _, err := collectionExecutionRecoveryRecord(ctx, view, head.ID, collectionExecutionOutcomeSlot(planOrdinal))
			if err != nil || !proof.commitments.matchesRecord(record) {
				return collectionExecutionItemPage{}, errors.Join(ErrCollectionInvalid, err)
			}
			outcome := record.Outcome
			if outcome.Ordinal != planOrdinal || outcome.InputOrdinal != inputOrdinal || outcome.RowDigest != row.RowDigest || outcome.Key != item.Key || outcome.Source != item.Source || outcome.SourceDocument != item.SourceDocument || outcome.SourceItem != item.SourceItem {
				return collectionExecutionItemPage{}, ErrCollectionInvalid
			}
			item.Decision, item.UID, item.NewVersion, item.Generation = outcome.Decision, outcome.UID, outcome.Revision, outcome.Generation
			item.CommittedIndex, item.DecidedAt = outcome.CommittedIndex, outcome.At
			if outcome.Decision == "accepted" {
				if terminal.Terminal == nil || !terminal.Terminal.matches(*outcome) {
					return collectionExecutionItemPage{}, ErrCollectionInvalid
				}
				path, err := proof.tree.Proof(planOrdinal)
				if err != nil || len(path) != collectionExecutionTerminalDepth {
					return collectionExecutionItemPage{}, errors.Join(ErrCollectionInvalid, err)
				}
				root := collectionExecutionTerminalPath(collectionExecutionTerminalLeaf(planOrdinal, terminalRaw), planOrdinal, path)
				if hex.EncodeToString(root[:]) != summary.Fence.Progress.TerminalRoot {
					return collectionExecutionItemPage{}, ErrCollectionInvalid
				}
				t := terminal.Terminal
				item.Child = &CollectionExecutionItemChild{ID: t.ChildID, State: t.State, Outcome: t.Outcome, UpdatedAt: t.UpdatedAt, InvalidatedByRestore: t.InvalidatedByRestore}
			} else if terminal.Terminal != nil {
				return collectionExecutionItemPage{}, ErrCollectionInvalid
			}
		} else {
			// The stop summary proves this suffix was never decided. Unexpected
			// source evidence is corruption, not an alternative unattempted row.
			outcome, _, err := collectionExecutionRecoveryRecord(ctx, view, head.ID, collectionExecutionOutcomeSlot(planOrdinal))
			if err != nil || outcome.Outcome != nil || terminal.Terminal != nil {
				return collectionExecutionItemPage{}, errors.Join(ErrCollectionInvalid, err)
			}
		}
		if err := item.validateSummary(summary); err != nil {
			return collectionExecutionItemPage{}, err
		}
		raw, err := collectionExecutionItemEncoding(item)
		if err != nil {
			return collectionExecutionItemPage{}, err
		}
		if int64(len(raw)) > maxBytes-page.Bytes {
			if len(page.Items) == 0 {
				return collectionExecutionItemPage{}, ErrCollectionQuota
			}
			break
		}
		page.Items = append(page.Items, item)
		page.Bytes += int64(len(raw))
		page.Next = inputOrdinal
	}
	if err := ctx.Err(); err != nil {
		return collectionExecutionItemPage{}, err
	}
	return page, nil
}
