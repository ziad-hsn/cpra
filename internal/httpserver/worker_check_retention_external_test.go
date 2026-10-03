//go:build externaljobs

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerCheckRetentionHTTPLostReceiptAndRestart(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "log"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			f := newWorkerSessionFixture(t)
			client := workerHTTPClient(t, f)
			first, input := startWorkerHTTPCheck(t, f, client)
			if _, err := client.Result(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			original, _, err := f.store.WorkerExecution(t.Context(), input.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			next := original.Intent.Clone()
			next.ID, err = f.store.AllocateWorkerCheckID(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			next.Revision = uuid.NewString()
			next.Generation++
			next.Scheduled = time.Now().UTC()
			next.Deadline = next.Scheduled.Add(original.Intent.Deadline.Sub(original.Intent.Scheduled))
			next.Payload = secureconfig.Envelope{}
			next, err = f.handler.catalog.PrepareWorkerExecution(t.Context(), next)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.CommitWorkerExecution(t.Context(), next); err != nil {
				t.Fatal(err)
			}
			f.request.SessionID, f.request.PollSequence = first.SessionID, 2
			offers, err := client.Poll(t.Context(), f.request)
			if err != nil || offers == nil || len(offers.Items) != 1 {
				t.Fatal("numbered check", err)
			}
			assignment := offers.Items[0]
			grant, err := client.Start(t.Context(), startWorkerOffer(assignment, api.StartModeBegin))
			if err != nil || grant.Disposition != api.StartDispositionGranted {
				t.Fatal(err)
			}
			input.ExecutionID, input.GrantID = next.ID, grant.GrantID
			// The server commits the result, but the worker never receives its receipt.
			f.handler.admit = func(_ context.Context, fn func() error) error {
				if err := fn(); err != nil {
					return err
				}
				return persistence.ErrCommitUnconfirmed
			}
			if _, err = client.Result(t.Context(), input); !errors.Is(err, cpra.ErrAmbiguous) {
				t.Fatal("lost receipt", err)
			}
			f.handler.admit = func(_ context.Context, fn func() error) error { return fn() }
			record, _, err := f.store.WorkerExecution(t.Context(), next.ID)
			if err != nil || record.Result == nil {
				t.Fatal(err)
			}
			before, _ := f.store.Get(next.MonitorID)
			beforeJSON, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := f.store.RetireWorkerChecks(t.Context(), record.Result.RecordedAt.Add(persistence.WorkerCheckReceiptLifetime), 1); err != nil || n != 1 {
				t.Fatal(n, err)
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
				t.Fatal(err)
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
				t.Fatal("authenticated restart", err)
			}
			f.handler.store, f.handler.catalog, f.handler.ready = store, catalog, store.ControllerHealthContext
			if _, err := client.Result(t.Context(), input); !errors.Is(err, cpra.ErrWorkerCheckExpired) || errors.Is(err, cpra.ErrAmbiguous) {
				t.Fatal("expired result", err)
			}
			for _, mode := range []api.StartMode{api.StartModeBegin, api.StartModeReconcile} {
				if _, err := client.Start(t.Context(), startWorkerOffer(assignment, mode)); !errors.Is(err, cpra.ErrWorkerCheckExpired) {
					t.Fatal("expired start", err)
				}
			}
			if _, err := client.Poll(t.Context(), f.request); !errors.Is(err, cpra.ErrWorkerSessionExpired) {
				t.Fatal("old poll reply", err)
			}
			bad := input
			bad.WorkerUID = "different-worker"
			if _, err := client.Result(t.Context(), bad); err == nil || errors.Is(err, cpra.ErrWorkerCheckExpired) {
				t.Fatal("foreign worker", err)
			}
			bad = input
			bad.ServerID = "different-server"
			if _, err := client.Result(t.Context(), bad); err == nil || errors.Is(err, cpra.ErrWorkerCheckExpired) {
				t.Fatal("foreign server", err)
			}
			bad = input
			bad.Kind, bad.Status = "recovery", "unknown"
			if _, err := client.Result(t.Context(), bad); err == nil || errors.Is(err, cpra.ErrWorkerCheckExpired) {
				t.Fatal("action expiry", err)
			}
			after, _ := store.Get(next.MonitorID)
			afterJSON, err := json.Marshal(after)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(beforeJSON, afterJSON) {
				t.Fatal("retired requests changed health state")
			}
		})
	}
}
