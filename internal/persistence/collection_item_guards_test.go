package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These are guard-protocol tests over real staged/sealed encrypted inputs and
// immutable plans. The synthetic current catalog/outcome adapter deliberately
// does not claim that a resource driver or atomic item command ran.
type itemGuardTestRow struct {
	row      CollectionPlanRow
	guards   []CollectionPlanGuard
	requires []uint64
	touches  []CatalogKey
}

func itemGuardCustomPlan(t *testing.T, count int, build func([]CollectionItem) []itemGuardTestRow) (*Store, CollectionState) {
	t.Helper()
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, count)
	inputs, err := s.fsm.collections.Page(head.ID, 0, 256)
	if err != nil || len(inputs) != count {
		t.Fatal("input fixture", err)
	}
	rows := build(inputs)
	header := planCodecHeader(head.ItemCount)
	header.OperationID, header.UploadID, header.Actor = head.ID, head.UploadID, head.Actor
	header.IdentityFormat, header.ContentDigest, header.InputProgressDigest = head.IdentityFormat, head.ContentDigest, head.ProgressDigest
	s.fsm.mu.RLock()
	header.ObservedIndex = s.fsm.image.Index
	s.fsm.mu.RUnlock()
	var encoded bytes.Buffer
	desc, err := EncodeCollectionPlan(context.Background(), &encoded, header, func(e *CollectionPlanEncoder) error {
		for _, spec := range rows {
			spec.row.GuardCount, spec.row.RequiresCount, spec.row.TouchesCount = uint64(len(spec.guards)), uint64(len(spec.requires)), uint64(len(spec.touches))
			if err := e.BeginRow(spec.row); err != nil {
				return err
			}
			if len(spec.guards) != 0 {
				if err := e.WriteGuards(spec.guards); err != nil {
					return err
				}
			}
			if len(spec.requires) != 0 {
				if err := e.WriteRequires(spec.requires); err != nil {
					return err
				}
			}
			if len(spec.touches) != 0 {
				if err := e.WriteTouches(spec.touches); err != nil {
					return err
				}
			}
			if err := e.EndRow(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("plan fixture", err)
	}
	var parts []CollectionPlanLedgerFragment
	if _, err := DecodeCollectionPlan(context.Background(), bytes.NewReader(encoded.Bytes()), func(f CollectionPlanFragment) error {
		parts = append(parts, CollectionPlanLedgerFragment{Ordinal: uint64(len(parts) + 1), Fragment: f})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	head = planApplyAll(t, s, planApplyBegin(t, s, head, CollectionPlanBegin{Header: header, Descriptor: desc}), parts)
	proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &proof}, head.ActivityAt.Add(time.Millisecond)))
	begin := CollectionValidationBegin{Header: CollectionValidationHeader{ResultID: uuid.NewString(), OperationID: head.ID, UploadID: head.UploadID,
		InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount, Authority: authority, CapabilitiesDigest: strings.Repeat("c", 64),
		Valid: true, PlanID: header.PlanID, PlanDigest: desc.Digest}, Descriptor: CollectionValidationDescriptor{Digest: CollectionValidationInitialDigest()}}
	byInput := map[uint64]CollectionPlanRow{}
	for _, spec := range rows {
		byInput[spec.row.InputOrdinal] = spec.row
	}
	var results []CollectionValidationItem
	for _, input := range inputs {
		row := byInput[input.Ordinal]
		item := CollectionValidationItem{Ordinal: input.Ordinal, Key: input.Key, Source: input.Source, Document: input.SourceDocument, Item: input.SourceItem,
			Change: row.Change, UID: row.Target.OriginalUID, ResourceVersion: row.Target.OriginalRevision}
		digest, cost, err := CollectionValidationNextDigest(begin.Descriptor.Digest, item)
		if err != nil {
			t.Fatal(err)
		}
		begin.Descriptor.Count++
		begin.Descriptor.Bytes += cost
		begin.Descriptor.Digest = digest
		results = append(results, item)
	}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, results))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	}
	return s, head
}

func itemGuardIndex(t *testing.T, s *Store, head CollectionState) (*collectionExecutionIndex, *collectionLedgerView) {
	t.Helper()
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	at := head.ActivityAt.Add(time.Millisecond)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, at), at))
	view := executionIndexView(t, s)
	x, err := buildCollectionExecutionIndex(context.Background(), head, view, defaultCollectionExecutionIndexLimits())
	if err != nil {
		t.Fatal(err)
	}
	return x, view
}

func itemGuardOriginal(row CollectionPlanRow) CatalogRecord {
	return CatalogRecord{Key: row.Key, UID: row.Target.OriginalUID, Revision: row.Target.OriginalRevision, Generation: uint64(row.Target.OriginalGeneration),
		DependentsVersion: func() uint64 {
			if row.Target.ReverseVersion != nil {
				return *row.Target.ReverseVersion
			}
			return 0
		}()}
}

func itemGuardDesired(row CollectionPlanRow) CatalogRecord {
	r := itemGuardOriginal(row)
	if row.Change == "create" {
		r.UID, r.Revision, r.Generation = "created-uid", "created-revision", 1
	}
	if row.Change == "update" {
		r.Revision += "-accepted"
		r.Generation++
	}
	return r
}

func itemGuardCertified(x *collectionExecutionIndex, ordinal, token uint64) collectionCertifiedItem {
	r, _ := x.row(ordinal)
	desired := itemGuardDesired(r.Row)
	decision := "accepted"
	if r.Row.Change == "unchanged" {
		decision = "unchanged"
		token = 0
	}
	return collectionCertifiedItem{OperationID: x.binding.OperationID, ActivationID: x.activationID, PlanID: x.binding.PlanID, PlanDigest: x.binding.Descriptor.Digest,
		Ordinal: ordinal, RowDigest: r.RowDigest, Key: r.Row.Key, Decision: decision, UID: desired.UID, Revision: desired.Revision, Generation: desired.Generation, MutationSequence: token}
}

func itemGuardCatalog(records map[CatalogKey]CatalogRecord) collectionItemCatalogLookup {
	return func(_ context.Context, key CatalogKey) (CatalogRecord, bool, error) {
		r, ok := records[key]
		return r, ok, nil
	}
}
func itemGuardPredecessors(records map[uint64]collectionCertifiedItem) collectionItemPredecessorLookup {
	return func(_ context.Context, n uint64) (collectionCertifiedItem, bool, error) {
		r, ok := records[n]
		return r, ok, nil
	}
}

func TestCollectionItemGuardsIndexOrdinalLookupIsOriginalAndBounded(t *testing.T) {
	s, head := executionIndexFixture(t, false, "normal")
	x, view := itemGuardIndex(t, s, head)
	for ordinal := uint64(1); ordinal <= uint64(len(x.rows)); ordinal++ {
		row, _ := x.row(ordinal)
		got, exists := x.rowOrdinal(row.Row.Key)
		if !exists || got != ordinal {
			t.Fatal("original execution ordinal lookup changed")
		}
		row.Row.Key.ID = "mutated-detached-copy"
		if _, exists := x.rowOrdinal(row.Row.Key); exists {
			t.Fatal("lookup aliases detached row mutation")
		}
	}
	if len(x.rowOrdinals) != len(x.rows) {
		t.Fatal("lookup retains non-row keys")
	}
	if _, exists := x.rowOrdinal(CatalogKey{Kind: "Credential", ID: "missing"}); exists {
		t.Fatal("invented original row")
	}
	var absent *collectionExecutionIndex
	if _, exists := absent.rowOrdinal(CatalogKey{}); exists {
		t.Fatal("nil lookup invented row")
	}
	// The extra map consumes the original index budget exactly once, during
	// construction, rather than every item's work/retention allowance.
	limits := defaultCollectionExecutionIndexLimits()
	limits.Bytes = x.bytes - 1
	s.fsm.mu.RLock()
	current := s.fsm.image.Collections[head.ID].Clone()
	s.fsm.mu.RUnlock()
	if _, err := buildCollectionExecutionIndex(context.Background(), current, view, limits); !errors.Is(err, errCollectionExecutionIndexLimit) {
		t.Fatal("lookup metadata escaped index budget", err)
	}
	// An independent one-create-row fixture has no reverse/touch metadata.
	// Assert the reviewed extra lookup charge explicitly: a self-derived
	// x.bytes-1 bound alone would pass even if that charge were deleted.
	oneStore, oneHead := itemGuardCustomPlan(t, 1, func(input []CollectionItem) []itemGuardTestRow {
		return []itemGuardTestRow{{row: planCodecRow(1, 1, input[0].Key, "create")}}
	})
	one, _ := itemGuardIndex(t, oneStore, oneHead)
	oneRow, _ := one.row(1)
	expected := int64(2048 + 32 + 256 + len(oneRow.Row.Source) + len(oneRow.Row.Key.Kind) + len(oneRow.Row.Key.ID))
	if one.bytes != expected {
		t.Fatal("original row did not pay its separate 32-byte input hash and 256-byte lookup charges")
	}
}

func TestCollectionItemGuardsOriginalTargetsAndCertifiedTouchChain(t *testing.T) {
	s, head := executionIndexFixture(t, false, "normal")
	x, view := itemGuardIndex(t, s, head)
	row, _ := x.row(3)
	original := itemGuardOriginal(row.Row)
	original.DependentsVersion = 51
	base := map[uint64]collectionCertifiedItem{1: itemGuardCertified(x, 1, 50), 2: itemGuardCertified(x, 2, 51)}
	before, _ := json.Marshal(s.fsm.image)
	for _, tc := range []struct {
		name string
		edit func(map[uint64]collectionCertifiedItem, *CatalogRecord)
		code collectionItemGuardCode
		err  error
	}{
		{name: "exact own touches", code: collectionItemGuardsAllowed},
		{name: "outside later touch", edit: func(_ map[uint64]collectionCertifiedItem, r *CatalogRecord) { r.DependentsVersion = 52 }, code: collectionItemGuardsConflict},
		{name: "target revision never rebased", edit: func(_ map[uint64]collectionCertifiedItem, r *CatalogRecord) { r.Revision = "outside-version" }, code: collectionItemGuardsConflict},
		{name: "target incarnation never rebased", edit: func(_ map[uint64]collectionCertifiedItem, r *CatalogRecord) { r.UID = "outside-uid" }, code: collectionItemGuardsConflict},
		{name: "failed removal blocks despite no Requires", edit: func(m map[uint64]collectionCertifiedItem, r *CatalogRecord) {
			a := m[1]
			a.Decision = "conflict"
			a.UID, a.Revision, a.Generation, a.MutationSequence = "", "", 0, 0
			m[1] = a
			r.DependentsVersion = 11
		}, code: collectionItemGuardsDependencyBlocked},
		{name: "blocked predecessor", edit: func(m map[uint64]collectionCertifiedItem, _ *CatalogRecord) {
			a := m[2]
			a.Decision = "dependencyBlocked"
			a.UID, a.Revision, a.Generation, a.MutationSequence = "", "", 0, 0
			m[2] = a
		}, code: collectionItemGuardsDependencyBlocked},
		{name: "missing is not an attempted failure", edit: func(m map[uint64]collectionCertifiedItem, _ *CatalogRecord) { delete(m, 1) }, err: errCollectionItemProgressIncomplete},
		{name: "failed outcome carries success fields", edit: func(m map[uint64]collectionCertifiedItem, _ *CatalogRecord) {
			a := m[1]
			a.Decision = "conflict"
			m[1] = a
		}, err: ErrCollectionPlanInvalid},
		{name: "foreign activation", edit: func(m map[uint64]collectionCertifiedItem, _ *CatalogRecord) {
			a := m[1]
			a.ActivationID = uuid.NewString()
			m[1] = a
		}, err: ErrCollectionPlanInvalid},
		{name: "foreign row digest", edit: func(m map[uint64]collectionCertifiedItem, _ *CatalogRecord) {
			a := m[1]
			a.RowDigest = strings.Repeat("f", 64)
			m[1] = a
		}, err: ErrCollectionPlanInvalid},
		{name: "same-entry token reused", edit: func(m map[uint64]collectionCertifiedItem, _ *CatalogRecord) {
			a := m[2]
			a.MutationSequence = 50
			m[2] = a
		}, err: ErrCollectionPlanInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcomes := map[uint64]collectionCertifiedItem{1: base[1], 2: base[2]}
			current := original
			if tc.edit != nil {
				tc.edit(outcomes, &current)
			}
			decision, err := verifyCollectionItemGuards(context.Background(), x, view, 3, itemGuardDesired(row.Row), itemGuardCatalog(map[CatalogKey]CatalogRecord{row.Row.Key: current}), itemGuardPredecessors(outcomes), defaultCollectionItemGuardLimits())
			if !errors.Is(err, tc.err) || decision.Code != tc.code {
				t.Fatalf("decision %q error %v", decision.Code, err)
			}
		})
	}
	after, _ := json.Marshal(s.fsm.image)
	if !bytes.Equal(before, after) || len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Operations) != 0 {
		t.Fatal("pure guard verification changed durable state")
	}
}

func TestCollectionItemGuardsExplicitOwnVersionAndTouchUnion(t *testing.T) {
	s, head := executionIndexFixture(t, false, "normal")
	x, view := itemGuardIndex(t, s, head)
	first, _ := x.row(1)
	second, _ := x.row(2)
	third, _ := x.row(3)
	a := itemGuardDesired(first.Row)
	a.DependentsVersion = *first.Row.Target.ReverseVersion
	b := itemGuardOriginal(second.Row)
	b.References = []CatalogKey{third.Row.Key}
	c := itemGuardOriginal(third.Row)
	c.DependentsVersion = 50
	outcomes := map[uint64]collectionCertifiedItem{1: itemGuardCertified(x, 1, 50)}
	for _, tc := range []struct {
		name string
		edit func(map[CatalogKey]CatalogRecord, *CatalogRecord)
		want collectionItemGuardCode
	}{
		{name: "explicit FromOrdinal", want: collectionItemGuardsAllowed},
		{name: "old live bytes cannot satisfy own version", edit: func(m map[CatalogKey]CatalogRecord, _ *CatalogRecord) {
			r := m[a.Key]
			r.Revision = first.Row.Target.OriginalRevision
			m[a.Key] = r
		}, want: collectionItemGuardsConflict},
		{name: "own config preserves incoming token", edit: func(m map[CatalogKey]CatalogRecord, _ *CatalogRecord) {
			r := m[a.Key]
			r.DependentsVersion = 50
			m[a.Key] = r
		}, want: collectionItemGuardsConflict},
		{name: "outside reverse touch before overwrite", edit: func(m map[CatalogKey]CatalogRecord, _ *CatalogRecord) {
			r := m[c.Key]
			r.DependentsVersion = 49
			m[c.Key] = r
		}, want: collectionItemGuardsConflict},
		{name: "missing actual old reference", edit: func(m map[CatalogKey]CatalogRecord, _ *CatalogRecord) {
			r := m[b.Key]
			r.References = nil
			m[b.Key] = r
		}, want: collectionItemGuardsConflict},
		{name: "unexpected desired reference", edit: func(_ map[CatalogKey]CatalogRecord, r *CatalogRecord) {
			r.References = []CatalogKey{{Kind: "Credential", ID: "unexpected"}}
		}, want: collectionItemGuardsConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := map[CatalogKey]CatalogRecord{a.Key: a, b.Key: b, c.Key: c}
			desired := itemGuardDesired(second.Row)
			if tc.edit != nil {
				tc.edit(catalog, &desired)
			}
			got, err := verifyCollectionItemGuards(context.Background(), x, view, 2, desired, itemGuardCatalog(catalog), itemGuardPredecessors(outcomes), defaultCollectionItemGuardLimits())
			if err != nil || got.Code != tc.want {
				t.Fatalf("decision %q error %v", got.Code, err)
			}
		})
	}
}

func TestCollectionItemGuardsBeforeOverwriteAndLastUse(t *testing.T) {
	// A generalized original plan: row1 removes the edge to row2, without a
	// forward guard on its old target. The later target supplies the baseline.
	s, head := itemGuardCustomPlan(t, 2, func(input []CollectionItem) []itemGuardTestRow {
		a := planCodecRow(1, 1, input[0].Key, "update")
		b := planCodecRow(2, 2, input[1].Key, "update")
		*a.Target.ReverseVersion, *b.Target.ReverseVersion = 3, 11
		return []itemGuardTestRow{{row: a, touches: []CatalogKey{b.Key}}, {row: b}}
	})
	x, view := itemGuardIndex(t, s, head)
	first, _ := x.row(1)
	second, _ := x.row(2)
	a := itemGuardOriginal(first.Row)
	a.References = []CatalogKey{second.Row.Key}
	b := itemGuardOriginal(second.Row)
	for _, token := range []uint64{11, 12} {
		b.DependentsVersion = token
		got, err := verifyCollectionItemGuards(context.Background(), x, view, 1, itemGuardDesired(first.Row), itemGuardCatalog(map[CatalogKey]CatalogRecord{a.Key: a, b.Key: b}), itemGuardPredecessors(nil), defaultCollectionItemGuardLimits())
		want := collectionItemGuardsAllowed
		if token != 11 {
			want = collectionItemGuardsConflict
		}
		if err != nil || got.Code != want {
			t.Fatal("before-overwrite baseline", got, err)
		}
	}
	// A different plan no longer needs the reverse condition after row1. The
	// later row may touch it without an unnecessary historical token fence.
	s2, head2 := itemGuardCustomPlan(t, 2, func(input []CollectionItem) []itemGuardTestRow {
		a := planCodecRow(1, 1, input[0].Key, "update")
		b := planCodecRow(2, 2, input[1].Key, "update")
		return []itemGuardTestRow{{row: a}, {row: b, touches: []CatalogKey{a.Key}}}
	})
	x2, v2 := itemGuardIndex(t, s2, head2)
	r1, _ := x2.row(1)
	r2, _ := x2.row(2)
	one := itemGuardDesired(r1.Row)
	one.DependentsVersion = 999
	two := itemGuardOriginal(r2.Row)
	two.References = []CatalogKey{one.Key}
	got, err := verifyCollectionItemGuards(context.Background(), x2, v2, 2, itemGuardDesired(r2.Row), itemGuardCatalog(map[CatalogKey]CatalogRecord{one.Key: one, two.Key: two}), itemGuardPredecessors(nil), defaultCollectionItemGuardLimits())
	if err != nil || got.Code != collectionItemGuardsAllowed {
		t.Fatal("completed reverse obligations blocked independent row", got, err)
	}
}

func TestCollectionItemGuardsUnchangedDependencyAndReferenceIdentity(t *testing.T) {
	s, head := itemGuardCustomPlan(t, 2, func(input []CollectionItem) []itemGuardTestRow {
		a := planCodecRow(1, 1, input[0].Key, "unchanged")
		b := planCodecRow(2, 2, input[1].Key, "update")
		return []itemGuardTestRow{{row: a}, {row: b, guards: []CollectionPlanGuard{a.Target}, requires: []uint64{1}}}
	})
	x, view := itemGuardIndex(t, s, head)
	first, _ := x.row(1)
	second, _ := x.row(2)
	a, b := itemGuardOriginal(first.Row), itemGuardOriginal(second.Row)
	lookup := itemGuardCatalog(map[CatalogKey]CatalogRecord{a.Key: a, b.Key: b})
	for _, present := range []bool{false, true} {
		outcomes := map[uint64]collectionCertifiedItem{}
		if present {
			outcomes[1] = itemGuardCertified(x, 1, 0)
		}
		got, err := verifyCollectionItemGuards(context.Background(), x, view, 2, itemGuardDesired(second.Row), lookup, itemGuardPredecessors(outcomes), defaultCollectionItemGuardLimits())
		if present {
			if err != nil || got.Code != collectionItemGuardsAllowed {
				t.Fatal(got, err)
			}
		} else if !errors.Is(err, errCollectionItemProgressIncomplete) || got.Code != "" {
			t.Fatal("unchanged live fallback", got, err)
		}
	}
	a.References = []CatalogKey{{Kind: "Credential", ID: "left"}, {Kind: "Credential", ID: "right"}}
	desired := a.Clone()
	desired.References = []CatalogKey{a.References[0], a.References[0]}
	got, err := verifyCollectionItemGuards(context.Background(), x, view, 1, desired, itemGuardCatalog(map[CatalogKey]CatalogRecord{a.Key: a}), itemGuardPredecessors(nil), defaultCollectionItemGuardLimits())
	if err != nil || got.Code != collectionItemGuardsConflict {
		t.Fatal("duplicate references concealed removed unchanged edge", got, err)
	}
	// Unchanged needs no prepared encrypted body. If its original target is
	// absent, the caller supplies just the original key/tuple and nil references.
	got, err = verifyCollectionItemGuards(context.Background(), x, view, 1, itemGuardDesired(first.Row), itemGuardCatalog(nil), itemGuardPredecessors(nil), defaultCollectionItemGuardLimits())
	if err != nil || got.Code != collectionItemGuardsConflict {
		t.Fatal("missing unchanged target required a nonexistent prepared body", got, err)
	}
}

func TestCollectionItemGuardsBeforeTouchOwnConfigurationIsNotItsToken(t *testing.T) {
	s, head := itemGuardCustomPlan(t, 3, func(input []CollectionItem) []itemGuardTestRow {
		a := planCodecRow(1, 1, input[0].Key, "update")
		b := planCodecRow(2, 2, input[1].Key, "update")
		c := planCodecRow(3, 3, input[2].Key, "update")
		guard := a.Target
		guard.FromOrdinal = 1
		return []itemGuardTestRow{{row: a}, {row: b, touches: []CatalogKey{a.Key}}, {row: c, guards: []CollectionPlanGuard{guard}, requires: []uint64{1}}}
	})
	x, view := itemGuardIndex(t, s, head)
	first, _ := x.row(1)
	second, _ := x.row(2)
	a := itemGuardDesired(first.Row)
	b := itemGuardOriginal(second.Row)
	b.References = []CatalogKey{a.Key}
	outcomes := itemGuardPredecessors(map[uint64]collectionCertifiedItem{1: itemGuardCertified(x, 1, 50)})
	for _, token := range []uint64{0, 50} {
		a.DependentsVersion = token
		got, err := verifyCollectionItemGuards(context.Background(), x, view, 2, itemGuardDesired(second.Row), itemGuardCatalog(map[CatalogKey]CatalogRecord{a.Key: a, b.Key: b}), outcomes, defaultCollectionItemGuardLimits())
		want := collectionItemGuardsAllowed
		if token != 0 {
			want = collectionItemGuardsConflict
		}
		if err != nil || got.Code != want {
			t.Fatal("before-touch resource identity/token confused", got, err)
		}
	}
}

func TestCollectionItemGuardsBoundsCancellationAndSafeErrors(t *testing.T) {
	s, head := executionIndexFixture(t, false, "normal")
	x, view := itemGuardIndex(t, s, head)
	row, _ := x.row(3)
	current := itemGuardOriginal(row.Row)
	current.DependentsVersion = 51
	catalog := itemGuardCatalog(map[CatalogKey]CatalogRecord{current.Key: current})
	outcomes := itemGuardPredecessors(map[uint64]collectionCertifiedItem{1: itemGuardCertified(x, 1, 50), 2: itemGuardCertified(x, 2, 51)})
	for _, limit := range []collectionItemGuardLimits{{Keys: 1, Outcomes: 10, Bytes: 32 << 20, Work: 1_000_000}, {Keys: 10, Outcomes: 1, Bytes: 32 << 20, Work: 1_000_000}, {Keys: 10, Outcomes: 10, Bytes: 1, Work: 1_000_000}, {Keys: 10, Outcomes: 10, Bytes: 32 << 20, Work: 1}} {
		limitedCatalog := catalog
		if limit.Keys == 1 {
			withReferences := current.Clone()
			withReferences.References = []CatalogKey{{Kind: "Credential", ID: "one"}, {Kind: "Credential", ID: "two"}}
			limitedCatalog = itemGuardCatalog(map[CatalogKey]CatalogRecord{current.Key: withReferences})
		}
		got, err := verifyCollectionItemGuards(context.Background(), x, view, 3, itemGuardDesired(row.Row), limitedCatalog, outcomes, limit)
		if !errors.Is(err, errCollectionItemGuardLimit) || got.Code != "" {
			t.Fatal("quota gave a decision", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	lookup := func(ctx context.Context, n uint64) (collectionCertifiedItem, bool, error) {
		item, present, err := outcomes(ctx, n)
		cancel()
		return item, present, err
	}
	got, err := verifyCollectionItemGuards(ctx, x, view, 3, itemGuardDesired(row.Row), catalog, lookup, defaultCollectionItemGuardLimits())
	if !errors.Is(err, context.Canceled) || got.Code != "" {
		t.Fatal("canceled lookup gave a decision", got, err)
	}
	const secret = "private-provider-canary-do-not-return"
	bad := func(context.Context, CatalogKey) (CatalogRecord, bool, error) {
		return CatalogRecord{}, false, errors.New(secret)
	}
	got, err = verifyCollectionItemGuards(context.Background(), x, view, 3, itemGuardDesired(row.Row), bad, outcomes, defaultCollectionItemGuardLimits())
	if !errors.Is(err, ErrCollectionUnavailable) || strings.Contains(err.Error(), secret) || got.Code != "" {
		t.Fatal("adapter diagnostic escaped safe classification")
	}
}

func TestCollectionItemGuardsLargeRowAndLateIntegrityFailure(t *testing.T) {
	s, head := executionIndexFixture(t, false, "large")
	x, view := itemGuardIndex(t, s, head)
	row, _ := x.row(1)
	if row.EndFragment-row.FirstFragment < 20 {
		t.Fatal("fixture no longer streams a large logical row")
	}
	reads := 0
	targetExists := false
	lookup := func(_ context.Context, key CatalogKey) (CatalogRecord, bool, error) {
		reads++
		if key == row.Row.Key {
			return CatalogRecord{Key: key, UID: "outside", Revision: "outside", Generation: 1}, targetExists, nil
		}
		return CatalogRecord{Key: key, UID: strings.Repeat("u", 256), Revision: strings.Repeat("v", 256), Generation: 1}, true, nil
	}
	before, _ := json.Marshal(s.fsm.image)
	got, err := verifyCollectionItemGuards(context.Background(), x, view, 1, itemGuardDesired(row.Row), lookup, itemGuardPredecessors(nil), defaultCollectionItemGuardLimits())
	if err != nil || got.Code != collectionItemGuardsAllowed || reads < 7000 {
		t.Fatal("large original row was not fully checked", got, err, reads)
	}
	// Corrupt the last framed record without touching the row's retained range
	// commitment. Earlier callbacks must never escape as an allowed decision.
	view.mu.Lock()
	raw := view.planRows[head.ID][row.EndFragment]
	var frame collectionPlanLedgerRow
	if err := json.Unmarshal(raw, &frame); err != nil {
		view.mu.Unlock()
		t.Fatal(err)
	}
	frame.Part.Fragment.End.Digest = strings.Repeat("e", 64)
	changed, err := json.Marshal(frame)
	if err != nil {
		view.mu.Unlock()
		t.Fatal(err)
	}
	view.planRows[head.ID][row.EndFragment] = changed
	view.mu.Unlock()
	reads = 0
	targetExists = true // An earlier target conflict must still verify the complete original range.
	got, err = verifyCollectionItemGuards(context.Background(), x, view, 1, itemGuardDesired(row.Row), lookup, itemGuardPredecessors(nil), defaultCollectionItemGuardLimits())
	if !errors.Is(err, ErrCollectionPlanInvalid) || got.Code != "" || reads < 7000 {
		t.Fatal("late corruption escaped provisional checks", got, err, reads)
	}
	after, _ := json.Marshal(s.fsm.image)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("guard verifier changed authoritative state")
	}
}
