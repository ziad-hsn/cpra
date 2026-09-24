package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func reconciliationContextFixture(t *testing.T) (*Catalog, *persistence.Store, ReadView, string, api.Resource) {
	t.Helper()
	c, store := testCatalog(t)
	monitor := resource("Monitor", "context-owner", api.MonitorSpec{Check: api.CheckSpec{
		Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health"}`)},
	}})
	prepared, err := c.Prepare(context.Background(), monitor, "", true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Commit(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	view, err := c.SnapshotContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c, store, view, result.Operation.ID, result.Resource
}

func TestReconciliationContextRejectsCanceledWorkWithoutMutation(t *testing.T) {
	c, store, view, operationID, monitor := reconciliationContextFixture(t)
	before := store.Status().CommittedIndex
	receipt, err := store.Operation(operationID)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	for _, input := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceled, context.Canceled},
		{"deadline", expired, context.DeadlineExceeded},
		{"nil", nil, ErrValidation},
	} {
		t.Run(input.name, func(t *testing.T) {
			for _, action := range []struct {
				name string
				run  func(context.Context) error
			}{
				{"snapshot", func(ctx context.Context) error {
					got, err := c.SnapshotContext(ctx)
					if got.catalog != nil || got.Index() != 0 {
						t.Fatal("canceled snapshot returned a retained view")
					}
					return err
				}},
				{"page", func(ctx context.Context) error {
					items, next, err := view.Page(ctx, "Monitor", "", 100)
					if len(items) != 0 || next != "" {
						t.Fatal("canceled page returned resources or a continuation")
					}
					return err
				}},
				{"prepare", func(ctx context.Context) error {
					got, err := c.PrepareRuntime(ctx, view, monitor.Metadata.ID)
					if !reflect.DeepEqual(got, RuntimePreparation{}) {
						t.Fatal("canceled preparation returned private runtime data")
					}
					return err
				}},
				{"observation", func(ctx context.Context) error {
					got, err := c.observedResource(ctx, monitor)
					if !reflect.DeepEqual(got, api.Resource{}) {
						t.Fatal("canceled observation returned a resource")
					}
					return err
				}},
				{"completion", func(ctx context.Context) error { return c.CompleteOperation(ctx, operationID, true) }},
			} {
				t.Run(action.name, func(t *testing.T) {
					if err := action.run(input.ctx); !errors.Is(err, input.want) {
						t.Fatalf("got %v, want %v", err, input.want)
					}
				})
			}
		})
	}
	after, err := store.Operation(operationID)
	if err != nil || !reflect.DeepEqual(after, receipt) || store.Status().CommittedIndex != before {
		t.Fatal("canceled reconciliation mutated the original receipt or durable index", err)
	}
	if c.failed.Load() || !c.Ready() {
		t.Fatal("canceled reconciliation poisoned catalog health")
	}
	if err := c.CompleteOperation(context.Background(), operationID, true); err != nil {
		t.Fatal("healthy completion could not resume", err)
	}
	// Even an already completed receipt must honor cancellation before taking
	// the ordinary idempotent success path.
	if err := c.CompleteOperation(canceled, operationID, true); !errors.Is(err, context.Canceled) {
		t.Fatal("completed receipt bypassed cancellation", err)
	}
}

type cancellationKeyWrapper struct {
	secureconfig.KeyWrapper
	entered chan context.Context
}

func (w *cancellationKeyWrapper) Unwrap(ctx context.Context, _, _ []byte) ([]byte, error) {
	w.entered <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestReconciliationContextCancelsCooperativeKeyWork(t *testing.T) {
	for _, operation := range []string{"page", "prepare", "verify"} {
		t.Run(operation, func(t *testing.T) {
			c, store, view, operationID, monitor := reconciliationContextFixture(t)
			before := store.Status().CommittedIndex
			receipt, err := store.Operation(operationID)
			if err != nil {
				t.Fatal(err)
			}
			originalSealer := c.sealer
			local, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
			if err != nil {
				t.Fatal(err)
			}
			wrapper := &cancellationKeyWrapper{KeyWrapper: local, entered: make(chan context.Context, 1)}
			c.sealer, err = secureconfig.NewSealer(wrapper)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "verify" {
				c.verified.Store(false)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "page":
					items, next, err := view.Page(ctx, "Monitor", "", 100)
					if len(items) != 0 || next != "" {
						done <- errors.New("canceled page returned partial data")
						return
					}
					done <- err
				case "prepare":
					got, err := c.PrepareRuntime(ctx, view, monitor.Metadata.ID)
					if !reflect.DeepEqual(got, RuntimePreparation{}) {
						done <- errors.New("canceled preparation returned private runtime data")
						return
					}
					done <- err
				case "verify":
					done <- c.Verify(ctx)
				}
			}()
			select {
			case received := <-wrapper.entered:
				if received != ctx {
					t.Fatal("key operation lost the caller context")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("key work never started")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("key cancellation was not preserved", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cooperative key operation did not stop")
			}
			if c.failed.Load() || store.ControllerHealth() != nil || operation == "verify" && c.verified.Load() {
				t.Fatal("cancellation poisoned health or completed startup verification")
			}
			after, err := store.Operation(operationID)
			if err != nil || !reflect.DeepEqual(receipt, after) || store.Status().CommittedIndex != before {
				t.Fatal("canceled key work mutated durable state", err)
			}
			c.sealer = originalSealer
			if err := c.Verify(context.Background()); err != nil || !c.Ready() {
				t.Fatal("catalog could not resume after canceled key work", err)
			}
		})
	}
}

func TestReconciliationContextCanceledVerificationKeepsAdmissionClosed(t *testing.T) {
	c, store := testCatalog(t)
	c.verified.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Verify(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("startup verification lost cancellation", err)
	}
	if c.verified.Load() || c.failed.Load() || store.ControllerHealth() != nil {
		t.Fatal("canceled startup verification changed admission or health")
	}
}
