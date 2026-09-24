package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func compilePlanFixture(t *testing.T, f *collectionSourceFixture) (CollectionValidation, *collectionPlan, error) {
	t.Helper()
	view, err := f.catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	source := f.open(t, nil)
	result, plan, err := f.catalog.compileStagedCollectionPlan(context.Background(), view, source, collectionReadAll)
	if source.liveBytes != 0 {
		t.Fatal("plan compilation retained a plaintext reservation")
	}
	return result, plan, err
}

func planRow(t *testing.T, plan *collectionPlan, kind, id string) collectionPlanRow {
	t.Helper()
	if plan != nil {
		for _, row := range plan.Rows {
			if row.Key == (persistence.CatalogKey{Kind: kind, ID: id}) {
				return row
			}
		}
	}
	t.Fatal("plan row missing", kind, id)
	return collectionPlanRow{}
}

func planGuard(t *testing.T, row collectionPlanRow, key persistence.CatalogKey) collectionPlanGuard {
	t.Helper()
	for _, guard := range row.Guards {
		if guard.Key == key {
			return guard
		}
	}
	t.Fatal("plan guard missing", key)
	return collectionPlanGuard{}
}

func planLogEndpoint(id, file string) api.Resource {
	raw, _ := json.Marshal(map[string]string{"file": file})
	return resource("NotificationEndpoint", id, api.DriverConfig{Type: "log", Config: raw})
}

func TestCollectionPlanDeterministicPrivateEvidenceAndNoEffects(t *testing.T) {
	inputs := []api.Resource{
		resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}),
		resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}),
		planLogEndpoint("chat", "private-never-opened-plan-log"),
		resource("Credential", "independent", api.CredentialSpec{Value: api.Pointer("private-plan-credential")}),
	}
	f := stagedValidationFixture(t, inputs...)
	before, _, _ := f.store.CollectionGet(f.head.ID)
	index := f.store.Status().CommittedIndex
	wrapper := collectionBlockSealing(t, f.catalog)
	result, plan, err := compilePlanFixture(t, &f)
	if err != nil || !result.Valid || plan == nil || len(plan.Rows) != len(inputs) {
		t.Fatal("valid inventory did not compile", err)
	}
	_, again, err := compilePlanFixture(t, &f)
	if err != nil || !reflect.DeepEqual(plan, again) {
		t.Fatal("plan compilation is not deterministic", err)
	}
	ordinary, err, _ := stagedValidationRun(t, &f, collectionReadAll)
	if err != nil || !reflect.DeepEqual(result, ordinary) {
		t.Fatal("compiler changed public validation observations", err)
	}
	if plan.Header.OperationID != f.head.ID || plan.Header.UploadID != f.head.UploadID || plan.Header.ContentDigest != f.head.ContentDigest ||
		plan.Header.InputProgressDigest != f.head.ProgressDigest || plan.Header.ItemCount != f.head.ItemCount || plan.Header.ObservedIndex != index {
		t.Fatal("plan detached from immutable input")
	}
	for i, row := range plan.Rows {
		if row.Ordinal != uint64(i+1) || row.Key != result.Order[i] || !row.Target.Absent || row.Target.FromOrdinal != 0 ||
			row.Source != f.items[row.InputOrdinal-1].Source || row.Document != 1 || row.Item != row.InputOrdinal {
			t.Fatal("plan order, source attribution or original create guard changed", row)
		}
		if !slices.IsSorted(row.Requires) {
			t.Fatal("requirements not deterministic")
		}
	}
	independent := planRow(t, plan, "Credential", "independent")
	if len(independent.Guards) != 0 || len(independent.Requires) != 0 {
		t.Fatal("independent item acquired unrelated collection guards")
	}
	chat, oncall := planRow(t, plan, "NotificationEndpoint", "chat"), planRow(t, plan, "Recipient", "oncall")
	guard := planGuard(t, oncall, chat.Key)
	if guard.FromOrdinal != chat.Ordinal || !guard.Absent || !slices.Contains(oncall.Requires, chat.Ordinal) {
		t.Fatal("created predecessor is not bound to its exact result")
	}
	encoded, _ := json.Marshal(plan)
	if bytes.Contains(encoded, []byte("private-plan-credential")) || bytes.Contains(encoded, []byte("private-never-opened-plan-log")) {
		t.Fatal("resource values entered plan metadata")
	}
	after, _, _ := f.store.CollectionGet(f.head.ID)
	if !reflect.DeepEqual(before, after) || f.store.Status().CommittedIndex != index || wrapper.wraps.Load() != 0 {
		t.Fatal("compiler mutated state or sealed output")
	}
	// Returned plans and nested slices belong to that invocation.
	plan.Rows[oncall.Ordinal-1].Requires[0] = 999
	if slices.Contains(again.Rows[oncall.Ordinal-1].Requires, uint64(999)) {
		t.Fatal("compiler invocations share mutable output")
	}
}

func TestCollectionPlanUnchangedChainRequiresChangedPredecessor(t *testing.T) {
	inputs := []api.Resource{resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}),
		resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}), planLogEndpoint("chat", "new-unused-file")}
	f := stagedValidationFixture(t, inputs...)
	oldEndpoint := createResource(t, f.catalog, planLogEndpoint("chat", "old-unused-file"))
	oldRecipient := createResource(t, f.catalog, inputs[1])
	_, plan, err := compilePlanFixture(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, recipient, group := planRow(t, plan, "NotificationEndpoint", "chat"), planRow(t, plan, "Recipient", "oncall"), planRow(t, plan, "NotificationGroup", "team")
	if endpoint.Change != "update" || endpoint.Target.OriginalRevision != oldEndpoint.Metadata.ResourceVersion ||
		recipient.Change != "unchanged" || recipient.Target.OriginalRevision != oldRecipient.Metadata.ResourceVersion || len(recipient.Touches) != 0 {
		t.Fatal("original/unchanged target identity lost")
	}
	if !slices.Contains(recipient.Requires, endpoint.Ordinal) || !slices.Contains(group.Requires, recipient.Ordinal) || !slices.Contains(group.Requires, endpoint.Ordinal) {
		t.Fatal("failure in included dependency chain can be bypassed")
	}
	if planGuard(t, recipient, endpoint.Key).FromOrdinal != endpoint.Ordinal || planGuard(t, group, recipient.Key).FromOrdinal != 0 {
		t.Fatal("changed and unchanged dependency versions were conflated")
	}
	// The changed endpoint validates the old included recipient before its turn;
	// that forward consumer guard cannot become a forward execution requirement.
	if len(endpoint.Requires) != 0 || planGuard(t, endpoint, recipient.Key).OriginalRevision != oldRecipient.Metadata.ResourceVersion {
		t.Fatal("outside-prefix old consumer introduced a forward dependency")
	}
}

func TestCollectionPlanTransitiveUnchangedInputCannotFallBack(t *testing.T) {
	monitor := collectionMonitor("api", "https://example.test/health")
	var spec api.MonitorSpec
	_ = json.Unmarshal(monitor.Spec, &spec)
	spec.Notifications = &map[string]api.AlertRule{"red": {EndpointRefs: api.Pointer([]string{"chat"})}}
	monitor.Spec, _ = json.Marshal(spec)
	f := stagedValidationFixture(t, monitor, resource("Credential", "hook", api.CredentialSpec{}))
	old := createResource(t, f.catalog, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/private-hook")}))
	createResource(t, f.catalog, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}))
	_, plan, err := compilePlanFixture(t, &f)
	if err != nil {
		t.Fatal(err)
	}
	credential, consumer := planRow(t, plan, "Credential", "hook"), planRow(t, plan, "Monitor", "api")
	guard := planGuard(t, consumer, credential.Key)
	if credential.Change != "unchanged" || !slices.Contains(consumer.Requires, credential.Ordinal) || guard.FromOrdinal != 0 || guard.OriginalRevision != old.Metadata.ResourceVersion {
		t.Fatal("transitive unchanged input can be replaced by unconfirmed old live bytes")
	}
}

func TestCollectionPlanRemovedEdgesOutsideGuardsAndIndependentEdits(t *testing.T) {
	f := stagedValidationFixture(t, resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"b", "c"}}))
	for _, id := range []string{"a", "b", "c"} {
		createResource(t, f.catalog, planLogEndpoint(id, "unused-"+id))
	}
	old := createResource(t, f.catalog, resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"a", "c"}}))
	outside := createResource(t, f.catalog, resource("NotificationGroup", "outside", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}))
	view, _ := f.catalog.Snapshot()
	// The compiler has scoped guards; this unrelated catalog write is permitted.
	createResource(t, f.catalog, resource("Credential", "unrelated", api.CredentialSpec{Value: api.Pointer("private-unrelated")}))
	_, plan, err := f.catalog.compileStagedCollectionPlan(context.Background(), view, f.open(t, nil), collectionReadAll)
	if err != nil {
		t.Fatal("unrelated write invalidated scoped plan", err)
	}
	row := planRow(t, plan, "Recipient", "oncall")
	wantTouches := []persistence.CatalogKey{{Kind: "NotificationEndpoint", ID: "a"}, {Kind: "NotificationEndpoint", ID: "b"}, {Kind: "NotificationEndpoint", ID: "c"}}
	guard := planGuard(t, row, persistence.CatalogKey{Kind: "NotificationGroup", ID: "outside"})
	if !reflect.DeepEqual(row.Touches, wantTouches) || row.Target.OriginalRevision != old.Metadata.ResourceVersion || row.Target.ReverseVersion == nil ||
		guard.OriginalRevision != outside.Metadata.ResourceVersion || guard.ReverseVersion == nil || guard.FromOrdinal != 0 {
		t.Fatal("removed reference or outside reverse guard lost")
	}
	for _, g := range row.Guards {
		if g.Key.ID == "unrelated" {
			t.Fatal("independent write leaked into guard footprint")
		}
	}
}

func TestCollectionPlanRejectsFinalConflictsAndMalformedInput(t *testing.T) {
	t.Run("final-captured-version", func(t *testing.T) {
		input := resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("new-value")})
		f := stagedValidationFixture(t, input)
		old := createResource(t, f.catalog, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("old-value")}))
		view, _ := f.catalog.Snapshot()
		edited := resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("outside-value")})
		prepared, err := f.catalog.Prepare(context.Background(), edited, old.Metadata.ResourceVersion, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.catalog.Commit(context.Background(), prepared); err != nil {
			t.Fatal(err)
		}
		result, plan, err := f.catalog.compileStagedCollectionPlan(context.Background(), view, f.open(t, nil), collectionReadAll)
		if !errors.Is(err, persistence.ErrCatalogConflict) || result.Valid || plan != nil {
			t.Fatal("partial compiler output escaped final conflict", err)
		}
	})
	t.Run("malformed-final-row", func(t *testing.T) {
		input := sourceCredentials(10)
		f := stagedSourceFixture(t, 3, func(i int) (persistence.CatalogKey, []byte) {
			key, raw := input(i)
			if i == 2 {
				clear(raw)
				raw = []byte(`{"kind":`)
			}
			return key, raw
		}, nil, nil)
		result, plan, err := compilePlanFixture(t, &f)
		if !errors.Is(err, ErrValidation) || result.Valid || plan != nil {
			t.Fatal("malformed trailing input returned plan", err)
		}
	})
}

func TestCollectionPlanCancellationAndAdmission(t *testing.T) {
	f := stagedValidationFixture(t, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("private-hook")}))
	view, _ := f.catalog.Snapshot()
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	options := CollectionValidationOptions{CanRead: func(persistence.CatalogKey) bool {
		reads++
		if reads == 3 {
			cancel()
		}
		return true
	}}
	result, plan, err := f.catalog.compileStagedCollectionPlan(ctx, view, f.open(t, nil), options)
	if !errors.Is(err, context.Canceled) || result.Valid || plan != nil || reads < 3 {
		t.Fatal("in-progress cancellation exposed plan", reads, err)
	}
	if _, plan, err := f.catalog.compileStagedCollectionPlan(nil, view, f.open(t, nil), collectionReadAll); !errors.Is(err, ErrValidation) || plan != nil {
		t.Fatal("nil context accepted")
	}
	options.CanRead = func(persistence.CatalogKey) bool { return false }
	if _, plan, err := f.catalog.compileStagedCollectionPlan(context.Background(), view, f.open(t, nil), options); !errors.Is(err, errCollectionReadDenied) || plan != nil {
		t.Fatal("read denial returned plan", err)
	}
}

func TestCollectionPlanCompilerBudgetsBeforeAllocation(t *testing.T) {
	p := collectionPlanCompiler{maxBytes: collectionPlanMetadataBytes, maxVisits: collectionPlanVisits}
	if err := p.charge(collectionPlanMetadataBytes); err != nil || !errors.Is(p.charge(1), ErrGraphLimit) || !errors.Is(p.charge(-1), ErrGraphLimit) || p.bytes != collectionPlanMetadataBytes {
		t.Fatal("byte allowance overflowed or silently exceeded")
	}
	p = collectionPlanCompiler{maxBytes: 0, maxVisits: 1}
	ref := stagedItemRef{Ordinal: 1, Key: persistence.CatalogKey{Kind: "Credential", ID: "id"}}
	if !errors.Is(p.source(context.Background(), ref), ErrGraphLimit) || len(p.sources) != 0 || !errors.Is(p.visit(context.Background()), ErrGraphLimit) {
		t.Fatal("source allocation preceded budget check")
	}
	p = collectionPlanCompiler{maxBytes: collectionPlanMetadataBytes, maxVisits: collectionPlanVisits}
	v := stagedValidator{live: map[persistence.CatalogKey]stagedValidationVersion{}, desired: map[persistence.CatalogKey]stagedValidationVersion{}, item: map[persistence.CatalogKey]int{}}
	selected := map[persistence.CatalogKey]bool{}
	// Metadata-only synthetic pressure: repeated prefix guard footprints must
	// fail at the independent 32 MiB compiler quota, without any resource blobs.
	for i := range 250 {
		key := persistence.CatalogKey{Kind: "Credential", ID: fmt.Sprintf("%04d-", i) + strings.Repeat("g", 200)}
		v.live[key] = stagedValidationVersion{uid: "uid", revision: "revision", generation: 1}
		selected[key] = false
	}
	var order []persistence.CatalogKey
	for i := range 200 {
		key := persistence.CatalogKey{Kind: "Monitor", ID: fmt.Sprintf("monitor-%04d", i)}
		order = append(order, key)
		v.item[key] = i
		v.result.Items = append(v.result.Items, CollectionValidationItem{Change: "create"})
		v.desired[key] = stagedValidationVersion{}
		if err := p.source(context.Background(), stagedItemRef{Ordinal: uint64(i + 1), Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.order(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	for i, key := range order {
		err := p.prefix(context.Background(), &v, key, selected, map[persistence.CatalogKey]bool{key: true})
		if errors.Is(err, ErrGraphLimit) {
			if len(p.plan.Rows) != i || p.bytes > p.maxBytes || p.visits >= p.maxVisits {
				t.Fatal("row published before complete byte check, or work quota hid byte boundary")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("large repeated guard footprint bypassed compiler byte quota")
}
