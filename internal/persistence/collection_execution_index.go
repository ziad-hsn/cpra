package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
)

var errCollectionExecutionIndexLimit = errors.New("collection execution index limit exceeded")

// These are logical retained-metadata and work limits, not an RSS promise.
// Codec scratch remains one bounded fragment plus its <=10,000 row indexes.
type collectionExecutionIndexLimits struct {
	Rows, ReverseKeys, Touches int
	Bytes                      int64
	Work                       uint64
}

func defaultCollectionExecutionIndexLimits() collectionExecutionIndexLimits {
	return collectionExecutionIndexLimits{Rows: 10_000, ReverseKeys: 10_000, Touches: 100_000, Bytes: 32 << 20, Work: 1_000_000}
}

func (l collectionExecutionIndexLimits) valid() bool {
	max := defaultCollectionExecutionIndexLimits()
	return l.Rows > 0 && l.Rows <= max.Rows && l.ReverseKeys > 0 && l.ReverseKeys <= max.ReverseKeys &&
		l.Touches > 0 && l.Touches <= max.Touches && l.Bytes > 0 && l.Bytes <= max.Bytes && l.Work > 0 && l.Work <= max.Work
}

type collectionExecutionRow struct {
	Row                        CollectionPlanRow
	FirstFragment, EndFragment uint64 // Inclusive, including the row-end frame.
	RowDigest, RangeDigest     string
	InputFrameDigest           [sha256.Size]byte // Exact canonical encrypted input wrapper.
}

const collectionExecutionRowMetadataBytes = 2048 + sha256.Size

// Original always describes the validation-time resource identity and token.
// TouchOrdinals describe required earlier own outcomes, never accepted tokens.
// A later atomic command must certify each successful touch before substituting
// its token. Such a substitution NEVER replaces a target UID/revision fence.
type collectionExecutionReverse struct {
	Original      CollectionPlanGuard
	LastUse       uint64
	TouchOrdinals []uint64
}

// collectionExecutionIndex is disposable and immutable after construction. It
// holds neither a ledger view nor encrypted inputs, credentials or guard arrays.
// It verifies artifact identity, not current catalog validity or authorization.
type collectionExecutionIndex struct {
	binding      CollectionPlanVerification
	resultID     string
	result       CollectionValidationDescriptor
	capabilities string
	activationID string
	auditOnly    bool // Recovery evidence only; never accepted by matches.
	rows         []collectionExecutionRow
	rowOrdinals  map[CatalogKey]uint64 // Immutable original positions; no current/outcome authority.
	reverse      map[CatalogKey]collectionExecutionReverse
	limits       collectionExecutionIndexLimits
	bytes        int64
	touches      int
}

func collectionExecutionEligible(s CollectionState) bool {
	return (s.Phase == "validated" || s.Phase == "applying") && collectionExecutionArtifactsComplete(s)
}

func collectionExecutionArtifactsComplete(s CollectionState) bool {
	p, v := s.Plan, s.Validation
	return s.validate() == nil &&
		p != nil && !p.FinalizedAt.IsZero() && p.UploadedFragments == p.Descriptor.Fragments && p.ArtifactBytes == p.Descriptor.Bytes &&
		p.RemovedFragments == 0 && p.RemovedBytes == 0 && s.Uploaded == s.ItemCount && s.RemovedRows == 0 && s.RemovedBytes == 0 &&
		v != nil && v.Header.Valid && !v.Header.SummaryOnly && !v.FinalizedAt.IsZero() && v.HistorySealed && v.HistoryExpiredAt.IsZero() &&
		v.RemovedRows == 0 && v.RemovedBytes == 0 && v.Header.PlanID == p.Header.PlanID && v.Header.PlanDigest == p.Descriptor.Digest
}

// buildCollectionExecutionIndex requires a frozen ledger captured with s at one
// FSM index. The caller owns and closes that view BEFORE any subsequent bbolt
// write/acceptance; a pinned read transaction can otherwise block a write/remap.
// No FSM lock may be held while this bounded verification runs. A returned
// index grants no execution rights and retains no view/transaction.
func buildCollectionExecutionIndex(ctx context.Context, s CollectionState, view *collectionLedgerView, limits collectionExecutionIndexLimits) (*collectionExecutionIndex, error) {
	if !collectionExecutionEligible(s) {
		return nil, ErrCollectionPlanInvalid
	}
	return buildCollectionExecutionArtifactIndex(ctx, s, view, limits, false)
}

// buildCollectionExecutionAuditIndex verifies retained admitted artifacts even
// after cancellation or explicit restore. It does not fabricate a live phase:
// matches always rejects the returned audit-only index. As with the ordinary
// constructor, the caller closes the frozen view before any subsequent write.
func buildCollectionExecutionAuditIndex(ctx context.Context, s CollectionState, view *collectionLedgerView, limits collectionExecutionIndexLimits) (*collectionExecutionIndex, error) {
	if s.Activation == nil || (s.Phase != "applying" && s.Phase != "canceled" && s.Phase != "invalidated" && !collectionExecutionCompleted(s.Phase)) || !collectionExecutionArtifactsComplete(s) {
		return nil, ErrCollectionPlanInvalid
	}
	return buildCollectionExecutionArtifactIndex(ctx, s, view, limits, true)
}

func buildCollectionExecutionArtifactIndex(ctx context.Context, s CollectionState, view *collectionLedgerView, limits collectionExecutionIndexLimits, auditOnly bool) (*collectionExecutionIndex, error) {
	if ctx == nil || view == nil || !limits.valid() {
		return nil, ErrCollectionPlanInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.ItemCount > uint64(limits.Rows) || int64(s.ItemCount) > limits.Bytes/collectionExecutionRowMetadataBytes {
		return nil, errCollectionExecutionIndexLimit
	}
	x := &collectionExecutionIndex{auditOnly: auditOnly, binding: collectionPlanFence(s), resultID: s.Validation.Header.ResultID,
		result: s.Validation.Descriptor, capabilities: s.Validation.Header.CapabilitiesDigest,
		rows: make([]collectionExecutionRow, 0, int(s.ItemCount)), reverse: make(map[CatalogKey]collectionExecutionReverse), rowOrdinals: make(map[CatalogKey]uint64), limits: limits,
		// Charge each input hash before allocating row storage.
		bytes: int64(s.ItemCount) * collectionExecutionRowMetadataBytes}
	if s.Activation != nil {
		x.activationID = s.Activation.ID
	}
	var work uint64
	spend := func(n uint64) error {
		if n > limits.Work-work {
			return errCollectionExecutionIndexLimit
		}
		work += n
		return ctx.Err()
	}
	if err := collectionExecutionInput(ctx, view, s, spend); err != nil {
		return nil, err
	}
	if err := collectionExecutionPlanStats(ctx, view, s.ID, s.Plan.UploadedFragments, s.Plan.EncodedBytes); err != nil {
		return nil, err
	}
	prefix := newCollectionPlanPrefix(s.Plan.Header)
	defer prefix.close()
	var current collectionExecutionRow
	var rangeHash hash.Hash
	for ordinal := uint64(1); ordinal <= s.Plan.UploadedFragments; ordinal++ {
		part, err := collectionExecutionPart(ctx, view, s.ID, ordinal)
		if err != nil {
			return nil, err
		}
		if err := spend(uint64(1 + len(part.Fragment.Guards) + len(part.Fragment.Requires) + len(part.Fragment.Touches))); err != nil {
			return nil, err
		}
		if err := prefix.add(ctx, s.ID, part); err != nil {
			return nil, err
		}
		f := part.Fragment
		if f.Row != nil {
			input, inputHash, err := collectionExecutionInputItemHash(ctx, view, s.ID, f.Row.InputOrdinal)
			if err != nil || !collectionPlanInputMatches(*f.Row, input) {
				return nil, errors.Join(ErrCollectionPlanInvalid, err)
			}
			if err := x.charge(int64(len(f.Row.Source) + len(f.Row.Key.Kind) + len(f.Row.Key.ID) + len(f.Row.Target.OriginalUID) + len(f.Row.Target.OriginalRevision))); err != nil {
				return nil, err
			}
			// Charge the additional immutable lookup before inserting its entry.
			// Key strings already belong to retained row metadata; the charge
			// conservatively covers map storage, not measured heap allocation.
			if err := x.charge(256); err != nil {
				return nil, err
			}
			if _, exists := x.rowOrdinals[f.Row.Key]; exists {
				return nil, ErrCollectionPlanInvalid
			}
			x.rowOrdinals[f.Row.Key] = f.Row.Ordinal
			current = collectionExecutionRow{Row: collectionExecutionCloneRow(*f.Row), FirstFragment: ordinal, InputFrameDigest: inputHash}
			rangeHash = sha256.New()
			if err := x.observeReverse(f.Row.Target, f.Row.Ordinal); err != nil {
				return nil, err
			}
		}
		if rangeHash != nil {
			if err := collectionExecutionHashFragment(rangeHash, f); err != nil {
				return nil, err
			}
		}
		for _, guard := range f.Guards {
			if err := x.observeReverse(guard, current.Row.Ordinal); err != nil {
				return nil, err
			}
		}
		if f.End != nil {
			current.EndFragment, current.RowDigest = ordinal, f.End.Digest
			current.RangeDigest = hex.EncodeToString(rangeHash.Sum(nil))
			x.rows = append(x.rows, current)
			current, rangeHash = collectionExecutionRow{}, nil
		}
	}
	if err := prefix.matches(s.Plan, false); err != nil || len(x.rows) != int(s.ItemCount) {
		return nil, errors.Join(ErrCollectionPlanInvalid, err)
	}
	// A reverse guard may occur after a row that touches its key. A second
	// bounded pass finds those obligations without retaining all guards/touches.
	for _, row := range x.rows {
		if err := x.walkRow(ctx, view, row.Row.Ordinal, func(f CollectionPlanFragment) error {
			if err := spend(uint64(1 + len(f.Guards) + len(f.Requires) + len(f.Touches))); err != nil {
				return err
			}
			if f.Row != nil {
				if err := x.checkOriginal(f.Row.Target); err != nil {
					return err
				}
			}
			for _, guard := range f.Guards {
				if err := x.checkOriginal(guard); err != nil {
					return err
				}
			}
			for _, key := range f.Touches {
				baseline, guarded := x.reverse[key]
				if !guarded || row.Row.Ordinal >= baseline.LastUse {
					continue
				}
				if x.touches == limits.Touches {
					return errCollectionExecutionIndexLimit
				}
				// Includes slice growth and map replacement overhead. It is a
				// conservative logical charge, not allocator instrumentation.
				if err := x.charge(64); err != nil {
					return err
				}
				baseline.TouchOrdinals = append(baseline.TouchOrdinals, row.Row.Ordinal)
				x.reverse[key], x.touches = baseline, x.touches+1
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return x, nil
}

func (x *collectionExecutionIndex) charge(n int64) error {
	if n < 0 || n > x.limits.Bytes-x.bytes {
		return errCollectionExecutionIndexLimit
	}
	x.bytes += n
	return nil
}

func (x *collectionExecutionIndex) observeReverse(g CollectionPlanGuard, ordinal uint64) error {
	if g.ReverseVersion == nil {
		return nil
	}
	if previous, ok := x.reverse[g.Key]; ok {
		if !collectionExecutionSameOriginal(previous.Original, g) || *previous.Original.ReverseVersion != *g.ReverseVersion {
			return ErrCollectionPlanInvalid
		}
		previous.LastUse = ordinal
		x.reverse[g.Key] = previous
		return nil
	}
	if len(x.reverse) == x.limits.ReverseKeys {
		return errCollectionExecutionIndexLimit
	}
	if err := x.charge(512 + int64(2*(len(g.Key.Kind)+len(g.Key.ID)+len(g.OriginalUID)+len(g.OriginalRevision)))); err != nil {
		return err
	}
	copy := g
	copy.FromOrdinal = 0 // Own outcome substitution is a per-row obligation.
	token := *g.ReverseVersion
	copy.ReverseVersion = &token
	x.reverse[g.Key] = collectionExecutionReverse{Original: copy, LastUse: ordinal}
	return nil
}

func (x *collectionExecutionIndex) checkOriginal(g CollectionPlanGuard) error {
	if baseline, exists := x.reverse[g.Key]; exists && !collectionExecutionSameOriginal(baseline.Original, g) {
		return ErrCollectionPlanInvalid
	}
	return nil
}

func collectionExecutionSameOriginal(a, b CollectionPlanGuard) bool {
	return a.Key == b.Key && a.Absent == b.Absent && a.OriginalUID == b.OriginalUID &&
		a.OriginalRevision == b.OriginalRevision && a.OriginalGeneration == b.OriginalGeneration
}

func collectionExecutionCloneRow(row CollectionPlanRow) CollectionPlanRow {
	if row.Target.ReverseVersion != nil {
		copy := *row.Target.ReverseVersion
		row.Target.ReverseVersion = &copy
	}
	return row
}

func collectionExecutionHashFragment(h hash.Hash, fragment CollectionPlanFragment) error {
	raw, err := json.Marshal(fragment)
	if err != nil || len(raw) > CollectionPlanMaxFragmentBytes {
		return ErrCollectionPlanInvalid
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(raw)
	return nil
}

func (x *collectionExecutionIndex) row(ordinal uint64) (collectionExecutionRow, bool) {
	if x == nil || ordinal == 0 || ordinal > uint64(len(x.rows)) {
		return collectionExecutionRow{}, false
	}
	row := x.rows[ordinal-1]
	row.Row = collectionExecutionCloneRow(row.Row)
	return row, true
}

// rowOrdinal returns an immutable original position without scanning the plan.
// It is metadata only; a matching current state and certified outcome are still
// required before any original-plus-own version substitution.
func (x *collectionExecutionIndex) rowOrdinal(key CatalogKey) (uint64, bool) {
	if x == nil {
		return 0, false
	}
	ordinal, ok := x.rowOrdinals[key]
	return ordinal, ok
}

func (x *collectionExecutionIndex) reverseBaseline(key CatalogKey) (CollectionPlanGuard, uint64, bool) {
	if x == nil {
		return CollectionPlanGuard{}, 0, false
	}
	b, ok := x.reverse[key]
	if !ok {
		return CollectionPlanGuard{}, 0, false
	}
	copy := b.Original
	token := *copy.ReverseVersion
	copy.ReverseVersion = &token
	return copy, b.LastUse, true
}

// walkPriorTouches reports planned predecessors, not successful outcomes. Even
// when a prior touch is absent from Requires, a future acceptance command must
// require its successful original outcome, or block the row, even when no token
// substitution would otherwise be needed. Planned presence is never success.
func (x *collectionExecutionIndex) walkPriorTouches(ctx context.Context, key CatalogKey, before uint64, visit func(uint64) error) error {
	if ctx == nil || x == nil || visit == nil || before == 0 || before > uint64(len(x.rows)) {
		return ErrCollectionPlanInvalid
	}
	for _, ordinal := range x.reverse[key].TouchOrdinals {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ordinal >= before {
			break
		}
		if err := visit(ordinal); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (x *collectionExecutionIndex) matches(s CollectionState) bool {
	if x == nil || x.auditOnly || !collectionExecutionEligible(s) || collectionPlanFence(s) != x.binding || s.Validation.Header.ResultID != x.resultID ||
		s.Validation.Descriptor != x.result || s.Validation.Header.CapabilitiesDigest != x.capabilities {
		return false
	}
	activation := ""
	if s.Activation != nil {
		activation = s.Activation.ID
	}
	return activation == x.activationID
}

// walkRow checks the inclusive range digest and original row-end digest while
// streaming one detached <=1MiB codec fragment at a time. Callback observations
// are provisional until success; callbacks must not install mutations. This
// remains true after a >4MiB logical row and on cancellation or corrupt input.
func (x *collectionExecutionIndex) walkRow(ctx context.Context, view *collectionLedgerView, ordinal uint64, visit func(CollectionPlanFragment) error) error {
	if ctx == nil || view == nil || visit == nil {
		return ErrCollectionPlanInvalid
	}
	row, found := x.row(ordinal)
	if !found {
		return ErrCollectionPlanInvalid
	}
	h := sha256.New()
	for partOrdinal := row.FirstFragment; partOrdinal <= row.EndFragment; partOrdinal++ {
		part, err := collectionExecutionPart(ctx, view, x.binding.OperationID, partOrdinal)
		if err != nil {
			return err
		}
		if partOrdinal == row.FirstFragment && (part.Fragment.Row == nil || part.Fragment.Row.Ordinal != ordinal) ||
			partOrdinal == row.EndFragment && (part.Fragment.End == nil || part.Fragment.End.Ordinal != ordinal || part.Fragment.End.Digest != row.RowDigest) {
			return ErrCollectionPlanInvalid
		}
		if err := collectionExecutionHashFragment(h, part.Fragment); err != nil {
			return err
		}
		if err := visit(part.Fragment); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != row.RangeDigest {
		return ErrCollectionPlanInvalid
	}
	return ctx.Err()
}
