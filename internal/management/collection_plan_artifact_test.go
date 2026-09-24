package management

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const artifactPlanID = "96aa48a8-6b2c-45cd-a02c-fd73187e8471"

func TestCollectionPlanArtifactRoundTripHasNoProviderDataOrEffects(t *testing.T) {
	f := stagedValidationFixture(t,
		resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}),
		resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}),
		planLogEndpoint("chat", "private-artifact-never-opened.log"),
		resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private-artifact-secret")}))
	_, plan, err := compilePlanFixture(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	index := f.store.Status().CommittedIndex
	before, _, _ := f.store.CollectionGet(f.head.ID)
	wrapper := collectionBlockSealing(t, f.catalog)
	a, err := prepareCollectionPlanArtifact(context.Background(), plan, artifactPlanID)
	if err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	descriptor, err := a.writeTo(context.Background(), &first)
	if err != nil || descriptor != a.descriptor {
		t.Fatal("artifact differs from its pre-staging descriptor", err)
	}
	if _, err := a.writeTo(context.Background(), &second); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("exact retry changed frozen artifact", err)
	}
	decoded, err := persistence.DecodeCollectionPlan(context.Background(), bytes.NewReader(first.Bytes()), func(persistence.CollectionPlanFragment) error { return nil })
	if err != nil || decoded != descriptor || descriptor.Bytes != uint64(first.Len()) {
		t.Fatal("streaming codec did not preserve complete artifact identity", err)
	}
	for _, forbidden := range []string{"private-artifact-secret", "private-artifact-never-opened.log"} {
		if bytes.Contains(first.Bytes(), []byte(forbidden)) {
			t.Fatal("provider data entered metadata artifact")
		}
	}
	after, _, _ := f.store.CollectionGet(f.head.ID)
	if index != f.store.Status().CommittedIndex || !reflect.DeepEqual(before, after) || wrapper.wraps.Load() != 0 {
		t.Fatal("artifact encoding changed durable state or sealed provider data")
	}
}

func TestCollectionPlanArtifactChunksOneRowLargerThanCommand(t *testing.T) {
	f := stagedValidationFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private-value")}))
	_, plan, err := compilePlanFixture(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic serialization stress, not a claim that the graph compiler can
	// validate this topology. One logical row must cross the 4MiB command size
	// while each emitted guard fragment remains bounded by the shared codec.
	for n := range 8000 {
		plan.Rows[0].Guards = append(plan.Rows[0].Guards, collectionPlanGuard{
			Key:         persistence.CatalogKey{Kind: "Credential", ID: fmt.Sprintf("outside-%05d", n)},
			OriginalUID: strings.Repeat("u", 256), OriginalRevision: strings.Repeat("r", 256), OriginalGeneration: 1})
	}
	a, err := prepareCollectionPlanArtifact(context.Background(), plan, artifactPlanID)
	if err != nil {
		t.Fatal(err)
	}
	if a.descriptor.Bytes <= 4<<20 || a.descriptor.Fragments <= 8000/persistence.CollectionPlanMaxChunkEntries {
		t.Fatal("fixture did not exercise fragmented large row")
	}
	var output bytes.Buffer
	if _, err := a.writeTo(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	descriptor, err := persistence.DecodeCollectionPlan(context.Background(), &output, func(persistence.CollectionPlanFragment) error { return nil })
	if err != nil || descriptor != a.descriptor {
		t.Fatal("large row is not a valid bounded artifact", err)
	}
}

func TestCollectionPlanArtifactRejectsChangedPlanAndInterruptedWriter(t *testing.T) {
	f := stagedValidationFixture(t, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private-value")}))
	_, plan, err := compilePlanFixture(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	a, err := prepareCollectionPlanArtifact(context.Background(), plan, artifactPlanID)
	if err != nil {
		t.Fatal(err)
	}
	// A valid structural edit still changes the intended artifact. The original
	// descriptor must not be silently replaced by a fresh measurement.
	plan.Rows[0].Item++
	if descriptor, err := a.writeTo(context.Background(), io.Discard); !errors.Is(err, persistence.ErrCollectionConflict) || descriptor != (persistence.CollectionPlanDescriptor{}) {
		t.Fatal("changed plan received original artifact identity", err)
	}
	plan.Rows[0].Item--
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if descriptor, err := a.writeTo(canceled, io.Discard); !errors.Is(err, context.Canceled) || descriptor != (persistence.CollectionPlanDescriptor{}) {
		t.Fatal("canceled encoding returned usable descriptor", err)
	}
	want := errors.New("destination unavailable")
	if descriptor, err := a.writeTo(context.Background(), artifactFailingWriter{err: want}); !errors.Is(err, want) || descriptor != (persistence.CollectionPlanDescriptor{}) {
		t.Fatal("failed write returned usable descriptor", err)
	}
	var output bytes.Buffer
	if descriptor, err := a.writeTo(context.Background(), &output); err != nil || descriptor != a.descriptor {
		t.Fatal("interrupted write changed retry identity", err)
	}
}

type artifactFailingWriter struct{ err error }

func (w artifactFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCollectionPlanArtifactEmptyInputsFailBeforeWriting(t *testing.T) {
	for _, plan := range []*collectionPlan{nil, {}, {Header: collectionPlanHeader{ItemCount: 1}}} {
		if artifact, err := prepareCollectionPlanArtifact(context.Background(), plan, artifactPlanID); err == nil || artifact != nil {
			t.Fatal("missing compiled rows received artifact", err)
		}
	}
	var artifact *collectionPlanArtifact
	if _, err := artifact.writeTo(context.Background(), io.Discard); !errors.Is(err, ErrValidation) {
		t.Fatal("nil artifact was accepted", err)
	}
}
