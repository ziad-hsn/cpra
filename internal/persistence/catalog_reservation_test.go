package persistence

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func catalogReservationFixture(t *testing.T, mutation func(*CatalogMutation)) (*machine, Command, OperationReservation) {
	t.Helper()
	f := catalogDeltaFixture()
	m := catalogDeltaCreate(f)
	if mutation != nil {
		mutation(&m)
	}
	c := Command{Kind: "catalog", At: catalogDeltaAt.Add(2 * time.Second), Catalog: &m}
	r, _, err := operationDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	f.image.OperationEpoch = "11111111-1111-4111-8111-111111111111"
	f.image.OperationHighWater = 1
	r.ID = operationHandle(f.image.OperationEpoch, 1)
	f.image.OperationReservations = map[string]OperationReservation{r.ID: r}
	c.Catalog.OperationID = r.ID
	c.At = c.At.Add(time.Millisecond)
	return f, c, r
}

func TestCatalogReservationPreparationCreditsOnlyRetainedReservation(t *testing.T) {
	f, c, r := catalogReservationFixture(t, nil)
	for n := f.pendingOperationCount(); n < maxPendingCatalogOperations; n++ {
		f.image.OperationReservations[fmt.Sprintf("capacity-%d", n)] = OperationReservation{}
	}
	before := catalogDeltaJSON(t, f.image)
	if p, err := f.prepareCatalogMutation(*c.Catalog, 101, c.At, CollectionActivationFormatVersion); !errors.Is(err, ErrCatalogBusy) || p != nil {
		t.Fatal("uncredited full capacity", err)
	}
	p, err := f.prepareCatalogMutationConsuming(*c.Catalog, 101, c.At, CollectionActivationFormatVersion, r.ID)
	if err != nil || p == nil || p.result.Operation.ID != r.ID {
		t.Fatal("exact reservation credit", err)
	}
	if !bytes.Equal(before, catalogDeltaJSON(t, f.image)) || f.operationVersions != nil {
		t.Fatal("pure preparation consumed reservation or initialized mutable index")
	}
	// Reference behavior consumes one reservation then uses the ordinary path.
	baseline := catalogDeltaCloneMachine(t, f)
	delete(baseline.image.OperationReservations, r.ID)
	want := baseline.applyManagementTarget(c, 101, CollectionActivationFormatVersion)
	got := f.applyOperationTarget(c, 101, CollectionActivationFormatVersion)
	if !got.Allowed || got.Err != nil || !reflect.DeepEqual(got, want) || !bytes.Equal(catalogDeltaJSON(t, f.image), catalogDeltaJSON(t, baseline.image)) {
		t.Fatal("credit changed installed catalog or receipt semantics", got.Err, want.Err)
	}
	if _, ok := f.image.OperationReservations[r.ID]; ok {
		t.Fatal("successful installation retained reservation")
	}
	if f.pendingOperationCount() != maxPendingCatalogOperations {
		t.Fatal("installation did not replace its existing capacity")
	}
}

func TestCatalogReservationPreparationRejectsForeignCredit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*OperationReservation)
	}{
		{"kind", func(r *OperationReservation) { r.CommandKind = "control" }},
		{"state", func(r *OperationReservation) { r.State = "committed" }},
		{"id", func(r *OperationReservation) { r.ID = ledgerTestOperation(999) }},
		{"key", func(r *OperationReservation) { r.Key.ID = "another" }},
		{"uid", func(r *OperationReservation) { r.UID = "another" }},
		{"version", func(r *OperationReservation) { r.NewVersion = "another" }},
		{"old-version", func(r *OperationReservation) { r.OldVersion = "another" }},
		{"generation", func(r *OperationReservation) { r.Generation++ }},
		{"actor", func(r *OperationReservation) { r.Actor = "another" }},
		{"removed", func(r *OperationReservation) { r.Removed = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, r := catalogReservationFixture(t, nil)
			tc.alter(&r)
			f.image.OperationReservations[c.Catalog.OperationID] = r
			before := catalogDeltaJSON(t, f.image)
			if p, err := f.prepareCatalogMutationConsuming(*c.Catalog, 101, c.At, CollectionActivationFormatVersion, c.Catalog.OperationID); !errors.Is(err, ErrOperationReservation) || p != nil {
				t.Fatal("foreign capacity credit", err)
			}
			if !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
				t.Fatal("rejected credit changed image")
			}
		})
	}
	f, c, original := catalogReservationFixture(t, nil)
	other := original
	other.ID = operationHandle(f.image.OperationEpoch, 2)
	f.image.OperationReservations[other.ID] = other
	before := catalogDeltaJSON(t, f.image)
	if p, err := f.prepareCatalogMutationConsuming(*c.Catalog, 101, c.At, CollectionActivationFormatVersion, other.ID); !errors.Is(err, ErrOperationReservation) || p != nil {
		t.Fatal("existing unrelated reservation credited", err)
	}
	if !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
		t.Fatal("rejected unrelated credit changed state")
	}
	if p, err := f.prepareCatalogMutationConsuming(*c.Catalog, 101, c.At, CollectionActivationFormatVersion, ledgerTestOperation(999)); !errors.Is(err, ErrOperationReservation) || p != nil {
		t.Fatal("missing reservation credit", err)
	}
}

func TestCatalogReservationPreparationFailureIsPureAndRejectionIsTerminal(t *testing.T) {
	f, c, r := catalogReservationFixture(t, func(m *CatalogMutation) { m.Conditions[0].Revision = "outside-version" })
	before := catalogDeltaJSON(t, f.image)
	if p, err := f.prepareCatalogMutationConsuming(*c.Catalog, 101, c.At, CollectionActivationFormatVersion, r.ID); !errors.Is(err, ErrCatalogDependency) || p != nil {
		t.Fatal("original guard did not fail", err)
	}
	if !bytes.Equal(before, catalogDeltaJSON(t, f.image)) {
		t.Fatal("failed preparation consumed or changed state")
	}
	result := f.applyOperationTarget(c, 101, CollectionActivationFormatVersion)
	if !errors.Is(result.Err, ErrCatalogDependency) || result.Allowed || result.Operation == nil || result.Operation.ID != r.ID || result.Operation.Outcome != "activation_rejected" || result.Operation.CommittedIndex != 0 {
		t.Fatal("business rejection changed semantics", result)
	}
	if _, ok := f.image.OperationReservations[r.ID]; ok {
		t.Fatal("business rejection resurrected reserved work")
	}
	if _, ok := f.image.Catalog[c.Catalog.Record.Key.indexKey()]; ok || f.image.CatalogMutationSequence != 0 {
		t.Fatal("rejected guard changed catalog or consumed token")
	}
	if len(result.Events) != 1 || result.Events[0].Operation == nil || result.Events[0].Operation.ID != r.ID {
		t.Fatal("terminal rejection audit absent")
	}
	after := catalogDeltaJSON(t, f.image)
	retry := f.applyOperationTarget(c, 102, CollectionActivationFormatVersion)
	if !errors.Is(retry.Err, ErrOperationExpired) || retry.Allowed || len(retry.Events) != 0 || !bytes.Equal(after, catalogDeltaJSON(t, f.image)) {
		t.Fatal("retry resurrected rejected reservation", retry)
	}
}
