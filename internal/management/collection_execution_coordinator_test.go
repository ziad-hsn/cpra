package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func executionTestWorker(t *testing.T, f collectionSourceFixture, head persistence.CollectionState) *CollectionExecutionCoordinator {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &CollectionExecutionCoordinator{catalog: f.catalog, profile: head.Activation.CapabilitiesDigest,
		now: collectionClock(head.Activation.At.Add(10 * time.Second)), ctx: ctx, cancel: cancel, done: make(chan struct{}), submit: f.store.Submit, flush: f.store.Flush}
	t.Cleanup(func() { cancel(); w.clearPending() })
	return w
}

func executionAdmitFixture(t *testing.T, f *collectionSourceFixture) persistence.CollectionState {
	t.Helper()
	coordinatorRequest(t, f)
	head, err := f.catalog.runCollectionValidation(context.Background(), f.head.ID, uuid.NewString(), collectionClock(f.at.Add(2*time.Second)))
	if err != nil || head.Validation == nil || !head.Validation.Header.Valid {
		t.Fatal("original collection validation", err)
	}
	at := f.at.Add(3 * time.Second)
	for !head.Validation.HistorySealed {
		head = submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "validation_publish", OperationID: head.ID, UploadID: head.UploadID, ValidationID: head.Validation.Header.ResultID, ValidationPublished: head.Validation.Published}, at)
	}
	authority, err := f.store.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	activation := persistence.CollectionActivation{ID: uuid.NewString(), Authority: authority, InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
		ResultID: head.Validation.Header.ResultID, ResultDescriptor: head.Validation.Descriptor, PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor,
		CapabilitiesDigest: head.Validation.Header.CapabilitiesDigest, ValidationRequest: persistence.CollectionValidationRequestFenceFor(head), At: at}
	return submitStagedSource(t, f.store, persistence.CollectionCommand{Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID, Activation: &activation, ActivationAuthority: &authority}, at)
}

func TestCollectionExecutionCoordinatorDependencyOrderAndPartialConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "never-opened.log")
			secret := "PRIVATE-EXECUTOR-CANARY"
			input := []api.Resource{
				collectionMonitor("service", "http://original.example/health"),
				resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}),
				resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"log"}}),
				planLogEndpoint("log", logPath),
				resource("Credential", "secret", api.CredentialSpec{Value: &secret}),
			}
			f := coordinatorFixture(t, input...)
			head := executionAdmitFixture(t, &f)
			if conflict {
				createResource(t, f.catalog, input[3]) // Outside writer after validation.
			}
			w, err := f.catalog.StartCollectionExecutionCoordinator(context.Background(), collectionClock(head.Activation.At.Add(10*time.Second)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { executionStop(t, w) })
			settled := executionWaitProcessed(t, w, head.ID, uint64(len(input)))
			executionStop(t, w)
			if conflict {
				if settled.Execution.Accepted != 2 || settled.Execution.Conflicts != 1 || settled.Execution.DependencyBlocked != 2 {
					t.Fatal("partial result lost original dependency conflict")
				}
			} else if settled.Execution.Accepted != uint64(len(input)) || settled.Execution.Conflicts != 0 || settled.Execution.DependencyBlocked != 0 {
				t.Fatal("dependency-ordered original collection failed")
			}
			if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("collection execution opened notification destination")
			}
			view, err := f.store.CatalogSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			for _, resource := range input {
				record, exists, err := f.store.CatalogGet(persistence.CatalogKey{Kind: resource.Kind, ID: resource.Metadata.ID})
				if err != nil {
					t.Fatal(err)
				}
				if conflict && (resource.Kind == "Recipient" || resource.Kind == "NotificationGroup") {
					if exists {
						t.Fatal("failed dependency was activated")
					}
					continue
				}
				if !exists {
					t.Fatal("accepted resource missing from catalog")
				}
				encoded, _ := json.Marshal(record)
				if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte(logPath)) {
					t.Fatal("plaintext execution input escaped encryption")
				}
			}
			if !conflict && view.Len() != len(input) || conflict && view.Len() != 3 {
				t.Fatal("unexpected active resources after original decisions")
			}
		})
	}
}

func executionWork(t *testing.T, w *CollectionExecutionCoordinator, id string) persistence.CollectionExecutionWork {
	t.Helper()
	items, err := w.catalog.store.CollectionExecutionWork(w.ctx, w.now())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.OperationID == id {
			return item
		}
	}
	t.Fatal("original execution missing from work selection")
	return persistence.CollectionExecutionWork{}
}

func executionAdvance(t *testing.T, w *CollectionExecutionCoordinator, id string) {
	t.Helper()
	item := executionWork(t, w, id)
	if err := w.prepareStep(w.ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := w.submitStep(w.ctx, item.Actor); err != nil {
		t.Fatal(err)
	}
}

func executionWaitProcessed(t *testing.T, w *CollectionExecutionCoordinator, id string, count uint64) persistence.CollectionState {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		head, ok, err := w.catalog.store.CollectionGet(id)
		if err != nil || !ok {
			t.Fatal(err)
		}
		if head.Execution != nil && head.Execution.Processed == count {
			if head.ExecutionResult != nil {
				return head
			}
			for _, status := range w.Status() {
				if status.OperationID == id && status.Condition == "awaitingCompletion" {
					return head
				}
			}
		}
		select {
		case <-w.Done():
			t.Fatal("coordinator stopped before the original decisions", w.Err())
		case <-deadline.C:
			t.Fatal("coordinator did not settle", w.Status())
		case <-tick.C:
		}
	}
}

func executionStop(t *testing.T, w *CollectionExecutionCoordinator) {
	t.Helper()
	w.BeginStop()
	if w.Ready() {
		t.Fatal("stopping coordinator remained ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionExecutionCoordinatorRealInputAndPreparedRestart(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			var providerCalls atomic.Uint64
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerCalls.Add(1) }))
			defer target.Close()
			f, head, config := candidateExecutionFixture(t, disk, collectionMonitor("managed", target.URL))
			var prepared persistence.CollectionPreparedItem
			if disk {
				prepared, _, head = candidateExecutionPrepare(t, f, head)
				if err := f.store.Snapshot(); err != nil {
					t.Fatal(err)
				}
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := persistence.Open(context.Background(), *config)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				f.store, f.catalog = reopened, candidateExecutionCatalog(t, reopened)
			}
			w, err := f.catalog.StartCollectionExecutionCoordinator(context.Background(), collectionClock(head.Activation.At.Add(10*time.Second)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { executionStop(t, w) })
			settled := executionWaitProcessed(t, w, head.ID, 1)
			if settled.Execution.Accepted != 1 || settled.Execution.ChildTerminals != 0 || settled.Phase != "applying" {
				t.Fatal("item admission incorrectly claimed parent/controller completion")
			}
			got, exists, err := f.store.CatalogGet(persistence.CatalogKey{Kind: "Monitor", ID: "managed"})
			if err != nil || !exists || disk && (got.UID != prepared.Record.UID || got.Revision != prepared.Record.Revision || !reflect.DeepEqual(got.Payload, prepared.Record.Payload)) {
				t.Fatal("prepared restart changed the original encrypted identity", err)
			}
			if providerCalls.Load() != 0 {
				t.Fatal("private management execution invoked provider")
			}
			status := w.Status()
			status[0].Condition = "caller mutation"
			if w.Status()[0].Condition == "caller mutation" {
				t.Fatal("status leaked mutable owner state")
			}
			executionStop(t, w)
			if _, err := f.catalog.StartCollectionExecutionCoordinator(context.Background(), time.Now); !errors.Is(err, persistence.ErrCollectionExecutionCoordinatorRegistered) {
				t.Fatal("stopped owner registration reused", err)
			}
		})
	}
}

func TestCollectionExecutionCoordinatorConflictAndUnchangedWithoutCrypto(t *testing.T) {
	for _, mode := range []string{"prepared-target-conflict", "unchanged-target-conflict", "unchanged"} {
		t.Run(mode, func(t *testing.T) {
			desired := collectionMonitor("service", "http://original.example/health")
			f, head := candidateFixture(t, desired, nil)
			if mode != "prepared-target-conflict" {
				f, head = candidateFixture(t, desired, &desired)
			} else {
				_, _, head = candidateExecutionPrepare(t, f, head)
				createResource(t, f.catalog, desired)
			}
			if mode == "unchanged-target-conflict" {
				original, err := f.catalog.Get(context.Background(), "Monitor", "service")
				if err != nil {
					t.Fatal(err)
				}
				name := "changed by another operator"
				original.Metadata.Name = &name
				mutation, err := f.catalog.Prepare(context.Background(), original, original.Metadata.ResourceVersion, false)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.catalog.CommitAs(context.Background(), mutation, "team/operator"); err != nil {
					t.Fatal(err)
				}
			}
			inner, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{99}, 32))
			f.catalog.sealer, _ = secureconfig.NewSealer(candidateFailKeys{KeyWrapper: inner, failOpen: true})
			w, err := f.catalog.StartCollectionExecutionCoordinator(context.Background(), collectionClock(head.Activation.At.Add(10*time.Second)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { executionStop(t, w) })
			settled := executionWaitProcessed(t, w, head.ID, 1)
			if mode == "unchanged" && settled.Execution.Unchanged != 1 || mode != "unchanged" && settled.Execution.Conflicts != 1 || settled.Execution.Accepted != 0 {
				t.Fatal("did not retain original conflict/unchanged outcome")
			}
			executionStop(t, w)
		})
	}
}

func TestCollectionExecutionCoordinatorUncertainRepliesAndBarrier(t *testing.T) {
	for _, action := range []string{"prepare", "decide"} {
		t.Run(action, func(t *testing.T) {
			f, head := candidateFixture(t, collectionMonitor("service", "http://original.example/health"), nil)
			w := executionTestWorker(t, f, head)
			executionAdvance(t, w, head.ID) // begin
			if action == "decide" {
				executionAdvance(t, w, head.ID) // prepare
			}
			item := executionWork(t, w, head.ID)
			if err := w.prepareStep(w.ctx, item); err != nil {
				t.Fatal(err)
			}
			original := w.pending.command
			calls := 0
			w.submit = func(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
				calls++
				result, err := f.store.Submit(ctx, commands)
				if err != nil {
					return result, err
				}
				return nil, persistence.ErrCommitUnconfirmed // Actual commit, deliberately lost reply.
			}
			if err := w.submitStep(w.ctx, item.Actor); !errors.Is(err, ErrOutcomeUnconfirmed) || !w.pending.barrier || calls != 1 {
				t.Fatal("lost reply did not retain original command", err)
			}
			committed, _, _ := f.store.CollectionGet(head.ID)
			w.flush = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
			ctx, cancel := context.WithTimeout(w.ctx, 20*time.Millisecond)
			err := w.submitStep(ctx, item.Actor)
			cancel()
			if !errors.Is(err, ErrOutcomeUnconfirmed) || calls != 1 || !w.pending.barrier || w.pending.command.Binding != original.Binding || w.pending.command.PreparedID != original.PreparedID {
				t.Fatal("uncertain barrier retried or released identity", err)
			}
			if original.Prepared != nil && !candidateExecutionSameWire(*original.Prepared, *w.pending.command.Prepared) {
				t.Fatal("lost preparation reply replaced ciphertext")
			}
			w.flush, w.submit = f.store.Flush, f.store.Submit
			if err := w.submitStep(w.ctx, item.Actor); err != nil || w.pending != nil {
				t.Fatal("exact ordered reconciliation failed", err)
			}
			after, _, _ := f.store.CollectionGet(head.ID)
			if !reflect.DeepEqual(after, committed) {
				t.Fatal("uncertain retry changed durable progress or identities")
			}
		})
	}
}

func TestCollectionExecutionCoordinatorBackpressureRetainsCandidate(t *testing.T) {
	f, head := candidateFixture(t, collectionMonitor("service", "http://original.example/health"), nil)
	w := executionTestWorker(t, f, head)
	executionAdvance(t, w, head.ID)
	item := executionWork(t, w, head.ID)
	if err := w.prepareStep(w.ctx, item); err != nil {
		t.Fatal(err)
	}
	original := w.pending.command.Prepared.Clone()
	w.submit = func(context.Context, []persistence.Command) ([]persistence.Result, error) {
		return []persistence.Result{{Err: persistence.ErrCollectionQuota}}, nil
	}
	if err := w.submitStep(w.ctx, item.Actor); !errors.Is(err, persistence.ErrCollectionQuota) || w.pending == nil || w.pending.barrier {
		t.Fatal("confirmed pressure lost pending original", err)
	}
	w.submit = f.store.Submit
	if err := w.submitStep(w.ctx, item.Actor); err != nil {
		t.Fatal(err)
	}
	got := executionWork(t, w, head.ID)
	if got.Prepared == nil || got.Prepared.ID != original.ID {
		t.Fatal("pressure retry minted a new candidate")
	}
}

func TestCollectionExecutionCoordinatorLostDecisionAtEndReconciles(t *testing.T) {
	f, head := candidateFixture(t, collectionMonitor("service", "http://original.example/health"), nil)
	w := executionTestWorker(t, f, head)
	if err := f.store.RegisterCollectionExecutionCoordinator(w.ctx, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var lost bool
	var decisions []persistence.CollectionExecuteCommand
	barriers := 0
	w.flush = func(ctx context.Context) error {
		barriers++
		return f.store.Flush(ctx)
	}
	w.submit = func(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
		if commands[0].CollectionExecute.Action == "decide" {
			decisions = append(decisions, *commands[0].CollectionExecute)
		}
		result, err := f.store.Submit(ctx, commands)
		if err == nil && commands[0].CollectionExecute.Action == "decide" && !lost {
			lost = true
			return nil, persistence.ErrCommitUnconfirmed
		}
		return result, err
	}
	w.ready.Store(true)
	go func() {
		w.err = w.loop()
		w.ready.Store(false)
		w.clearPending()
		close(w.done)
	}()
	t.Cleanup(func() { executionStop(t, w) })
	settled := executionWaitProcessed(t, w, head.ID, 1)
	executionStop(t, w)
	if !lost || barriers != 1 || len(decisions) != 2 || !reflect.DeepEqual(decisions[0], decisions[1]) || settled.Execution.Accepted != 1 || settled.Execution.Prepared != nil {
		t.Fatal("last-item lost reply duplicated or abandoned original decision")
	}
}

func TestCollectionExecutionCoordinatorAdmissionFailureBeforePreparedDecision(t *testing.T) {
	f, head := candidateFixture(t, collectionMonitor("service", "http://original.example/health"), nil)
	_, _, head = candidateExecutionPrepare(t, f, head)
	w := executionTestWorker(t, f, head)
	item := executionWork(t, w, head.ID)
	if err := w.prepareStep(w.ctx, item); err != nil {
		t.Fatal(err)
	}
	w.pending.barrier = true
	barriers, submits := 0, 0
	w.flush = func(ctx context.Context) error { barriers++; return f.store.Flush(ctx) }
	w.submit = func(context.Context, []persistence.Command) ([]persistence.Result, error) { submits++; return nil, nil }
	f.catalog.failed.Store(true) // Sibling management owner closes admission only.
	if err := w.submitStep(w.ctx, item.Actor); !errors.Is(err, ErrUnavailable) || barriers != 1 || submits != 0 || w.pending == nil || w.pending.barrier {
		t.Fatal("prepared decision bypassed admission or uncertainty ordering", err)
	}
	after, _, err := f.store.CollectionGet(head.ID)
	if err != nil || !reflect.DeepEqual(after, head) {
		t.Fatal("failed admission changed committed prepared item", err)
	}
}

func TestCollectionExecutionCoordinatorShutdownJoinsUncooperativeCrypto(t *testing.T) {
	f, head := candidateFixture(t, collectionMonitor("service", "http://original.example/health"), nil)
	keys := candidateObserveKeys(t, f.catalog)
	entered, release := make(chan struct{}), make(chan struct{})
	keys.beforeWrap = func(context.Context) { close(entered); <-release }
	w, err := f.catalog.StartCollectionExecutionCoordinator(context.Background(), collectionClock(head.Activation.At.Add(10*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { executionStop(t, w) })
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	select {
	case <-entered:
	case <-w.Done():
		t.Fatal("execution failed before crypto", w.Err())
	case <-time.After(10 * time.Second):
		t.Fatal("crypto was not reached")
	}
	w.BeginStop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = w.Wait(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || w.Ready() {
		t.Fatal("uncooperative crypto was abandoned", err)
	}
	select {
	case <-w.Done():
		t.Fatal("owner released dependencies while crypto was active")
	default:
	}
	if err := f.store.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString()); !errors.Is(err, persistence.ErrCollectionExecutionCoordinatorRegistered) {
		t.Fatal("live shutdown released exclusive ownership", err)
	}
	close(release)
	released = true
	executionStop(t, w)
	after, _, err := f.store.CollectionGet(head.ID)
	if err != nil || after.Execution == nil || after.Execution.Processed != 0 || after.Execution.Prepared != nil {
		t.Fatal("canceled crypto admitted a candidate", err)
	}
	if view, _ := f.store.CatalogSnapshot(); view.Len() != 0 {
		t.Fatal("shutdown crypto changed active catalog")
	}
}
