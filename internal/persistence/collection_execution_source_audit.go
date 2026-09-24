package persistence

import (
	"bytes"
	"context"
)

type collectionExecutionSourceRetirementBatch struct {
	Namespace        string
	Removed          collectionLedgerDeletion
	InputDigest      string
	PlanDigest       string
	ValidationDigest string
}

func validateCollectionExecutionSourcePrefixes(ctx context.Context, i image, s CollectionState, view *collectionLedgerView) error {
	_, err := auditCollectionExecutionSourcePrefixes(ctx, i, s, view, false, nil)
	return err
}

func planCollectionExecutionSourceRetirement(ctx context.Context, i image, s CollectionState, view *collectionLedgerView) (*collectionExecutionSourceRetirementBatch, error) {
	return auditCollectionExecutionSourcePrefixes(ctx, i, s, view, true, nil)
}

// Audit only the selected parent's retained prefixes. The frozen view and the
// caller's transition ownership bind this plan to the subsequent deletion.
func auditCollectionExecutionSourcePrefixes(ctx context.Context, i image, s CollectionState, view *collectionLedgerView, selectTail bool, certificate *collectionExecutionSourceCertificate) (*collectionExecutionSourceRetirementBatch, error) {
	if ctx == nil || view == nil {
		return nil, ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := collectionExecutionRetiredRecoveryHeader(i, s); err != nil {
		return nil, err
	}
	if !s.ExecutionRetirement.executionComplete(s) {
		return nil, ErrCollectionInvalid
	}
	if err := validateCollectionExecutionRetirementNamespace(ctx, view, s.ID, collectionExecutionStats{}); err != nil {
		return nil, err
	}
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return nil, err
	}
	defer view.mu.Unlock()
	if view.closed {
		return nil, errCollectionLedgerClosed
	}
	inputCount, inputBytes := s.Uploaded-s.RemovedRows, s.EncodedBytes-s.RemovedBytes
	planCount, planBytes := s.Plan.UploadedFragments-s.Plan.RemovedFragments, s.Plan.EncodedBytes-s.Plan.RemovedBytes
	validationCount, validationBytes := s.Validation.Uploaded-s.Validation.RemovedRows, s.Validation.EncodedBytes-s.Validation.RemovedBytes
	if err := collectionExecutionSourceInputStats(ctx, view, s.ID, inputCount, inputBytes); err != nil {
		return nil, err
	}
	if count, size, err := view.planStats(s.ID); err != nil || count != planCount || size != planBytes {
		return nil, ErrCollectionInvalid
	}
	if count, size, err := view.validationStats(s.ID); err != nil || count != validationCount || size != validationBytes {
		return nil, ErrCollectionInvalid
	}
	result := &collectionExecutionSourceRetirementBatch{Namespace: "header", InputDigest: collectionInitialDigest(), PlanDigest: collectionPlanInitialDigest(), ValidationDigest: CollectionValidationInitialDigest()}
	inputKeep, planKeep, validationKeep := inputCount, planCount, validationCount
	if selectTail {
		var err error
		switch {
		case validationCount != 0:
			result.Namespace = "validation"
			result.Removed, err = view.selectValidationTail(s.ID, validationCount, validationBytes)
			validationKeep -= result.Removed.Rows
		case planCount != 0:
			result.Namespace = "plan"
			result.Removed, err = view.selectPlanTail(s.ID, planCount, planBytes)
			planKeep -= result.Removed.Rows
		case inputCount != 0:
			result.Namespace = "input"
			_, result.Removed, err = view.planDelete(s.ID, inputCount, inputBytes)
			inputKeep -= result.Removed.Rows
		}
		if err != nil {
			return nil, err
		}
	}
	wantInput, wantPlan, wantValidation := s.ProgressDigest, s.Plan.ProgressDigest, s.Validation.ProgressDigest
	if source := s.ExecutionRetirement.Sources; source != nil {
		wantInput, wantPlan, wantValidation = source.InputDigest, source.PlanDigest, source.ValidationDigest
	}
	inputDigest := collectionInitialDigest()
	var encoded int64
	err := walkCollectionExecutionSourcePrefix(ctx, view, s.ID, collectionLedgerRecords, view.rows[s.ID], inputCount, collectionLedgerMaxFrame, func(ordinal uint64, raw []byte) error {
		row, err := decodeCollectionLedgerRow(raw)
		if err != nil || row.OperationID != s.ID || row.Item.Ordinal != ordinal || int64(len(raw)) > inputBytes-encoded {
			return ErrCollectionInvalid
		}
		indexed, found, err := collectionReadIndex(view, s.ID, row.Item.Key)
		if err != nil || !found || indexed != ordinal {
			return ErrCollectionInvalid
		}
		inputDigest, err = collectionNextDigest(inputDigest, row.Item)
		if err != nil {
			return err
		}
		encoded += int64(len(raw))
		if certificate != nil {
			certificate.input.observe(ordinal, inputDigest)
		}
		if ordinal == inputKeep {
			result.InputDigest = inputDigest
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if encoded != inputBytes || inputDigest != wantInput {
		return nil, ErrCollectionInvalid
	}
	prefix := newCollectionPlanPrefix(s.Plan.Header)
	defer prefix.close()
	var validationInputs map[uint64]struct{}
	if validationCount != 0 && s.Validation.Header.Valid {
		if s.Plan.RemovedFragments != 0 {
			return nil, ErrCollectionInvalid
		}
		validationInputs = make(map[uint64]struct{})
	}
	err = walkCollectionExecutionSourcePrefix(ctx, view, s.ID, collectionLedgerPlans, view.planRows[s.ID], planCount, collectionPlanLedgerMaxFrame, func(ordinal uint64, raw []byte) error {
		part, err := decodeCollectionPlanLedgerRow(raw)
		if err != nil || part.OperationID != s.ID || part.Part.Ordinal != ordinal {
			return ErrCollectionInvalid
		}
		if row := part.Part.Fragment.Row; row != nil {
			input, found, err := view.item(s.ID, row.InputOrdinal)
			if err != nil || !found || !collectionPlanInputMatches(*row, input) {
				return ErrCollectionInvalid
			}
			if validationInputs != nil {
				if _, duplicate := validationInputs[row.InputOrdinal]; duplicate || len(validationInputs) >= CollectionValidationMaxItems {
					return ErrCollectionInvalid
				}
				validationInputs[row.InputOrdinal] = struct{}{}
				if row.InputOrdinal <= validationCount {
					data, err := view.encodedValidationItem(s.ID, row.InputOrdinal)
					if err != nil {
						return err
					}
					verdict, err := decodeCollectionValidationLedgerRow(data)
					if err != nil || verdict.OperationID != s.ID || verdict.Item != collectionValidationPlanItem(*row) {
						return ErrCollectionInvalid
					}
				}
			}
		}
		if err := prefix.add(ctx, s.ID, part.Part); err != nil {
			return err
		}
		if certificate != nil {
			certificate.plan.observe(ordinal, prefix.progress)
		}
		if ordinal == planKeep {
			result.PlanDigest = prefix.progress
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := prefix.matches(s.Plan, collectionExecutionSourcePlanTerminal(s)); err != nil {
		return nil, err
	}
	if prefix.progress != wantPlan || validationInputs != nil && uint64(len(validationInputs)) != s.ItemCount {
		return nil, ErrCollectionInvalid
	}
	validationDigest, logicalBytes := CollectionValidationInitialDigest(), uint64(0)
	encoded = 0
	err = walkCollectionExecutionSourcePrefix(ctx, view, s.ID, collectionLedgerValidation, view.validationRows[s.ID], validationCount, collectionValidationLedgerMaxFrame, func(ordinal uint64, raw []byte) error {
		row, err := decodeCollectionValidationLedgerRow(raw)
		if err != nil || row.OperationID != s.ID || row.Item.Ordinal != ordinal || int64(len(raw)) > validationBytes-encoded {
			return ErrCollectionInvalid
		}
		input, found, err := view.item(s.ID, ordinal)
		if err != nil || !found || !collectionValidationInputMatches(row.Item, input) {
			return ErrCollectionInvalid
		}
		digest, cost, err := CollectionValidationNextDigest(validationDigest, row.Item)
		if err != nil || cost > s.Validation.ResultBytes-logicalBytes {
			return ErrCollectionInvalid
		}
		validationDigest, logicalBytes, encoded = digest, logicalBytes+cost, encoded+int64(len(raw))
		if certificate != nil {
			certificate.validation.observe(ordinal, digest)
		}
		if ordinal == validationKeep {
			result.ValidationDigest = digest
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if encoded != validationBytes || validationDigest != wantValidation || s.Validation.RemovedRows == 0 && logicalBytes != s.Validation.ResultBytes {
		return nil, ErrCollectionInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Inspect physical keys as well as counters: fabricated bucket sequences must
// not conceal additional rows or a sparse ordinal range.
func walkCollectionExecutionSourcePrefix(ctx context.Context, view *collectionLedgerView, op string, namespace []byte, rows map[uint64][]byte, count uint64, maxFrame int, visit func(uint64, []byte) error) error {
	check := func(ordinal uint64, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > maxFrame {
			return ErrCollectionInvalid
		}
		return visit(ordinal, raw)
	}
	if view.tx == nil {
		if uint64(len(rows)) != count {
			return ErrCollectionInvalid
		}
		for ordinal := uint64(1); ordinal <= count; ordinal++ {
			if err := check(ordinal, rows[ordinal]); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	root := view.tx.Bucket(namespace)
	if root == nil {
		return ErrCollectionInvalid
	}
	bucket := root.Bucket([]byte(op))
	if bucket == nil {
		if count != 0 {
			return ErrCollectionInvalid
		}
		return ctx.Err()
	}
	var ordinal uint64
	cursor := bucket.Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		ordinal++
		if ordinal > count || !bytes.Equal(key, collectionOrdinal(ordinal)) {
			return ErrCollectionInvalid
		}
		if err := check(ordinal, raw); err != nil {
			return err
		}
	}
	if ordinal != count {
		return ErrCollectionInvalid
	}
	return ctx.Err()
}

func collectionExecutionSourceInputStats(ctx context.Context, view *collectionLedgerView, op string, count uint64, size int64) error {
	if view.tx == nil {
		if uint64(len(view.rows[op])) != count || uint64(len(view.keys[op])) != count || view.operationBytes[op] != size || size > view.bytes {
			return ErrCollectionInvalid
		}
		return ctx.Err()
	}
	root, index := view.tx.Bucket(collectionLedgerRecords), view.tx.Bucket(collectionLedgerKeys)
	if root == nil || index == nil {
		return ErrCollectionInvalid
	}
	rows, keys := root.Bucket([]byte(op)), index.Bucket([]byte(op))
	if count == 0 && rows == nil && keys == nil {
		return ctx.Err()
	}
	if rows == nil || keys == nil || count == 0 || rows.Sequence() != count || keys.Sequence() != uint64(size) {
		return ErrCollectionInvalid
	}
	used, err := collectionLedgerBytes(view.tx.Bucket(collectionLedgerMeta))
	if err != nil || size > used {
		return ErrCollectionInvalid
	}
	var entries uint64
	err = keys.ForEach(func(_ []byte, raw []byte) error {
		entries++
		if entries > count || len(raw) != 8 {
			return ErrCollectionInvalid
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	if entries != count {
		return ErrCollectionInvalid
	}
	return ctx.Err()
}
