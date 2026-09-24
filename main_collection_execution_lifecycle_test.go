package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/encryptionsetup"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

type mainRuntimeCollectionOwner struct {
	done    chan struct{}
	failure error
	ready   atomic.Bool
	stopped atomic.Bool
	wait    func(context.Context) error
}

func (o *mainRuntimeCollectionOwner) Done() <-chan struct{} { return o.done }
func (o *mainRuntimeCollectionOwner) Err() error            { return o.failure }
func (o *mainRuntimeCollectionOwner) Ready() bool           { return o.ready.Load() }
func (o *mainRuntimeCollectionOwner) BeginStop()            { o.stopped.Store(true); o.ready.Store(false) }
func (o *mainRuntimeCollectionOwner) Wait(ctx context.Context) error {
	if o.wait != nil {
		return o.wait(ctx)
	}
	select {
	case <-o.done:
		return o.failure
	case <-ctx.Done():
		return ctx.Err()
	}
}
func mainRuntimeOwner() *mainRuntimeCollectionOwner {
	o := &mainRuntimeCollectionOwner{done: make(chan struct{})}
	o.ready.Store(true)
	return o
}

func TestMainCollectionOwnersStopAllBeforeSharedJoin(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "joined-failure", true: "deadline-retains-dependencies"}[held], func(t *testing.T) {
			validation, execution, reselection := mainRuntimeOwner(), mainRuntimeOwner(), mainRuntimeOwner()
			failure := errors.New("execution failed after joining")
			execution.failure = failure
			close(execution.done)
			seen := 0
			validation.wait = func(ctx context.Context) error {
				if !validation.stopped.Load() || !execution.stopped.Load() || !reselection.stopped.Load() {
					t.Error("join began before all owners stopped admission")
				}
				if held {
					<-ctx.Done()
					return ctx.Err()
				}
				close(validation.done)
				return nil
			}
			execution.wait = func(context.Context) error { seen++; return failure }
			close(reselection.done)
			reselection.wait = func(context.Context) error { seen++; return nil }
			owners := runtimeCollectionOwners{validation: validation, execution: execution, reselection: reselection}
			owners.BeginStop()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			joined, err := owners.Join(ctx)
			if joined == held || !errors.Is(err, failure) || seen != 2 || owners.Ready() {
				t.Fatal("lost owner failure/join boundary", joined, err, seen)
			}
			if held && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("shared deadline lost", err)
			}
		})
	}
}
func TestMainCollectionOwnersReadinessAndSupervision(t *testing.T) {
	for _, failed := range []string{"validation", "execution", "reselection"} {
		t.Run(failed, func(t *testing.T) {
			validation, execution, reselection := mainRuntimeOwner(), mainRuntimeOwner(), mainRuntimeOwner()
			owners := runtimeCollectionOwners{validation: validation, execution: execution, reselection: reselection}
			if !owners.Ready() || owners.Failure() != nil {
				t.Fatal("healthy owners unavailable")
			}
			o := validation
			if failed == "execution" {
				o = execution
			}
			if failed == "reselection" {
				o = reselection
			}
			failure := errors.New("original owner failure")
			o.failure = failure
			o.ready.Store(false)
			close(o.done)
			if owners.Ready() || !errors.Is(owners.Failure(), failure) {
				t.Fatal("failed owner remained ready")
			}
			if err := owners.Supervise(t.Context()); !errors.Is(err, failure) || !strings.Contains(err.Error(), failed) {
				t.Fatal("supervisor lost original owner failure", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := owners.Supervise(ctx); err != nil {
				t.Fatal("deliberate process stop became failure", err)
			}
		})
	}
}

// Public activation remains absent. This stopped fixture uses the actual
// authenticated validation coordinator and the private durable admission only.
func seedMainCollectionExecution(t *testing.T, f mainManagementFixture) persistence.CollectionState {
	t.Helper()
	original := seedMainCollectionValidation(t, f, false)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	encryption, err := encryptionsetup.Open(ctx, encryptionsetup.Options{StorageMode: f.settings.Storage.Mode, DataDirectory: f.settings.Storage.Directory, Encryption: f.settings.Management.Encryption})
	if err != nil {
		t.Fatal(err)
	}
	defer encryption.Close()
	store, err := persistence.Open(ctx, f.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	startup, err := management.StartupCatalog(ctx, store, encryption.Sealer(), managementStartupOptions(f.settings, f.options))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := startup.Catalog.StartCollectionValidationCoordinator(ctx, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { owner.BeginStop(); _ = owner.Wait(context.Background()) }()
	var head persistence.CollectionState
	for {
		var exists bool
		head, exists, err = store.CollectionGet(original.ID)
		if err != nil || !exists {
			t.Fatal("original input disappeared", err)
		}
		if head.Validation != nil && head.Validation.HistorySealed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("original validation did not publish", ctx.Err())
		case <-owner.Done():
			t.Fatal("validation owner failed", owner.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	owner.BeginStop()
	if err = owner.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if head.Phase != "validated" || !head.Validation.Header.Valid {
		t.Fatal("fixture failed original validation")
	}
	at := time.Now().UTC()
	authority, err := store.ObserveOperatorAuthority(ctx, head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	activation := persistence.CollectionActivation{ID: uuid.NewString(), Authority: authority, InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount,
		ResultID: head.Validation.Header.ResultID, ResultDescriptor: head.Validation.Descriptor, PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor,
		CapabilitiesDigest: head.Validation.Header.CapabilitiesDigest, ValidationRequest: persistence.CollectionValidationRequestFenceFor(head), At: at}
	result, err := store.Submit(ctx, []persistence.Command{{Kind: "collection", At: at, Collection: &persistence.CollectionCommand{Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID, Activation: &activation, ActivationAuthority: &authority}}})
	if err != nil || len(result) != 1 || result[0].Err != nil || result[0].Collection == nil {
		t.Fatal("private original activation failed", err)
	}
	head = result[0].Collection.Clone()
	if head.Execution != nil {
		t.Fatal("stopped fixture executed an item")
	}
	if err = store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	return head
}

func waitMainCollectionFinalization(t *testing.T, client *cpra.Client, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	for {
		result, err := client.Operations.Get(ctx, id)
		if err != nil || result.Data.ID != id {
			t.Fatal("read original collection finalization", err)
		}
		switch result.Data.State {
		case "completed":
			return
		case "partial", "failed", "canceled", "invalidated":
			t.Fatal("collection did not complete its original child", result.Data.State)
		}
		select {
		case <-ctx.Done():
			t.Fatal("collection finalization did not complete", result.Data.State, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func TestMainCollectionExecutionRecoversAndControllerCompletesOriginalChild(t *testing.T) {
	fixture := newMainManagementFixture(t)
	original := seedMainCollectionExecution(t, fixture)
	client, stop := startMainManagement(t, fixture)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	childID := ""
	for childID == "" {
		page, err := client.Operations.List(ctx, cpra.ListOptions{Limit: 100})
		if err != nil {
			t.Fatal("read actual controller child", err)
		}
		for _, op := range page.Data.Items {
			if op.ID != original.ID && len(op.Items) == 1 && op.Items[0].ID == "staged-secret" {
				childID = op.ID
			}
		}
		if childID != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("execution did not create original child", ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
	waitMainOperation(t, client, childID)
	// Ordinary child receipts expose an applied count. Parent public counters
	// are a separate projection; verify their authoritative summary after stop.
	waitMainCollectionFinalization(t, client, original.ID)
	if _, err := client.Ready(ctx); err != nil {
		t.Fatal("live controller/coordinators not ready", err)
	}
	credential, err := client.Credentials.Get(ctx, "staged-secret")
	if err != nil || credential.Data.Metadata.UID == "" || credential.Data.Spec.Value != nil {
		t.Fatal("credential not stored or secret disclosed", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+fixture.options.webAddr+"/api/v2/operations/"+original.ID+"/activate", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+startupOperatorToken)
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	// Public activation exists, but a request without its required JSON media
	// type must be rejected before touching the already finalized execution.
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("activation accepted a request without its JSON content type", response.StatusCode)
	}
	stop()
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal("shutdown retained one coordinator/store owner", err)
	}
	head, exists, err := store.CollectionGet(original.ID)
	if err != nil || !exists || head.Phase != "completed" || head.ExecutionResult == nil || head.ExecutionResult.Summary.Outcome != "completed" || head.Execution == nil || head.Execution.Processed != 1 || head.Execution.Accepted != 1 || head.Execution.ChildTerminals != 1 {
		_ = store.Close()
		t.Fatal("original child completion/progress did not survive shutdown", err)
	}
	summary := head.ExecutionResult.Summary
	if summary.Binding.OperationID != original.ID || summary.Binding.ActivationID != original.Activation.ID ||
		!summary.ActivationAt.Equal(original.Activation.At) || summary.Unattempted != 0 || summary.Fence.Progress == nil ||
		summary.Fence.Progress.Accepted != 1 || summary.Fence.Progress.ChildApplied != 1 ||
		summary.FinalizedAt.Before(head.Execution.LastAt) {
		_ = store.Close()
		t.Fatal("parent summary lost the original accepted and applied child")
	}
	child, err := store.Operation(childID)
	if err != nil || child.State != "completed" || child.Outcome != "applied" {
		_ = store.Close()
		t.Fatal("controller completion absent", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	client, stop = startMainManagement(t, fixture)
	waitMainOperation(t, client, childID)
	waitMainCollectionFinalization(t, client, original.ID)
	stop()
	store, err = persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, exists, err := store.CollectionGet(original.ID)
	if err != nil || !exists || after.ExecutionResult == nil || !reflect.DeepEqual(after.Execution, head.Execution) || !reflect.DeepEqual(after.ExecutionResult.Summary, head.ExecutionResult.Summary) {
		t.Fatal("restart changed original execution or finalized summary", err)
	}
	page, err := store.History().Page("collection-execution/"+original.ID, "", 10)
	anchors := 0
	for _, event := range page.Events {
		if event.CollectionExecution != nil {
			anchors++
		}
	}
	if err != nil || anchors != 1 {
		t.Fatal("restart duplicated immutable finalization anchor", anchors, err)
	}
}

func TestMainCollectionExecutionJoinsBothOnListenerFailure(t *testing.T) {
	fixture := newMainManagementFixture(t)
	seedMainCollectionExecution(t, fixture)
	listener, err := net.Listen("tcp", fixture.options.webAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	err = runCPRa(ctx, fixture.options, func() { t.Error("failed listener announced ready") })
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatal("listener failure lost", err)
	}
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal("early error retained coordinator/store ownership", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
}
