package persistence

import (
	"context"
	"errors"
)

var (
	errCollectionItemProgressIncomplete = errors.New("collection predecessor progress is incomplete")
	errCollectionItemGuardLimit         = errors.New("collection item guard verification limit exceeded")
)

type collectionItemGuardCode string

const (
	collectionItemGuardsAllowed           collectionItemGuardCode = "allowed"
	collectionItemGuardsConflict          collectionItemGuardCode = "conflict"
	collectionItemGuardsDependencyBlocked collectionItemGuardCode = "dependencyBlocked"
)

// A zero decision accompanying an error is never a terminal item result.
type collectionItemGuardDecision struct{ Code collectionItemGuardCode }

// collectionCertifiedItem is a private adapter view of an ALREADY COMMITTED
// predecessor. It is neither a persisted format nor a client assertion. The
// future authoritative outcome reader must verify its own committed prefix,
// checksums and execution protocol before returning this value. This helper
// additionally checks its exact original activation/plan/row binding.
// MutationSequence certifies references touched by an accepted mutation; it
// does not describe the changed resource's own incoming-reference version.
type collectionCertifiedItem struct {
	OperationID, ActivationID, PlanID, PlanDigest string
	Ordinal                                       uint64
	RowDigest                                     string
	Key                                           CatalogKey
	Decision                                      string // accepted, unchanged, conflict, dependencyBlocked
	UID, Revision                                 string
	Generation, MutationSequence                  uint64
}

// Lookups expose one immutable, caller-owned observation at a time. They must
// refer to one unchanged authoritative catalog/outcome generation. Returned
// payloads are never read, decrypted, retained or included in diagnostics.
type collectionItemCatalogLookup func(context.Context, CatalogKey) (CatalogRecord, bool, error)
type collectionItemPredecessorLookup func(context.Context, uint64) (collectionCertifiedItem, bool, error)

type collectionItemGuardLimits struct {
	Keys, Outcomes int
	Bytes          int64
	Work           uint64
}

func defaultCollectionItemGuardLimits() collectionItemGuardLimits {
	return collectionItemGuardLimits{Keys: 10_000, Outcomes: 10_000, Bytes: 32 << 20, Work: 1_000_000}
}

func (l collectionItemGuardLimits) valid() bool {
	m := defaultCollectionItemGuardLimits()
	return l.Keys > 0 && l.Keys <= m.Keys && l.Outcomes > 0 && l.Outcomes <= m.Outcomes &&
		l.Bytes > 0 && l.Bytes <= m.Bytes && l.Work > 0 && l.Work <= m.Work
}

type collectionItemGuardVerifier struct {
	ctx          context.Context
	index        *collectionExecutionIndex
	ordinal      uint64
	current      collectionItemCatalogLookup
	predecessors collectionItemPredecessorLookup
	limits       collectionItemGuardLimits
	work         uint64
	bytes        int64
	decision     collectionItemGuardCode
	outcomes     map[uint64]collectionCertifiedItem
}

// verifyCollectionItemGuards checks the original sealed row against one
// unchanged caller-owned observation. The caller must first match the index to
// the exact current original state; a disposable index is not authority. The
// desired encrypted record must separately be authenticated and prepared from
// the ORIGINAL input, never a freshly rebased Catalog.Prepare result.
//
// This function grants no authorization, reserves no handle, and publishes no
// mutation or outcome. A decision is returned only after complete range-digest
// verification. Close the caller's frozen ledger view before a later write.
// The future atomic command must retain/recheck the catalog and outcome fence;
// success here cannot be carried across an intervening mutation or restoration.
func verifyCollectionItemGuards(ctx context.Context, x *collectionExecutionIndex, view *collectionLedgerView,
	ordinal uint64, desired CatalogRecord, current collectionItemCatalogLookup, predecessors collectionItemPredecessorLookup,
	limits collectionItemGuardLimits) (collectionItemGuardDecision, error) {
	if ctx == nil || x == nil || view == nil || current == nil || predecessors == nil || !limits.valid() || !validOperationEpoch(x.activationID) {
		return collectionItemGuardDecision{}, ErrCollectionPlanInvalid
	}
	if err := ctx.Err(); err != nil {
		return collectionItemGuardDecision{}, err
	}
	row, exists := x.row(ordinal)
	if !exists || desired.Key != row.Row.Key || desired.Removed || !catalogIdentifier(desired.UID, 256) ||
		!catalogIdentifier(desired.Revision, 256) || desired.Generation == 0 {
		return collectionItemGuardDecision{}, ErrCollectionPlanInvalid
	}
	v := &collectionItemGuardVerifier{ctx: ctx, index: x, ordinal: ordinal, current: current, predecessors: predecessors,
		limits: limits, decision: collectionItemGuardsAllowed, outcomes: make(map[uint64]collectionCertifiedItem)}
	old, active, err := v.catalog(desired.Key)
	if err != nil {
		return collectionItemGuardDecision{}, err
	}
	if err := v.guard(row.Row.Target); err != nil {
		return collectionItemGuardDecision{}, err
	}
	// Target identity is always original. Only an explicit guard FromOrdinal
	// permits adopting a successful own resource version.
	switch row.Row.Change {
	case "create":
		if desired.Generation != 1 {
			return collectionItemGuardDecision{}, ErrCollectionPlanInvalid
		}
	case "update":
		if desired.UID != row.Row.Target.OriginalUID || desired.Revision == row.Row.Target.OriginalRevision ||
			desired.Generation < uint64(row.Row.Target.OriginalGeneration) || desired.Generation-uint64(row.Row.Target.OriginalGeneration) > 1 {
			return collectionItemGuardDecision{}, ErrCollectionPlanInvalid
		}
	case "unchanged":
		if desired.UID != row.Row.Target.OriginalUID || desired.Revision != row.Row.Target.OriginalRevision ||
			desired.Generation != uint64(row.Row.Target.OriginalGeneration) {
			return collectionItemGuardDecision{}, ErrCollectionPlanInvalid
		}
	default:
		return collectionItemGuardDecision{}, ErrCollectionPlanInvalid
	}
	actual := make(map[CatalogKey]struct{})
	add := func(refs []CatalogKey) error {
		for _, key := range refs {
			if err := v.spend(1); err != nil {
				return err
			}
			if key.validate() != nil || key == desired.Key {
				return ErrCollectionPlanInvalid
			}
			if _, ok := actual[key]; ok {
				continue
			}
			if len(actual) == limits.Keys {
				return errCollectionItemGuardLimit
			}
			if err := v.reserve(192 + int64(len(key.Kind)+len(key.ID))); err != nil {
				return err
			}
			actual[key] = struct{}{}
		}
		return nil
	}
	if active {
		if err := add(old.References); err != nil {
			return collectionItemGuardDecision{}, err
		}
	}
	if row.Row.Change == "unchanged" {
		// An unchanged row has no mutation Touches. Check its proposed reference
		// identity separately so it cannot smuggle a changed outgoing edge.
		if len(actual) != len(desired.References) {
			v.conflict()
		}
		for _, key := range desired.References {
			if err := v.spend(1); err != nil {
				return collectionItemGuardDecision{}, err
			}
			if _, ok := actual[key]; !ok {
				v.conflict()
			} else {
				delete(actual, key)
			}
		}
		if len(actual) != 0 {
			v.conflict()
		}
		clear(actual)
	} else if err := add(desired.References); err != nil {
		return collectionItemGuardDecision{}, err
	}
	err = x.walkRow(ctx, view, ordinal, func(fragment CollectionPlanFragment) error {
		if err := v.spend(1); err != nil {
			return err
		}
		for _, guard := range fragment.Guards {
			if err := v.guard(guard); err != nil {
				return err
			}
		}
		for _, required := range fragment.Requires {
			if _, err := v.predecessor(required); err != nil {
				return err
			}
		}
		for _, key := range fragment.Touches {
			if err := v.spend(1); err != nil {
				return err
			}
			if _, ok := actual[key]; !ok {
				v.conflict()
			} else {
				delete(actual, key)
			}
			baseline, last, guarded := x.reverseBaseline(key)
			if guarded && last > ordinal {
				if err := v.beforeTouch(baseline); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return collectionItemGuardDecision{}, collectionItemGuardError(err)
	}
	if len(actual) != 0 {
		v.conflict()
	}
	if err := ctx.Err(); err != nil {
		return collectionItemGuardDecision{}, err
	}
	return collectionItemGuardDecision{Code: v.decision}, nil
}

func (v *collectionItemGuardVerifier) spend(n uint64) error {
	if n > v.limits.Work-v.work {
		return errCollectionItemGuardLimit
	}
	v.work += n
	return v.ctx.Err()
}

func (v *collectionItemGuardVerifier) reserve(n int64) error {
	if n < 0 || n > v.limits.Bytes-v.bytes {
		return errCollectionItemGuardLimit
	}
	v.bytes += n
	return nil
}

func (v *collectionItemGuardVerifier) conflict() {
	if v.decision != collectionItemGuardsDependencyBlocked {
		v.decision = collectionItemGuardsConflict
	}
}

func (v *collectionItemGuardVerifier) catalog(key CatalogKey) (CatalogRecord, bool, error) {
	if err := v.spend(1); err != nil {
		return CatalogRecord{}, false, err
	}
	record, exists, err := v.current(v.ctx, key)
	if err != nil {
		return CatalogRecord{}, false, collectionItemGuardError(err)
	}
	if err := v.ctx.Err(); err != nil {
		return CatalogRecord{}, false, err
	}
	if exists && record.Key != key {
		return CatalogRecord{}, false, ErrCollectionPlanInvalid
	}
	return record, exists && !record.Removed, nil
}

func (v *collectionItemGuardVerifier) predecessor(ordinal uint64) (collectionCertifiedItem, error) {
	if err := v.spend(1); err != nil {
		return collectionCertifiedItem{}, err
	}
	if ordinal == 0 || ordinal >= v.ordinal {
		return collectionCertifiedItem{}, ErrCollectionPlanInvalid
	}
	if cached, ok := v.outcomes[ordinal]; ok {
		return cached, nil
	}
	row, exists := v.index.row(ordinal)
	if !exists {
		return collectionCertifiedItem{}, ErrCollectionPlanInvalid
	}
	item, present, err := v.predecessors(v.ctx, ordinal)
	if err != nil {
		return collectionCertifiedItem{}, collectionItemGuardError(err)
	}
	if err := v.ctx.Err(); err != nil {
		return collectionCertifiedItem{}, err
	}
	if !present {
		return collectionCertifiedItem{}, errCollectionItemProgressIncomplete
	}
	if item.OperationID != v.index.binding.OperationID || item.ActivationID != v.index.activationID ||
		item.PlanID != v.index.binding.PlanID || item.PlanDigest != v.index.binding.Descriptor.Digest ||
		item.Ordinal != ordinal || item.RowDigest != row.RowDigest || item.Key != row.Row.Key {
		return collectionCertifiedItem{}, ErrCollectionPlanInvalid
	}
	switch item.Decision {
	case "accepted":
		if row.Row.Change == "unchanged" || !catalogIdentifier(item.UID, 256) || !catalogIdentifier(item.Revision, 256) || item.MutationSequence == 0 ||
			row.Row.Change == "create" && item.Generation != 1 ||
			row.Row.Change == "update" && (item.UID != row.Row.Target.OriginalUID || item.Revision == row.Row.Target.OriginalRevision ||
				item.Generation < uint64(row.Row.Target.OriginalGeneration) || item.Generation-uint64(row.Row.Target.OriginalGeneration) > 1) {
			return collectionCertifiedItem{}, ErrCollectionPlanInvalid
		}
	case "unchanged":
		if row.Row.Change != "unchanged" || item.UID != row.Row.Target.OriginalUID || item.Revision != row.Row.Target.OriginalRevision ||
			item.Generation != uint64(row.Row.Target.OriginalGeneration) || item.MutationSequence != 0 {
			return collectionCertifiedItem{}, ErrCollectionPlanInvalid
		}
	case "conflict", "dependencyBlocked":
		if item.UID != "" || item.Revision != "" || item.Generation != 0 || item.MutationSequence != 0 {
			return collectionCertifiedItem{}, ErrCollectionPlanInvalid
		}
		v.decision = collectionItemGuardsDependencyBlocked
	default:
		return collectionCertifiedItem{}, ErrCollectionPlanInvalid
	}
	if len(v.outcomes) == v.limits.Outcomes {
		return collectionCertifiedItem{}, errCollectionItemGuardLimit
	}
	if err := v.reserve(1024 + int64(len(item.UID)+len(item.Revision)+len(item.Key.Kind)+len(item.Key.ID))); err != nil {
		return collectionCertifiedItem{}, err
	}
	v.outcomes[ordinal] = item
	return item, nil
}

func collectionItemSuccessful(item collectionCertifiedItem) bool {
	return item.Decision == "accepted" || item.Decision == "unchanged"
}

// originalToken proves every earlier planned touch succeeded, even when no
// explicit Requires mentions it. Missing outcomes are incomplete progress;
// known failures block the item rather than falling back to an older token.
func (v *collectionItemGuardVerifier) originalToken(g CollectionPlanGuard) (uint64, bool, error) {
	if g.ReverseVersion == nil {
		return 0, false, ErrCollectionPlanInvalid
	}
	token, complete := *g.ReverseVersion, true
	err := v.index.walkPriorTouches(v.ctx, g.Key, v.ordinal, func(ordinal uint64) error {
		item, err := v.predecessor(ordinal)
		if err != nil {
			return err
		}
		if !collectionItemSuccessful(item) {
			complete = false
			return nil
		}
		if item.Decision != "accepted" || item.MutationSequence <= token {
			return ErrCollectionPlanInvalid
		}
		token = item.MutationSequence
		return nil
	})
	return token, complete, collectionItemGuardError(err)
}

func (v *collectionItemGuardVerifier) guard(g CollectionPlanGuard) error {
	if err := v.spend(1); err != nil {
		return err
	}
	uid, revision, generation := g.OriginalUID, g.OriginalRevision, uint64(g.OriginalGeneration)
	expectAbsent, complete := g.Absent, true
	if g.FromOrdinal != 0 {
		item, err := v.predecessor(g.FromOrdinal)
		if err != nil {
			return err
		}
		if item.Key != g.Key {
			return ErrCollectionPlanInvalid
		}
		if !collectionItemSuccessful(item) {
			complete = false
		} else {
			uid, revision, generation, expectAbsent = item.UID, item.Revision, item.Generation, false
		}
	}
	var token uint64
	if g.ReverseVersion != nil {
		value, success, err := v.originalToken(g)
		if err != nil {
			return err
		}
		token, complete = value, complete && success
	}
	current, active, err := v.catalog(g.Key)
	if err != nil {
		return err
	}
	if !complete {
		return nil
	}
	if expectAbsent {
		if active {
			v.conflict()
		}
	} else if !active || current.UID != uid || current.Revision != revision || current.Generation != generation ||
		g.ReverseVersion != nil && current.DependentsVersion != token {
		v.conflict()
	}
	return nil
}

func (v *collectionItemGuardVerifier) beforeTouch(g CollectionPlanGuard) error {
	// This is a before-overwrite baseline check, not the current row Target.
	// Its resource configuration may legitimately be an earlier included write.
	if ordinal, included := v.index.rowOrdinal(g.Key); included && ordinal < v.ordinal {
		item, err := v.predecessor(ordinal)
		if err != nil {
			return err
		}
		if !collectionItemSuccessful(item) {
			return nil
		}
		g.FromOrdinal = ordinal
	}
	return v.guard(g)
}

// Never forward arbitrary provider/parser/adapter diagnostics. Known safe
// sentinel classes preserve cancellation, integrity, bounded work and progress.
func collectionItemGuardError(err error) error {
	if err == nil {
		return nil
	}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, errCollectionItemProgressIncomplete,
		errCollectionItemGuardLimit, ErrCollectionPlanInvalid, errCollectionExecutionIndexLimit} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, errCollectionLedgerCorrupt) {
		return ErrCollectionPlanInvalid
	}
	return ErrCollectionUnavailable
}
