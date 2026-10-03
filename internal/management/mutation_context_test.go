package management

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestPrepareCancellationBeforeUnavailableCatalog(t *testing.T) {
	c, store := testCatalog(t)
	c.verified.Store(false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	prepared, err := c.Prepare(ctx, resource("NotificationEndpoint", "console", api.DriverConfig{Type: "log"}), "", true)
	if prepared != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("prepare lost request cancellation", prepared, err)
	}
	if view, err := store.CatalogSnapshot(); err != nil || view.Len() != 0 {
		t.Fatal("cancelled preparation wrote catalog state", err)
	}
}

func TestMutationCancellationDoesNotAllocateOperations(t *testing.T) {
	c, store := testCatalog(t)
	endpoint := createResource(t, c, resource("NotificationEndpoint", "console", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-validation-only.log"}`)}))
	index := store.Status().CommittedIndex
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, expire := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer expire()
	for _, input := range []struct {
		name string
		ctx  context.Context
		want error
	}{{"cancelled", cancelled, context.Canceled}, {"deadline", expired, context.DeadlineExceeded}} {
		t.Run(input.name, func(t *testing.T) {
			for _, operation := range []struct {
				name string
				run  func(context.Context) error
			}{
				{"prepare", func(ctx context.Context) error {
					_, err := c.Prepare(ctx, endpoint, endpoint.Metadata.ResourceVersion, false)
					return err
				}},
				{"delete", func(ctx context.Context) error {
					_, err := c.PrepareDelete(ctx, endpoint.Kind, endpoint.Metadata.ID, endpoint.Metadata.ResourceVersion)
					return err
				}},
				{"control", func(ctx context.Context) error {
					_, err := c.PrepareControl(ctx, "snooze", "monitor", api.ControlRequest{Revision: "1"})
					return err
				}},
				{"action", func(ctx context.Context) error {
					_, err := c.PrepareAction(ctx, "recover", "monitor", api.ControlRequest{Revision: "1"})
					return err
				}},
				{"commit", func(ctx context.Context) error {
					p := &PreparedChange{catalog: c}
					_, err := c.CommitAs(ctx, p, "operator")
					if p.OperationID() != "" {
						t.Fatal("cancelled mutation allocated an operation")
					}
					return err
				}},
				{"commit-control", func(ctx context.Context) error {
					p := &PreparedControl{catalog: c}
					_, err := c.CommitControlAs(ctx, p, "operator")
					if p.OperationID() != "" {
						t.Fatal("cancelled control allocated an operation")
					}
					return err
				}},
				{"commit-action", func(ctx context.Context) error {
					p := &PreparedAction{catalog: c}
					_, err := c.CommitActionAs(ctx, p, "operator")
					if p.OperationID() != "" {
						t.Fatal("cancelled action allocated an operation")
					}
					return err
				}},
			} {
				t.Run(operation.name, func(t *testing.T) {
					if err := operation.run(input.ctx); !errors.Is(err, input.want) {
						t.Fatalf("got %v, want %v", err, input.want)
					}
				})
			}
		})
	}
	if store.Status().CommittedIndex != index || c.failed.Load() || !c.Ready() {
		t.Fatal("cancelled work changed committed state or catalog health")
	}
}

func TestSubmissionClassificationPreservesUnknownOutcome(t *testing.T) {
	c, _ := testCatalog(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	uncertain := errors.Join(persistence.ErrCommitUnconfirmed, context.Canceled)
	if got := c.submissionError(ctx, uncertain); !errors.Is(got, ErrOutcomeUnconfirmed) || !errors.Is(got, persistence.ErrCommitUnconfirmed) || !errors.Is(got, context.Canceled) {
		t.Fatal("cancelled diagnostic erased an uncertain admitted write", got)
	}
	if got := c.submissionError(ctx, context.Canceled); !errors.Is(got, context.Canceled) || errors.Is(got, ErrOutcomeUnconfirmed) {
		t.Fatal("definitive cancellation became an unknown write", got)
	}
}
