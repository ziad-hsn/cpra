//go:build externaljobs

package httpserver

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerFinalizationAutomaticTimerSDKAndRestart(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			f := newWorkerSessionFixtureForCategory(t, category, true)
			if category == "check" {
				queueWorkerHTTPCheck(t, f)
			} else {
				queueWorkerHTTPAction(t, f, category)
			}
			client := workerHTTPClient(t, f)
			page, err := client.Poll(t.Context(), f.request)
			if err != nil || page == nil || len(page.Items) != 1 {
				t.Fatal("assignment", err)
			}
			a := page.Items[0]
			start, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin))
			if err != nil || start.Disposition != api.StartDispositionGranted {
				t.Fatal("start", err)
			}
			before, _ := f.store.Get(a.MonitorID)
			var final persistence.WorkerExecutionRecord
			until := time.Now().Add(10 * time.Second)
			for time.Now().Before(until) {
				final, _, err = f.store.WorkerExecution(t.Context(), a.ExecutionID)
				if err != nil {
					t.Fatal(err)
				}
				if final.Finalization != nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if final.Finalization == nil || final.Finalization.Reason != "deadline" || final.Result != nil {
				t.Fatal("real maintenance timer did not finalize missing report")
			}
			outcome := api.Outcome{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID,
				GrantID: start.GrantID, Kind: category, Status: map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category],
				Data: json.RawMessage(`"private-schema-canary"`), Evidence: []string{}}
			receipt, err := client.Result(t.Context(), outcome)
			if err != nil || receipt.ReceiptID != final.Finalization.Receipt.ReceiptID || string(receipt.Disposition) != final.Finalization.Receipt.Disposition {
				t.Fatal("late result changed original finalization", err)
			}
			input := api.LateEvidenceRequest{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID,
				OriginalReceiptID: receipt.ReceiptID, EvidenceID: uuid.NewString(), Data: outcome.Data,
				Diagnostic: "private-late-finalization-canary", Evidence: []string{}}
			first, err := client.LateEvidence(t.Context(), input)
			if err != nil {
				t.Fatal("late evidence after automatic finalization", err)
			}
			retained, _, err := f.store.WorkerExecution(t.Context(), a.ExecutionID)
			if err != nil || retained.Result != nil || !reflect.DeepEqual(retained.Finalization, final.Finalization) || len(retained.Evidence) != 1 {
				t.Fatal("evidence replaced finalization", err)
			}
			current, _ := f.store.Get(a.MonitorID)
			if current.TotalChecks != before.TotalChecks || current.LastOutcome != before.LastOutcome || current.IncidentID != before.IncidentID {
				t.Fatal("missing/late report changed health")
			}
			if category != "check" {
				action, exists, err := f.store.ActionContext(t.Context(), retained.Intent.ActionID)
				if err != nil || !exists || action.MonitorID != current.ID || action.State != persistence.Unknown || !action.Held {
					t.Fatal("uncertain action was not held", err)
				}
			}
			if err := f.handler.catalog.Verify(t.Context()); err != nil {
				t.Fatal("finalized evidence authentication", err)
			}
			if err := f.store.Snapshot(); err != nil {
				t.Fatal(err)
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			cfg := runtimeconfig.Default()
			cfg.Storage.Directory = f.dir
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal("snapshot restart", err)
			}
			defer store.Close()
			wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
			if err != nil {
				t.Fatal(err)
			}
			sealer, err := secureconfig.NewSealer(wrapper)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := management.NewCatalog(store, sealer)
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.Verify(t.Context()); err != nil {
				t.Fatal("restored finalization/evidence", err)
			}
			f.handler.store, f.handler.catalog, f.handler.ready = store, catalog, store.ControllerHealthContext
			replayed, err := client.LateEvidence(t.Context(), input)
			if err != nil || !reflect.DeepEqual(first, replayed) {
				t.Fatal("restart changed evidence receipt", err)
			}
			terminal, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeReconcile))
			if err != nil || terminal.ReceiptID != receipt.ReceiptID || terminal.Disposition == api.StartDispositionGranted {
				t.Fatal("restart regranted finalized work", err)
			}
		})
	}
}
