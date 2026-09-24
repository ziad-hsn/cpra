package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

var catalogDeltaAt = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

// These structurally valid ciphertext-shaped bytes exercise ownership, not
// encryption. Catalog application never decrypts them; cryptographic recovery
// remains covered by the existing catalog storage/process tests.
func catalogDeltaRecord(kind, id string, refs ...CatalogKey) CatalogRecord {
	return CatalogRecord{Key: CatalogKey{Kind: kind, ID: id}, UID: id + "-uid", Revision: id + "-v1", Generation: 1,
		Purpose: "resource", CreatedAt: catalogDeltaAt, UpdatedAt: catalogDeltaAt,
		Payload: secureconfig.Envelope{Format: secureconfig.FormatVersion, KeyID: "fixture-key", WrappedKey: []byte{1, 2, 3},
			Nonce: bytes.Repeat([]byte{4}, 12), Ciphertext: bytes.Repeat([]byte{5}, 32)}, References: refs}
}

func catalogDeltaFixture() *machine {
	a := catalogDeltaRecord("Credential", "a")
	b := catalogDeltaRecord("Credential", "b")
	e := catalogDeltaRecord("NotificationEndpoint", "endpoint", a.Key)
	m := catalogDeltaRecord("Monitor", "monitor", e.Key)
	a.CommittedIndex, a.DependentsVersion = 80, 83
	b.CommittedIndex = 81
	e.CommittedIndex, e.DependentsVersion = 83, 84
	m.CommittedIndex = 84
	f := &machine{image: image{Version: CollectionFormatVersion, Index: 100,
		Catalog: map[string]CatalogRecord{a.Key.indexKey(): a, b.Key.indexKey(): b, e.Key.indexKey(): e, m.Key.indexKey(): m},
		Operations: map[string]OperationReceipt{e.Revision: {ID: e.Revision, Key: e.Key, UID: e.UID, NewVersion: e.Revision,
			Generation: e.Generation, CommittedIndex: e.CommittedIndex, Actor: "operator", At: catalogDeltaAt,
			UpdatedAt: catalogDeltaAt, State: "committed", Outcome: "committed"}}}}
	f.rebuildCatalogIndexes()
	return f // Leave operationVersions nil to exercise read-only fallback.
}

func catalogDeltaCloneMachine(t *testing.T, f *machine) *machine {
	t.Helper()
	data, err := json.Marshal(f.image)
	if err != nil {
		t.Fatal(err)
	}
	clone := &machine{catalogSequence: f.catalogSequence, catalogEpoch: f.catalogEpoch}
	if err := json.Unmarshal(data, &clone.image); err != nil {
		t.Fatal(err)
	}
	clone.catalogChanges = append([]CatalogChange(nil), f.catalogChanges...)
	clone.rebuildCatalogIndexes()
	if f.operationVersions != nil {
		clone.rebuildOperationIndex()
	}
	return clone
}

func catalogDeltaConditions(f *machine, refs []CatalogKey) []CatalogCondition {
	var conditions []CatalogCondition
	for _, key := range refs {
		r := f.image.Catalog[key.indexKey()]
		conditions = append(conditions, CatalogCondition{Key: key, UID: r.UID, Revision: r.Revision})
	}
	return conditions
}

func catalogDeltaUpdate(f *machine, key CatalogKey, refs ...CatalogKey) CatalogMutation {
	old := f.image.Catalog[key.indexKey()]
	r := old.Clone()
	r.Revision += "-next"
	r.Generation++
	r.CommittedIndex, r.DependentsVersion = 0, 0
	r.UpdatedAt = catalogDeltaAt.Add(time.Second)
	r.References = refs
	return CatalogMutation{Record: r, ExpectedUID: old.UID, ExpectedRevision: old.Revision,
		ExpectedDependentsVersion: old.DependentsVersion, Conditions: catalogDeltaConditions(f, refs)}
}

func catalogDeltaEndpoint(f *machine) CatalogMutation {
	m := catalogDeltaUpdate(f, CatalogKey{"NotificationEndpoint", "endpoint"}, CatalogKey{"Credential", "b"})
	m.OperationID, m.Actor = m.Record.Revision, "operator"
	return m
}

func catalogDeltaCreate(f *machine) CatalogMutation {
	r := catalogDeltaRecord("Monitor", "new", CatalogKey{"Credential", "a"}, CatalogKey{"Credential", "b"})
	r.UpdatedAt = catalogDeltaAt.Add(time.Second)
	return CatalogMutation{Record: r, Create: true, Conditions: catalogDeltaConditions(f, r.References), OperationID: r.Revision, Actor: "operator"}
}

func catalogDeltaDelete(f *machine, key CatalogKey) CatalogMutation {
	m := catalogDeltaUpdate(f, key)
	m.Record.Removed, m.Record.Payload = true, secureconfig.Envelope{}
	return m
}

func catalogDeltaFillCapacity(f *machine) {
	// Reservations occupy the same admission budget without changing target
	// catalog state. The factoring under test only observes their cardinality.
	f.image.OperationReservations = make(map[string]OperationReservation, maxPendingCatalogOperations-1)
	for i := 0; i < maxPendingCatalogOperations-1; i++ {
		id := fmt.Sprintf("reserved-%04d", i)
		f.image.OperationReservations[id] = OperationReservation{}
	}
}

func catalogDeltaJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func catalogDeltaTree(f *machine) []CatalogRecord {
	var rows []CatalogRecord
	if f.catalog != nil {
		f.catalog.Ascend(func(item catalogItem) bool { rows = append(rows, item.record.Clone()); return true })
	}
	return rows
}

func catalogDeltaAssertEquivalent(t *testing.T, got, want *machine, result, expected Result) {
	t.Helper()
	if (result.Err == nil) != (expected.Err == nil) || result.Err != nil && result.Err.Error() != expected.Err.Error() {
		t.Fatalf("error parity changed: got %v, want %v", result.Err, expected.Err)
	}
	// Errors have no useful JSON payload; compare them above, and compare all
	// success fields and event order below rather than pointer identity.
	result.Err, expected.Err = nil, nil
	if !bytes.Equal(catalogDeltaJSON(t, result), catalogDeltaJSON(t, expected)) {
		t.Fatal("result or ordered event bytes differ from pre-refactor oracle")
	}
	if !bytes.Equal(catalogDeltaJSON(t, got.image), catalogDeltaJSON(t, want.image)) ||
		!reflect.DeepEqual(catalogDeltaTree(got), catalogDeltaTree(want)) ||
		!reflect.DeepEqual(got.catalogIncoming, want.catalogIncoming) ||
		!reflect.DeepEqual(got.catalogChanges, want.catalogChanges) || got.catalogSequence != want.catalogSequence {
		t.Fatal("catalog, reverse edges, receipts, image version, or change ring differs from pre-refactor oracle")
	}
	// Lazy derived-index initialization may legitimately differ after rejection.
	// Compare observable version lookups without initializing either cache.
	for _, r := range want.image.Operations {
		x, xOK := got.pendingCatalogOperation(r.Key, r.UID, r.NewVersion)
		y, yOK := want.pendingCatalogOperation(r.Key, r.UID, r.NewVersion)
		if xOK != yOK || x != y {
			t.Fatal("pending catalog operation lookup changed")
		}
	}
}

func TestCatalogDeltaPreparationMatchesFrozenApply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		make    func(*machine) CatalogMutation
		edit    func(*machine, *CatalogMutation)
		format  int
		wantErr error
	}{
		{name: "create", make: catalogDeltaCreate},
		{name: "update-supersedes", make: catalogDeltaEndpoint},
		{name: "update-no-new-receipt", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) { m.OperationID, m.Actor = "", "" }},
		{name: "same-edge-still-touches", make: func(f *machine) CatalogMutation {
			return catalogDeltaUpdate(f, CatalogKey{"NotificationEndpoint", "endpoint"}, CatalogKey{"Credential", "a"})
		}},
		{name: "delete", make: func(f *machine) CatalogMutation { return catalogDeltaDelete(f, CatalogKey{"Monitor", "monitor"}) }},
		{name: "delete-referenced", make: func(f *machine) CatalogMutation {
			return catalogDeltaDelete(f, CatalogKey{"NotificationEndpoint", "endpoint"})
		}, wantErr: ErrCatalogReferenced},
		{name: "create-active", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) { m.Create = true }, wantErr: ErrCatalogConflict},
		{name: "missing-target", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) { m.Record.Key.ID = "missing" }, wantErr: ErrCatalogNotFound},
		{name: "stale-target", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) { m.ExpectedRevision = "stale" }, wantErr: ErrCatalogConflict},
		{name: "stale-reverse", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) { m.ExpectedDependentsVersion++ }, wantErr: ErrCatalogConflict},
		{name: "stale-dependency", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) { m.Conditions[0].Revision = "stale" }, wantErr: ErrCatalogDependency},
		{name: "stale-dependency-reverse", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) {
			value := uint64(99)
			m.Conditions[0].ExpectedDependentsVersion = &value
		}, wantErr: ErrCatalogDependency},
		{name: "future-record-time", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) { m.Record.UpdatedAt = catalogDeltaAt.Add(time.Hour) }, wantErr: ErrCatalogConflict},
		{name: "regressed-record-time", make: catalogDeltaEndpoint, edit: func(_ *machine, m *CatalogMutation) { m.Record.UpdatedAt = catalogDeltaAt.Add(-time.Second) }, wantErr: ErrCatalogConflict},
		{name: "regressed-receipt-time", make: catalogDeltaEndpoint, edit: func(f *machine, _ *CatalogMutation) {
			r := f.image.Operations["endpoint-v1"]
			r.UpdatedAt = catalogDeltaAt.Add(time.Hour)
			f.image.Operations[r.ID] = r
		}, wantErr: ErrCatalogConflict},
		{name: "capacity", make: catalogDeltaCreate, edit: func(f *machine, _ *CatalogMutation) { catalogDeltaFillCapacity(f) }, wantErr: ErrCatalogBusy},
		{name: "supersede-at-capacity", make: catalogDeltaEndpoint, edit: func(f *machine, _ *CatalogMutation) { catalogDeltaFillCapacity(f) }},
		{name: "sequence-exhausted", make: catalogDeltaEndpoint, edit: func(f *machine, _ *CatalogMutation) { f.image.CatalogMutationSequence = math.MaxUint64 }, wantErr: ErrCatalogSequenceExhausted},
		{name: "format-downgrade", make: catalogDeltaEndpoint, format: CollectionFormatVersion, edit: func(f *machine, _ *CatalogMutation) {
			f.image.Version, f.image.CatalogMutationSequence = CatalogMutationFormatVersion, 100
		}, wantErr: ErrCatalogFormatDowngrade},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := catalogDeltaFixture()
			m := tc.make(f)
			if tc.edit != nil {
				tc.edit(f, &m)
			}
			baseline := catalogDeltaCloneMachine(t, f)
			format := tc.format
			if format == 0 {
				format = CatalogMutationFormatVersion
			}
			at := catalogDeltaAt.Add(2 * time.Second)
			before := catalogDeltaJSON(t, f.image)
			beforeTree := catalogDeltaTree(f)
			beforeIncoming := catalogDeltaJSON(t, f.catalogIncoming)
			prepared, err := f.prepareCatalogMutation(m, 101, at, format)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("unexpected preparation: %v", err)
			}
			if !bytes.Equal(before, catalogDeltaJSON(t, f.image)) || !reflect.DeepEqual(beforeTree, catalogDeltaTree(f)) ||
				!bytes.Equal(beforeIncoming, catalogDeltaJSON(t, f.catalogIncoming)) || f.operationVersions != nil || f.catalogChanges != nil || f.catalogSequence != 0 {
				t.Fatal("preparation mutated authoritative state or derived indexes")
			}
			var got Result
			if err != nil {
				if prepared != nil {
					t.Fatal("rejected preparation returned an installable delta")
				}
				got.Err = err
			} else {
				got = f.installCatalogMutation(prepared)
			}
			want := baseline.catalogDeltaLegacyApply(m.Clone(), 101, at, format)
			catalogDeltaAssertEquivalent(t, f, baseline, got, want)
		})
	}
}

func TestCatalogDeltaPreparedInputAndResultOwnership(t *testing.T) {
	f := catalogDeltaFixture()
	m := catalogDeltaEndpoint(f)
	original := m.Clone()
	p, err := f.prepareCatalogMutation(m, 101, catalogDeltaAt.Add(2*time.Second), CatalogMutationFormatVersion)
	if err != nil {
		t.Fatal(err)
	}
	// Caller-owned envelope/reference/condition buffers can disappear after
	// preparation. No changing machine state is permitted between these calls.
	m.Record.Payload.WrappedKey[0] ^= 0xff
	m.Record.Payload.Nonce[0] ^= 0xff
	m.Record.Payload.Ciphertext[0] ^= 0xff
	m.Record.References[0] = CatalogKey{"Credential", "changed-by-caller"}
	m.Conditions[0].Key.ID = "changed-condition"
	result := f.installCatalogMutation(p)
	stored := f.image.Catalog[original.Record.Key.indexKey()]
	if !reflect.DeepEqual(stored.Payload, original.Record.Payload) || !reflect.DeepEqual(stored.References, original.Record.References) {
		t.Fatal("prepared record retained caller buffers")
	}
	if result.Operation == nil || len(result.Events) != 2 || result.Events[0].Operation == nil ||
		result.Events[0].Operation.Outcome != "superseded" || result.Events[1].Operation == nil || result.Events[1].Operation.Outcome != "committed" {
		t.Fatal("supersession/new-child event ordering changed")
	}
	before := catalogDeltaJSON(t, f.image)
	result.Catalog.Payload.Ciphertext[0] ^= 0xff
	result.Catalog.References[0].ID = "changed-result"
	result.Operation.Actor = "changed-result"
	result.Events[0].Operation.Actor = "changed-result"
	result.Events[1].Operation.Actor = "changed-result"
	if !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
		t.Fatal("returned result aliases installed state")
	}
	if _, ok := f.image.Operations["endpoint-v1"]; ok {
		t.Fatal("superseded ordinary receipt remains pending")
	}
	if got := f.image.Catalog[CatalogKey{"Credential", "a"}.indexKey()].DependentsVersion; got != 101 {
		t.Fatal("removed reference was not touched")
	}
	if got := f.image.Catalog[CatalogKey{"Credential", "b"}.indexKey()].DependentsVersion; got != 101 {
		t.Fatal("new reference was not touched")
	}
	if _, ok := f.catalogIncoming[CatalogKey{"Credential", "a"}.indexKey()]; ok {
		t.Fatal("removed reference still has reverse edge")
	}
	if _, ok := f.catalogIncoming[CatalogKey{"Credential", "b"}.indexKey()][original.Record.Key.indexKey()]; !ok {
		t.Fatal("new reverse edge missing")
	}
}

func TestCatalogDeltaLegacyAndSameEntryTokenParity(t *testing.T) {
	for _, format := range []int{CatalogFormatVersion, CollectionFormatVersion, CatalogMutationFormatVersion, CollectionActivationFormatVersion} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			f := catalogDeltaFixture()
			f.image.Version = CatalogFormatVersion
			baseline := catalogDeltaCloneMachine(t, f)
			at := catalogDeltaAt.Add(2 * time.Second)
			var tokens []uint64
			for i := 0; i < 2; i++ {
				r := catalogDeltaRecord("Monitor", fmt.Sprintf("same-entry-%d", i), CatalogKey{"Credential", "a"})
				m := CatalogMutation{Record: r, Create: true, Conditions: catalogDeltaConditions(f, r.References)}
				p, err := f.prepareCatalogMutation(m, 101, at, format)
				if err != nil {
					t.Fatal(err)
				}
				got := f.installCatalogMutation(p)
				want := baseline.catalogDeltaLegacyApply(m.Clone(), 101, at, format)
				catalogDeltaAssertEquivalent(t, f, baseline, got, want)
				tokens = append(tokens, got.CatalogMutationSequence)
			}
			guard := f.image.Catalog[CatalogKey{"Credential", "a"}.indexKey()].DependentsVersion
			if format < CatalogMutationFormatVersion {
				if tokens[0] != 0 || tokens[1] != 0 || guard != 101 {
					t.Fatal("historical log-index semantics changed")
				}
			} else if tokens[0] != 101 || tokens[1] != 102 || guard != 102 {
				t.Fatal("same-entry writes lost their distinct reverse tokens")
			}
		})
	}
}

func TestCatalogDeltaUninitializedAndTombstoneParity(t *testing.T) {
	for _, tc := range []string{"empty", "recreate", "reuse-uid", "reuse-revision"} {
		t.Run(tc, func(t *testing.T) {
			f := &machine{image: image{Version: FormatVersion}}
			r := catalogDeltaRecord("Monitor", "new")
			m := CatalogMutation{Record: r, Create: true}
			if tc != "empty" {
				old := r.Clone()
				old.Removed, old.Payload, old.References = true, secureconfig.Envelope{}, nil
				old.CommittedIndex = 90
				f.image.Version, f.image.Index = CatalogFormatVersion, 100
				f.image.Catalog = map[string]CatalogRecord{old.Key.indexKey(): old}
				m.Record.UID, m.Record.Revision = "new-incarnation", "new-revision"
				if tc == "reuse-uid" {
					m.Record.UID = old.UID
				}
				if tc == "reuse-revision" {
					m.Record.Revision = old.Revision
				}
			}
			baseline := catalogDeltaCloneMachine(t, f)
			baseline.catalog, baseline.catalogIncoming = nil, nil
			p, err := f.prepareCatalogMutation(m, 101, catalogDeltaAt, CatalogMutationFormatVersion)
			if f.catalog != nil || f.catalogIncoming != nil || f.operationVersions != nil {
				t.Fatal("preparation initialized lazy caches")
			}
			var got Result
			if err != nil {
				if p != nil {
					t.Fatal("rejected preparation returned delta")
				}
				got.Err = err
			} else {
				got = f.installCatalogMutation(p)
			}
			want := baseline.catalogDeltaLegacyApply(m.Clone(), 101, catalogDeltaAt, CatalogMutationFormatVersion)
			catalogDeltaAssertEquivalent(t, f, baseline, got, want)
			if (tc == "reuse-uid" || tc == "reuse-revision") && !errors.Is(err, ErrCatalogConflict) {
				t.Fatal("retained identity reused", err)
			}
		})
	}
}

func TestCatalogDeltaIndexedLookupPreparationReadOnly(t *testing.T) {
	f := catalogDeltaFixture()
	f.rebuildOperationIndex()
	before := make(map[operationVersionKey]string, len(f.operationVersions))
	for key, id := range f.operationVersions {
		before[key] = id
	}
	m := catalogDeltaEndpoint(f)
	p, err := f.prepareCatalogMutation(m, 101, catalogDeltaAt.Add(2*time.Second), CatalogMutationFormatVersion)
	if err != nil || p == nil || !reflect.DeepEqual(f.operationVersions, before) {
		t.Fatal("indexed preparation changed cache or failed", err)
	}
	if p.supersededID != "endpoint-v1" {
		t.Fatal("indexed lookup lost original pending receipt")
	}
}
