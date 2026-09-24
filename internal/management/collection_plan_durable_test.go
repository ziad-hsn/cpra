package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Exercise the compiler/codec/FSM seam with real encrypted inputs and Raft,
// without constructing prepared catalog mutations or starting a controller.
func TestCollectionPlanCompilerThroughDurableFinalization(t *testing.T) {
	ctx := context.Background()
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	logPath := filepath.Join(t.TempDir(), "private-plan-provider-output.log")
	const canary = "private-compiler-to-raft-credential-canary"
	monitor := collectionMonitor("api", target.URL)
	var spec api.MonitorSpec
	if err := json.Unmarshal(monitor.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Notifications = &map[string]api.AlertRule{"red": {NotifyType: api.Pointer("log"), GroupRef: api.Pointer("team")}}
	monitor.Spec, _ = json.Marshal(spec)
	resources := []api.Resource{
		monitor,
		resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}),
		resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}),
		planLogEndpoint("chat", logPath),
		resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer(canary)}),
	}
	store, _ := bootstrapTestStore(t, "raft", 1000)
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	f := stagedSourceFixtureStore(t, catalog, store, len(resources), func(i int) (persistence.CatalogKey, []byte) {
		raw, err := json.Marshal(resources[i])
		if err != nil {
			t.Fatal(err)
		}
		return persistence.CatalogKey{Kind: resources[i].Kind, ID: resources[i].Metadata.ID}, raw
	}, nil, nil)
	readOnly := collectionBlockSealing(t, catalog)
	view, err := catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	beforeCompile := store.Status().CommittedIndex
	source := f.open(t, nil)
	validation, plan, err := catalog.compileStagedCollectionPlan(ctx, view, source, collectionReadAll)
	if err != nil || !validation.Valid || plan == nil || len(plan.Rows) != len(resources) {
		t.Fatal("encrypted inventory failed compilation", err)
	}
	if source.liveBytes != 0 || readOnly.opens.Load() == 0 || store.Status().CommittedIndex != beforeCompile {
		t.Fatal("compilation did not remain a scoped read of encrypted input")
	}
	source.close()
	artifact, err := prepareCollectionPlanArtifact(ctx, plan, artifactPlanID)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	descriptor, err := artifact.writeTo(ctx, &encoded)
	if err != nil || descriptor != artifact.descriptor {
		t.Fatal("artifact changed its measured descriptor", err)
	}
	for _, private := range []string{canary, logPath, target.URL} {
		if bytes.Contains(encoded.Bytes(), []byte(private)) {
			t.Fatal("provider data entered the durable plan artifact")
		}
	}
	var parts []persistence.CollectionPlanLedgerFragment
	var rows []persistence.CollectionPlanRow
	decoded, err := persistence.DecodeCollectionPlan(ctx, bytes.NewReader(encoded.Bytes()), func(fragment persistence.CollectionPlanFragment) error {
		parts = append(parts, persistence.CollectionPlanLedgerFragment{Ordinal: uint64(len(parts) + 1), Fragment: fragment})
		if fragment.Row != nil {
			rows = append(rows, *fragment.Row)
		}
		return nil
	})
	if err != nil || decoded != descriptor || uint64(len(parts)) != descriptor.Fragments || len(rows) != len(resources) {
		t.Fatal("complete compiler artifact failed the durable codec", err)
	}
	wantKinds := []string{"Credential", "NotificationEndpoint", "Recipient", "NotificationGroup", "Monitor"}
	for i, row := range rows {
		if row.Ordinal != uint64(i+1) || row.Key.Kind != wantKinds[i] || row.Key != validation.Order[i] || row.InputOrdinal == 0 || row.InputOrdinal > uint64(len(f.items)) {
			t.Fatal("artifact lost dependency order or original input ordinal")
		}
		input := f.items[row.InputOrdinal-1]
		if row.Key != input.Key || row.Source != input.Source || row.Document != input.SourceDocument || row.Item != input.SourceItem {
			t.Fatal("dependency reorder changed source attribution")
		}
	}
	at := f.at.Add(time.Millisecond)
	state := submitStagedSource(t, store, persistence.CollectionCommand{
		Action: "plan_begin", OperationID: f.head.ID, UploadID: f.head.UploadID,
		PlanBegin: &persistence.CollectionPlanBegin{Header: artifact.header, Descriptor: descriptor},
	}, at)
	if state.Phase != "validating" || state.Plan == nil || state.Plan.Header.InputProgressDigest != f.head.ProgressDigest {
		t.Fatal("begin detached the plan from the encrypted inventory")
	}
	for i := range parts {
		at = at.Add(time.Millisecond)
		state = submitStagedSource(t, store, persistence.CollectionCommand{
			Action: "plan_append", OperationID: f.head.ID, UploadID: f.head.UploadID,
			PlanID: artifact.header.PlanID, PlanFragment: &parts[i],
		}, at)
	}
	if state.Plan.UploadedFragments != descriptor.Fragments || state.Plan.ArtifactBytes != descriptor.Bytes {
		t.Fatal("committed plan prefix differs from complete compiler artifact")
	}
	beforeVerify := store.Status().CommittedIndex
	verified, err := store.VerifyCollectionPlan(ctx, f.head.ID, at)
	if err != nil || verified.Descriptor != descriptor || verified.InputProgressDigest != f.head.ProgressDigest || verified.InputCount != f.head.ItemCount || store.Status().CommittedIndex != beforeVerify {
		t.Fatal("immutable durable verification changed or detached the artifact", err)
	}
	finish := persistence.CollectionCommand{Action: "plan_finalize", OperationID: f.head.ID, UploadID: f.head.UploadID, PlanFinalize: &verified}
	at = at.Add(time.Millisecond)
	finalized := submitStagedSource(t, store, finish, at)
	if finalized.Phase != "validated" || finalized.Plan == nil || finalized.Plan.Descriptor != descriptor || !finalized.Plan.FinalizedAt.Equal(at) {
		t.Fatal("verified artifact did not reach the inactive validated phase")
	}
	for i := range 2 {
		retry := submitStagedSource(t, store, finish, at.Add(time.Duration(i+1)*time.Second))
		if !reflect.DeepEqual(retry, finalized) {
			t.Fatal("exact finalization retry replaced the verdict or renewed its deadline")
		}
	}
	active, err := store.CatalogSnapshot()
	if err != nil || active.Len() != 0 {
		t.Fatal("durable plan staging activated catalog resources", err)
	}
	if requests.Load() != 0 || readOnly.wraps.Load() != 0 {
		t.Fatal("plan pipeline invoked the HTTP target or sealed provider resources")
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plan pipeline opened the configured notification log", err)
	}
	t.Logf("real Raft: %d encrypted input rows, %d source-bound plan rows, %d committed fragments, two identical finalization retries; zero active resources, HTTP requests or notification log writes", len(f.items), len(rows), len(parts))
}
