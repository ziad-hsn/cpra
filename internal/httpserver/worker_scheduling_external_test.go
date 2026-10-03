//go:build externaljobs

package httpserver

import (
	"bytes"
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

func TestWorkerSchedulingHTTPSuccessiveChecksAndRestart(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "log"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			f := newWorkerSessionFixture(t)
			client := workerHTTPClient(t, f)
			assignment, first := startWorkerHTTPCheck(t, f, client)
			firstReceipt, err := client.Result(t.Context(), first)
			if err != nil || firstReceipt == nil || firstReceipt.Disposition != api.ReceiptDispositionAccepted {
				t.Fatal("first result", err)
			}
			original, _, err := f.store.WorkerExecution(t.Context(), first.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			next := original.Intent.Clone()
			next.ID, next.Revision = uuid.NewString(), uuid.NewString()
			next.Generation++
			next.Scheduled = time.Now().UTC()
			next.Deadline = next.Scheduled.Add(5 * time.Second)
			next.Payload = secureconfig.Envelope{}
			next, err = f.handler.catalog.PrepareWorkerExecution(t.Context(), next)
			if err != nil {
				t.Fatal("prepare next generation", err)
			}
			if _, err = f.store.CommitWorkerExecution(t.Context(), next); err != nil {
				t.Fatal("completed check blocked its successor", err)
			}
			f.request.SessionID, f.request.PollSequence = assignment.SessionID, 2
			offers, err := client.Poll(t.Context(), f.request)
			if err != nil || offers == nil || len(offers.Items) != 1 || offers.Items[0].ExecutionID != next.ID {
				t.Fatal("next check not offered", err)
			}
			started, err := client.Start(t.Context(), startWorkerOffer(offers.Items[0], api.StartModeBegin))
			if err != nil || started == nil || started.Disposition != api.StartDispositionGranted {
				t.Fatal("next check not granted", err)
			}
			second := first
			second.ExecutionID, second.GrantID = next.ID, started.GrantID
			secondReceipt, err := client.Result(t.Context(), second)
			if err != nil || secondReceipt == nil || secondReceipt.Disposition != api.ReceiptDispositionAccepted || secondReceipt.ReceiptID == firstReceipt.ReceiptID {
				t.Fatal("second result", err)
			}
			before, _ := f.store.Get(next.MonitorID)
			if before.TotalChecks != 2 || before.Generation != 2 || before.SuccessfulChecks != 2 {
				t.Fatal("successive results did not produce two distinct observations")
			}
			if snapshot {
				if err := f.store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			cfg := runtimeconfig.Default()
			cfg.Storage.Directory = f.dir
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal("restart", err)
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
				t.Fatal("restart authentication", err)
			}
			f.handler.store, f.handler.catalog, f.handler.ready = store, catalog, store.ControllerHealthContext
			for n, input := range []api.Outcome{first, second} {
				receipt, err := client.Result(t.Context(), input)
				if err != nil || !reflect.DeepEqual(receipt, []*api.Receipt{firstReceipt, secondReceipt}[n]) {
					t.Fatal("restarted result did not retain its original receipt", n, err)
				}
			}
			after, _ := store.Get(next.MonitorID)
			if before.TotalChecks != after.TotalChecks || before.Generation != after.Generation || before.SuccessfulChecks != after.SuccessfulChecks || !before.LastCheck.Equal(after.LastCheck) {
				t.Fatal("receipt replay repeated a health observation")
			}
		})
	}
}
