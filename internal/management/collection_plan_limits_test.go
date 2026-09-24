package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// A valid final union is insufficient: changing the endpoint before moving its
// existing recipient would temporarily change the outside monitor's delivery
// type. Compilation must not expose the rows accumulated before that rejection.
func TestCollectionPlanUnsafePrefixReturnsNoPlan(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	const marker = "private-unsafe-plan-credential"
	inputs := []api.Resource{
		resource("Credential", "independent", api.CredentialSpec{Value: api.Pointer(marker)}),
		resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"b"}}),
		resource("NotificationEndpoint", "a", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"private-never-opened-plan.log"}`)}),
	}
	f := stagedValidationFixture(t, inputs...)
	for _, id := range []string{"a", "b"} {
		createResource(t, f.catalog, resource("Credential", id, api.CredentialSpec{Value: api.Pointer(target.URL + "/" + marker + id)}))
		createResource(t, f.catalog, resource("NotificationEndpoint", id, api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": id})}))
	}
	createResource(t, f.catalog, resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"a"}}))
	monitor := collectionMonitor("outside", target.URL)
	var spec api.MonitorSpec
	if err := json.Unmarshal(monitor.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Notifications = &map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), RecipientRefs: api.Pointer([]string{"oncall"})}}
	monitor.Spec, _ = json.Marshal(spec)
	createResource(t, f.catalog, monitor)
	view, err := f.catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	source := f.open(t, nil)
	index := f.store.Status().CommittedIndex
	before, _, err := f.store.CollectionGet(f.head.ID)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := collectionBlockSealing(t, f.catalog)
	result, plan, err := f.catalog.compileStagedCollectionPlan(t.Context(), view, source, collectionReadAll)
	if !errors.Is(err, ErrValidation) || result.Valid || plan != nil || result.Items[2].Issue != "unsafePrefix" ||
		!slices.Contains(result.Impacted, persistence.CatalogKey{Kind: "Monitor", ID: "outside"}) {
		t.Fatal("unsafe intermediate resource union exposed a plan", err)
	}
	after, _, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !reflect.DeepEqual(before, after) || f.store.Status().CommittedIndex != index || wrapper.wraps.Load() != 0 || calls.Load() != 0 {
		t.Fatal("rejected compilation changed staging, sealed output or invoked a provider", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || bytes.Contains(encoded, []byte(marker)) || bytes.Contains(encoded, []byte("private-never-opened-plan.log")) || source.liveBytes != 0 {
		t.Fatal("rejected compilation retained private values or borrowed-byte reservations")
	}
}

func TestCollectionPlanFinalSourceFenceRejectsCommittedRetirement(t *testing.T) {
	for _, mode := range []string{"cancel", "expire"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer target.Close()
			const marker = "private-final-plan-source"
			f := stagedValidationFixture(t, collectionMonitor("api", target.URL+"/"+marker),
				resource("Credential", "independent", api.CredentialSpec{Value: api.Pointer(marker)}))
			view, err := f.catalog.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			source := f.open(t, nil)
			wrapper := collectionBlockSealing(t, f.catalog)
			index := f.store.Status().CommittedIndex
			observations := 0
			source.now = func() time.Time { observations++; return f.at }
			valid, baseline, err := f.catalog.compileStagedCollectionPlan(t.Context(), view, source, collectionReadAll)
			if err != nil || !valid.Valid || baseline == nil || len(baseline.Rows) != 2 || observations < 2 || f.store.Status().CommittedIndex != index {
				t.Fatal("pure baseline did not reach complete plan", err)
			}
			// The last clock observation is finish's final protected source check,
			// after every prefix row has compiled. Calibrating against the identical
			// pure invocation avoids depending on a hard-coded internal read count.
			lastObservation := observations
			observations = 0
			retired := false
			var retirementIndex uint64
			source.now = func() time.Time {
				observations++
				if observations == lastObservation {
					at := f.at.Add(time.Second)
					command := persistence.CollectionCommand{Action: "cancel", OperationID: f.head.ID, UploadID: f.head.UploadID,
						Cancel: &persistence.CollectionCancellation{ID: uuid.NewString(), Actor: f.head.Actor, At: at}}
					if mode == "expire" {
						at = f.head.ExpiresAt
						command.Action, command.Cancel = "cleanup", nil
						command.Cleanup = &persistence.CollectionCleanup{Uploaded: f.head.Uploaded, EncodedBytes: f.head.EncodedBytes,
							RemovedRows: f.head.RemovedRows, RemovedBytes: f.head.RemovedBytes, ActivityAt: f.head.ActivityAt}
					}
					results, submitErr := f.store.Submit(context.Background(), []persistence.Command{{Kind: "collection", At: at, Collection: &command}})
					if submitErr != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
						t.Fatal("test source retirement failed", submitErr)
					}
					retired, retirementIndex = true, f.store.Status().CommittedIndex
				}
				return f.at
			}
			result, plan, err := f.catalog.compileStagedCollectionPlan(t.Context(), view, source, collectionReadAll)
			if !retired || observations != lastObservation || !errors.Is(err, persistence.ErrOperationExpired) || result.Valid || plan != nil {
				t.Fatal("final committed source retirement exposed a plan", retired, observations, lastObservation, err)
			}
			active, snapshotErr := f.store.CatalogSnapshot()
			if snapshotErr != nil || active.Len() != 0 || f.store.Status().CommittedIndex != retirementIndex || retirementIndex <= index || wrapper.wraps.Load() != 0 || calls.Load() != 0 {
				t.Fatal("compiler performed work beyond the test's explicit retirement", snapshotErr)
			}
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil || bytes.Contains(encoded, []byte(marker)) || source.liveBytes != 0 || source.peakBytes == 0 {
				t.Fatal("late rejection retained private result bytes or reservations")
			}
			source.close()
			if source.key != [32]byte{} || source.fingerprint != [32]byte{} {
				t.Fatal("closed rejected source retained explicit key material")
			}
		})
	}
}
