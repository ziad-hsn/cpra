package persistence

import (
	"bytes"
	"context"
	"slices"
)

// This independent metadata allowance may coexist with an execution/publication
// index. Retaining it across other parents' work avoids repeated cold audits.
// An admitted plan has <=32MiB/(4 framing bytes + 1 JSON byte) fragments and
// input/validation each have <=10,000 rows. Count-limited boundaries use at
// most one checkpoint per 256 records; byte-limited boundaries consume >2MiB
// of the <=1GiB ledger. Including six endpoints and 128KiB of fence allowance,
// the conservative valid-artifact bound is below 3.5MiB.
const collectionExecutionSourceCertificateBytes = 4 << 20
const collectionExecutionSourceCheckpointBytes = 128

type collectionExecutionSourceCheckpoint struct {
	ordinal uint64
	encoded int64
	digest  string
}

type collectionExecutionSourceCheckpoints struct {
	points []collectionExecutionSourceCheckpoint
	next   int
}

// Only certified deletion boundaries survive the cold audit. No ledger frames,
// codec states, resource bodies or transactions are retained.
type collectionExecutionSourceCertificate struct {
	ledger                  *collectionLedger
	binding                 CollectionExecutionBinding
	fence                   *CollectionExecutionSourceRetirementFence
	input, plan, validation collectionExecutionSourceCheckpoints
	bytes                   int
}

func (p *collectionExecutionSourceCheckpoints) observe(ordinal uint64, digest string) {
	if p.next < len(p.points) && p.points[p.next].ordinal == ordinal {
		p.points[p.next].digest = digest
		p.next++
	}
}

func (c *collectionExecutionSourceCertificate) matches(ledger *collectionLedger, s CollectionState) bool {
	if c == nil || c.ledger != ledger || c.fence == nil || s.ExecutionRetirement == nil || !s.ExecutionRetirement.complete(s) {
		return false
	}
	binding, err := collectionExecutionBindingFor(s)
	if err != nil || binding != c.binding || !collectionExecutionRetirementsEqual(c.fence.Retirement.Retirement, s.ExecutionRetirement) ||
		!collectionExecutionResultStatesEqual(c.fence.Retirement.Result, *s.ExecutionResult, true) {
		return false
	}
	earlier, err := compareCollectionSourceProgress(c.fence.Cleanup, collectionCleanupFor(s))
	return err == nil && !earlier
}

func (c *collectionExecutionSourceCertificate) advance(s CollectionState) {
	c.fence = CollectionExecutionSourceRetirementFenceFor(s)
}

func buildCollectionExecutionSourceCertificate(ctx context.Context, i image, s CollectionState, view *collectionLedgerView, ledger *collectionLedger) (*collectionExecutionSourceCertificate, error) {
	if ctx == nil || view == nil || ledger == nil || collectionExecutionRetiredRecoveryHeader(i, s) != nil || !s.ExecutionRetirement.executionComplete(s) {
		return nil, ErrCollectionInvalid
	}
	c := &collectionExecutionSourceCertificate{ledger: ledger, binding: s.ExecutionResult.Summary.Binding,
		fence: CollectionExecutionSourceRetirementFenceFor(s), bytes: 8 * maxCollectionReceiptEventBytes}
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return nil, err
	}
	if view.closed {
		view.mu.Unlock()
		return nil, errCollectionLedgerClosed
	}
	var err error
	c.input, err = c.boundaries(ctx, view, s.ID, "input", s.Uploaded-s.RemovedRows, s.EncodedBytes-s.RemovedBytes, collectionInitialDigest())
	if err == nil {
		c.plan, err = c.boundaries(ctx, view, s.ID, "plan", s.Plan.UploadedFragments-s.Plan.RemovedFragments, s.Plan.EncodedBytes-s.Plan.RemovedBytes, collectionPlanInitialDigest())
	}
	if err == nil {
		c.validation, err = c.boundaries(ctx, view, s.ID, "validation", s.Validation.Uploaded-s.Validation.RemovedRows, s.Validation.EncodedBytes-s.Validation.RemovedBytes, CollectionValidationInitialDigest())
	}
	view.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if _, err := auditCollectionExecutionSourcePrefixes(ctx, i, s, view, false, c); err != nil {
		return nil, err
	}
	for _, p := range []*collectionExecutionSourceCheckpoints{&c.input, &c.plan, &c.validation} {
		if p.next != len(p.points) {
			return nil, ErrCollectionInvalid
		}
	}
	return c, nil
}

// The reverse pass selects the same byte/count boundaries as Delete*Page. The
// subsequent full audit supplies their rolling digests in original order.
func (c *collectionExecutionSourceCertificate) boundaries(ctx context.Context, view *collectionLedgerView, op, namespace string, count uint64, size int64, initial string) (collectionExecutionSourceCheckpoints, error) {
	p := collectionExecutionSourceCheckpoints{}
	add := func(ordinal uint64, encoded int64) error {
		if c.bytes > collectionExecutionSourceCertificateBytes-collectionExecutionSourceCheckpointBytes {
			return ErrCollectionQuota
		}
		c.bytes += collectionExecutionSourceCheckpointBytes
		p.points = append(p.points, collectionExecutionSourceCheckpoint{ordinal: ordinal, encoded: encoded})
		return nil
	}
	if err := add(count, size); err != nil {
		return p, err
	}
	remaining, batchRows, batchBytes := size, uint64(0), int64(0)
	for ordinal := count; ordinal > 0; ordinal-- {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		raw, err := collectionExecutionSourceFrame(view, op, namespace, ordinal)
		if err != nil || len(raw) == 0 || int64(len(raw)) > collectionLedgerBatchBytes || int64(len(raw)) > remaining {
			return p, ErrCollectionInvalid
		}
		if batchRows == collectionLedgerBatchLimit || int64(len(raw)) > collectionLedgerBatchBytes-batchBytes {
			if err := add(ordinal, remaining); err != nil {
				return p, err
			}
			batchRows, batchBytes = 0, 0
		}
		remaining -= int64(len(raw))
		batchRows++
		batchBytes += int64(len(raw))
	}
	if remaining != 0 {
		return p, ErrCollectionInvalid
	}
	if count != 0 {
		if err := add(0, 0); err != nil {
			return p, err
		}
	}
	slices.Reverse(p.points)
	p.points[0].digest, p.next = initial, 1
	return p, nil
}

func collectionExecutionSourceFrame(view *collectionLedgerView, op, namespace string, ordinal uint64) ([]byte, error) {
	switch namespace {
	case "input":
		return view.encodedItem(op, ordinal)
	case "plan":
		return view.encodedPlanPart(op, ordinal)
	case "validation":
		return view.encodedValidationItem(op, ordinal)
	}
	return nil, ErrCollectionInvalid
}

type collectionExecutionSourceReadCost struct {
	Rows  uint64
	Bytes int64
}

// Warm planning authenticates the selected tail against certified boundary
// digests. Other certified prefixes are unchanged by the terminal protocol;
// recovery and cold reconstruction continue to audit their complete contents.
func (c *collectionExecutionSourceCertificate) planRetirement(ctx context.Context, i image, s CollectionState, view *collectionLedgerView, ledger *collectionLedger) (*collectionExecutionSourceRetirementBatch, collectionExecutionSourceReadCost, error) {
	cost := collectionExecutionSourceReadCost{}
	if ctx == nil || view == nil || !c.matches(ledger, s) || collectionExecutionRetiredRecoveryHeader(i, s) != nil {
		return nil, cost, ErrCollectionInvalid
	}
	if err := validateCollectionExecutionRetirementNamespace(ctx, view, s.ID, collectionExecutionStats{}); err != nil {
		return nil, cost, err
	}
	if err := collectionExecutionViewLock(ctx, &view.mu); err != nil {
		return nil, cost, err
	}
	defer view.mu.Unlock()
	if view.closed {
		return nil, cost, errCollectionLedgerClosed
	}
	if err := collectionExecutionSourceCachedStats(view, s); err != nil {
		return nil, cost, err
	}
	r := &collectionExecutionSourceRetirementBatch{Namespace: "header", InputDigest: s.ProgressDigest, PlanDigest: s.Plan.ProgressDigest, ValidationDigest: s.Validation.ProgressDigest}
	if source := s.ExecutionRetirement.Sources; source != nil {
		r.InputDigest, r.PlanDigest, r.ValidationDigest = source.InputDigest, source.PlanDigest, source.ValidationDigest
	}
	var p *collectionExecutionSourceCheckpoints
	var count uint64
	var size int64
	var current string
	var err error
	switch {
	case s.Validation.RemovedRows != s.Validation.Uploaded:
		r.Namespace, p, count, size, current = "validation", &c.validation, s.Validation.Uploaded-s.Validation.RemovedRows, s.Validation.EncodedBytes-s.Validation.RemovedBytes, r.ValidationDigest
		r.Removed, err = view.selectValidationTail(s.ID, count, size)
	case s.Plan.RemovedFragments != s.Plan.UploadedFragments:
		r.Namespace, p, count, size, current = "plan", &c.plan, s.Plan.UploadedFragments-s.Plan.RemovedFragments, s.Plan.EncodedBytes-s.Plan.RemovedBytes, r.PlanDigest
		r.Removed, err = view.selectPlanTail(s.ID, count, size)
	case s.RemovedRows != s.Uploaded:
		r.Namespace, p, count, size, current = "input", &c.input, s.Uploaded-s.RemovedRows, s.EncodedBytes-s.RemovedBytes, r.InputDigest
		_, r.Removed, err = view.planDelete(s.ID, count, size)
	default:
		return r, cost, ctx.Err()
	}
	if err != nil {
		return nil, cost, err
	}
	// The selector has decoded exactly this bounded tail once.
	cost.Rows, cost.Bytes = r.Removed.Rows, r.Removed.EncodedBytes
	// Byte-limited selection also borrows the next frame's length before
	// rejecting it. Charge its maximum size without reading it a second time.
	probeBytes := int64(0)
	if r.Removed.More && r.Removed.Rows < collectionLedgerBatchLimit {
		probeBytes = collectionLedgerMaxFrame
		if r.Namespace == "plan" {
			probeBytes = collectionPlanLedgerMaxFrame
		} else if r.Namespace == "validation" {
			probeBytes = collectionValidationLedgerMaxFrame
		}
		cost.Rows++
		cost.Bytes += probeBytes
	}
	at, found := slices.BinarySearchFunc(p.points, count, func(a collectionExecutionSourceCheckpoint, b uint64) int {
		if a.ordinal < b {
			return -1
		}
		if a.ordinal > b {
			return 1
		}
		return 0
	})
	if !found || at == 0 || p.points[at].digest != current || p.points[at].encoded != size {
		return nil, cost, ErrCollectionInvalid
	}
	keep := p.points[at-1]
	if keep.ordinal != count-r.Removed.Rows || keep.encoded != size-r.Removed.EncodedBytes || !bootstrapHash(keep.digest) {
		return nil, cost, ErrCollectionInvalid
	}
	digest := keep.digest
	for ordinal := keep.ordinal + 1; ordinal <= count; ordinal++ {
		if err := ctx.Err(); err != nil {
			return nil, cost, err
		}
		raw, err := collectionExecutionSourceFrame(view, s.ID, r.Namespace, ordinal)
		if err != nil || len(raw) == 0 {
			return nil, cost, ErrCollectionInvalid
		}
		cost.Rows++
		cost.Bytes += int64(len(raw))
		if cost.Rows > 2*collectionLedgerBatchLimit+1 || cost.Bytes > 2*collectionLedgerBatchBytes+probeBytes {
			return nil, cost, ErrCollectionInvalid
		}
		switch r.Namespace {
		case "input":
			row, decodeErr := decodeCollectionLedgerRow(raw)
			if decodeErr != nil || row.OperationID != s.ID || row.Item.Ordinal != ordinal {
				return nil, cost, ErrCollectionInvalid
			}
			digest, err = collectionNextDigest(digest, row.Item)
		case "plan":
			row, decodeErr := decodeCollectionPlanLedgerRow(raw)
			if decodeErr != nil || row.OperationID != s.ID || row.Part.Ordinal != ordinal {
				return nil, cost, ErrCollectionInvalid
			}
			digest, err = collectionPlanNextDigest(digest, row.Part.Fragment)
		case "validation":
			row, decodeErr := decodeCollectionValidationLedgerRow(raw)
			if decodeErr != nil || row.OperationID != s.ID || row.Item.Ordinal != ordinal {
				return nil, cost, ErrCollectionInvalid
			}
			digest, _, err = CollectionValidationNextDigest(digest, row.Item)
		}
		if err != nil {
			return nil, cost, err
		}
	}
	if digest != current {
		return nil, cost, ErrCollectionInvalid
	}
	switch r.Namespace {
	case "input":
		r.InputDigest = keep.digest
	case "plan":
		r.PlanDigest = keep.digest
	case "validation":
		r.ValidationDigest = keep.digest
	}
	return r, cost, ctx.Err()
}

func collectionExecutionSourceCachedStats(view *collectionLedgerView, s CollectionState) error {
	inputCount, inputBytes := s.Uploaded-s.RemovedRows, s.EncodedBytes-s.RemovedBytes
	if view.tx == nil {
		if uint64(len(view.rows[s.ID])) != inputCount || uint64(len(view.keys[s.ID])) != inputCount || view.operationBytes[s.ID] != inputBytes {
			return ErrCollectionInvalid
		}
	} else {
		rows, keys := view.tx.Bucket(collectionLedgerRecords), view.tx.Bucket(collectionLedgerKeys)
		if rows == nil || keys == nil {
			return ErrCollectionInvalid
		}
		r, k := rows.Bucket([]byte(s.ID)), keys.Bucket([]byte(s.ID))
		if inputCount == 0 {
			if r != nil || k != nil {
				return ErrCollectionInvalid
			}
		} else if r == nil || k == nil || r.Sequence() != inputCount || k.Sequence() != uint64(inputBytes) {
			return ErrCollectionInvalid
		}
	}
	if count, size, err := view.planStats(s.ID); err != nil || count != s.Plan.UploadedFragments-s.Plan.RemovedFragments || size != s.Plan.EncodedBytes-s.Plan.RemovedBytes {
		return ErrCollectionInvalid
	}
	if count, size, err := view.validationStats(s.ID); err != nil || count != s.Validation.Uploaded-s.Validation.RemovedRows || size != s.Validation.EncodedBytes-s.Validation.RemovedBytes {
		return ErrCollectionInvalid
	}
	if view.tx != nil {
		for _, pair := range []struct {
			namespace []byte
			count     uint64
		}{{collectionLedgerRecords, inputCount}, {collectionLedgerPlans, s.Plan.UploadedFragments - s.Plan.RemovedFragments}, {collectionLedgerValidation, s.Validation.Uploaded - s.Validation.RemovedRows}} {
			root := view.tx.Bucket(pair.namespace)
			if root == nil {
				return ErrCollectionInvalid
			}
			bucket := root.Bucket([]byte(s.ID))
			if bucket == nil {
				if pair.count != 0 {
					return ErrCollectionInvalid
				}
				continue
			}
			first, _ := bucket.Cursor().First()
			last, _ := bucket.Cursor().Last()
			if pair.count == 0 || !bytes.Equal(first, collectionOrdinal(1)) || !bytes.Equal(last, collectionOrdinal(pair.count)) {
				return ErrCollectionInvalid
			}
		}
	}
	return nil
}
