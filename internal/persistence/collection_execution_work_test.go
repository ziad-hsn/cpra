package persistence

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func executionWorkAdmit(t *testing.T, s *Store) CollectionState {
	t.Helper()
	head, _ := activationSnapshotAdmit(t, s, activationSnapshotInput(t, s, true))
	return head
}

func executionWorkPrepare(t *testing.T, s *Store) (CollectionState, CollectionExecuteCommand) {
	t.Helper()
	head, begin := executeStoreBegin(t, s)
	candidate := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 1, head.Activation.At.Add(2*time.Second))
	result := executeStoreCommand(t, s, candidate, candidate.Prepared.At)
	if result.Err != nil || !result.Allowed {
		t.Fatal("prepare original fixture", result.Err)
	}
	return *result.Collection, candidate
}

func executionWorkMap(work []CollectionExecutionWork) map[string]CollectionExecutionWork {
	byID := make(map[string]CollectionExecutionWork, len(work))
	for _, item := range work {
		byID[item.OperationID] = item
	}
	return byID
}

func TestCollectionExecutionWorkDetachedProgressAndNoIO(t *testing.T) {
	s := openCatalogMemory(t)
	admitted := executionWorkAdmit(t, s)
	prepared, candidate := executionWorkPrepare(t, s)
	completed, begin := executeStoreBegin(t, s)
	var first *OperationReceipt
	for ordinal := uint64(1); ordinal <= completed.ItemCount; ordinal++ {
		at := completed.Execution.LastAt.Add(time.Second)
		c := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, ordinal, at)
		if r := executeStoreCommand(t, s, c, at); r.Err != nil {
			t.Fatal(r.Err)
		}
		r := executeStoreCommand(t, s, executeDecision(c), at.Add(time.Second))
		if r.Err != nil || r.Operation == nil {
			t.Fatal("original decision", r.Err)
		}
		completed = *r.Collection
		if first == nil {
			first = r.Operation
		}
	}
	update := OperationUpdate{ID: first.ID, Key: first.Key, UID: first.UID, Revision: first.NewVersion, Applied: true}
	r := submit(t, s, Command{Kind: "operation", Operation: &update, At: completed.Execution.LastAt.Add(time.Second)})[0]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	completed, _, _ = s.CollectionGet(completed.ID)
	canceled := executionWorkAdmit(t, s)
	at := canceled.Activation.At.Add(time.Second) // The admission fixture already observes ten minutes in the future.
	canceled = validationApplyAllowed(t, collectionCommand(t, s, activationCancel(canceled, *canceled.Owner, at), at))
	_ = validationHistoryCrashInput(t, s, 1) // Ordinary unactivated input is not execution work.
	before := catalogDeltaJSON(t, s.fsm.image)
	work, err := s.CollectionExecutionWork(context.Background(), at.Add(31*24*time.Hour))
	if err != nil || len(work) != 3 {
		t.Fatal("admitted work selection", len(work), err)
	}
	byID := executionWorkMap(work)
	a, p, c := byID[admitted.ID], byID[prepared.ID], byID[completed.ID]
	if a.OperationID == "" || a.Begun || a.Processed != 0 || a.NextOrdinal != 1 || a.Prepared != nil ||
		p.Prepared == nil || p.Prepared.ID != candidate.Prepared.ID || p.Prepared.RowDigest != candidate.Prepared.RowDigest ||
		p.Prepared.Ordinal != 1 || p.Prepared.InputOrdinal != candidate.Prepared.InputOrdinal || !p.Begun || p.NextOrdinal != 1 ||
		c.Processed != completed.ItemCount || c.NextOrdinal != 0 || c.Accepted != completed.ItemCount || c.ChildTerminals != 1 || c.Prepared != nil {
		t.Fatal("original progress/candidate observation changed")
	}
	for _, item := range work {
		head, _, _ := s.CollectionGet(item.OperationID)
		binding, _ := collectionExecutionBindingFor(head)
		if item.Binding != binding || item.Actor != head.Actor || item.CapabilitiesDigest != head.Activation.CapabilitiesDigest ||
			item.ItemCount != head.ItemCount || !item.ActivationAt.Equal(head.Activation.At) {
			t.Fatal("work lost original admitted identity")
		}
	}
	encoded, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"Secret", "Payload", "Ciphertext", "WrappedKey", authenticationToken,
		base64.StdEncoding.EncodeToString(prepared.Secret.Ciphertext), base64.StdEncoding.EncodeToString(candidate.Prepared.Record.Payload.Ciphertext)} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("selector retained protected material")
		}
	}
	p.Prepared.ID, p.Prepared.RowDigest = "changed", "changed"
	work[0].Binding.PlanID, work[0].Actor = "changed", "changed"
	if !bytes.Equal(before, catalogDeltaJSON(t, s.fsm.image)) {
		t.Fatal("selection or returned metadata mutation changed durable state")
	}

	// A protected metadata-only owner has no history, ledger or decryption
	// facility. Selection must not require any of those handles.
	probe := &Store{stop: make(chan struct{}), fsm: &machine{image: image{Version: CollectionExecutionFormatVersion,
		OperationEpoch: s.fsm.image.OperationEpoch, OperationHighWater: s.fsm.image.OperationHighWater,
		Collections: map[string]CollectionState{prepared.ID: prepared.Clone()}}}}
	if got, err := probe.CollectionExecutionWork(context.Background(), at); err != nil || len(got) != 1 || got[0].Prepared.ID != candidate.Prepared.ID {
		t.Fatal("metadata observation requires ledger or history I/O", err)
	}
	if err := probe.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString()); err != nil {
		t.Fatal("registration requires ledger or history I/O", err)
	}
}

func TestCollectionExecutionWorkBoundOrderEpochAndClock(t *testing.T) {
	s := openCatalogMemory(t)
	original := executionWorkAdmit(t, s)
	probe := &Store{stop: make(chan struct{}), fsm: &machine{image: image{Version: CollectionActivationFormatVersion,
		OperationEpoch: s.fsm.image.OperationEpoch, OperationHighWater: maxCollectionOperations + 1,
		Collections: make(map[string]CollectionState)}}}
	// Header-only synthetic identities qualify the 64-entry observation bound;
	// they are never installed in the live Store or submitted for execution.
	for sequence := uint64(1); sequence <= maxCollectionOperations; sequence++ {
		head := original.Clone()
		head.ID = operationHandle(probe.fsm.image.OperationEpoch, sequence)
		head.Plan.Header.OperationID, head.Validation.Header.OperationID = head.ID, head.ID
		if sequence != maxCollectionOperations {
			head.Activation.At = head.Activation.At.Add(time.Second)
		}
		if head.validate() != nil {
			t.Fatal("invalid bounded metadata fixture")
		}
		probe.fsm.image.Collections[head.ID] = head
	}
	work, err := probe.CollectionExecutionWork(context.Background(), original.Activation.At.Add(-time.Second))
	if err != nil || len(work) != maxCollectionOperations || work[0].OperationID != operationHandle(probe.fsm.image.OperationEpoch, maxCollectionOperations) {
		t.Fatal("future parents or activation order lost", len(work), err)
	}
	for i := 1; i < len(work); i++ {
		if work[i].OperationID != operationHandle(probe.fsm.image.OperationEpoch, uint64(i)) || !work[i].LastAt.After(original.Activation.At) {
			t.Fatal("numeric sequence order or original future clock lost")
		}
	}
	late, err := probe.CollectionExecutionWork(context.Background(), original.ExpiresAt.Add(31*24*time.Hour))
	if err != nil || !reflect.DeepEqual(work, late) {
		t.Fatal("former upload expiry changed execution work", err)
	}
	probe.fsm.image.OperationEpoch = uuid.NewString()
	if got, err := probe.CollectionExecutionWork(context.Background(), original.Activation.At); err != nil || len(got) != 0 {
		t.Fatal("old-epoch admission became work", err)
	}
	probe.fsm.image.Collections["oversized-map"] = CollectionState{}
	if got, err := probe.CollectionExecutionWork(context.Background(), original.Activation.At); !errors.Is(err, ErrCollectionUnavailable) || got != nil {
		t.Fatal("oversized map returned partial work", err)
	}
}

func TestCollectionExecutionWorkRejectsCorruptionAndUnavailable(t *testing.T) {
	fixture := openCatalogMemory(t)
	head, _ := executionWorkPrepare(t, fixture)
	for _, mode := range []string{"id", "activation", "progress", "candidate", "high-water", "format", "administrative", "opening", "store-error", "fsm-error", "bootstrap", "restore", "reset", "nil-fsm", "closed"} {
		t.Run(mode, func(t *testing.T) {
			probe := &Store{stop: make(chan struct{}), fsm: &machine{image: image{Version: CollectionExecutionFormatVersion,
				OperationEpoch: fixture.fsm.image.OperationEpoch, OperationHighWater: fixture.fsm.image.OperationHighWater,
				Collections: map[string]CollectionState{head.ID: head.Clone()}}}}
			bad := probe.fsm.image.Collections[head.ID]
			switch mode {
			case "id":
				bad.ID = operationHandle(probe.fsm.image.OperationEpoch, 900)
			case "activation":
				bad.Activation = nil
			case "progress":
				bad.Execution.Processed = bad.ItemCount + 1
			case "candidate":
				bad.Execution.Prepared.RowDigest = "corrupt"
			case "high-water":
				probe.fsm.image.OperationHighWater = 0
			case "format":
				probe.fsm.image.Version = CollectionActivationFormatVersion
			case "administrative":
				probe.administrative = true
			case "opening":
				probe.opening = true
			case "store-error":
				probe.err = errors.New("private store diagnosis")
			case "fsm-error":
				probe.fsm.err = errors.New("private FSM diagnosis")
			case "bootstrap":
				probe.fsm.image.Bootstrap = &BootstrapState{Phase: "begin"}
			case "restore":
				probe.fsm.image.Restore = &RestoreState{Phase: "begin"}
			case "reset":
				probe.fsm.image.Authentication = &AuthenticationState{ResetRequired: true}
			case "closed":
				close(probe.stop)
			}
			probe.fsm.image.Collections[head.ID] = bad
			if mode == "nil-fsm" {
				probe.fsm = nil
			}
			if got, err := probe.CollectionExecutionWork(context.Background(), head.Execution.LastAt); !errors.Is(err, ErrCollectionUnavailable) || got != nil {
				t.Fatal("corruption/unavailability returned work", err)
			}
		})
	}
	for _, at := range []time.Time{{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := fixture.CollectionExecutionWork(context.Background(), at); !errors.Is(err, ErrCollectionInvalid) {
			t.Fatal("invalid observation time accepted", err)
		}
	}
	if _, err := fixture.CollectionExecutionWork(nil, time.Now()); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("nil context accepted", err)
	}
}

func TestCollectionExecutionCoordinatorRegistrationAndCanceledLocks(t *testing.T) {
	s := openCatalogMemory(t)
	for _, runID := range []string{"", "not-a-uuid"} {
		if err := s.RegisterCollectionExecutionCoordinator(context.Background(), runID); !errors.Is(err, ErrCollectionInvalid) || s.executionRunID != "" {
			t.Fatal("invalid registration consumed lifetime slot", err)
		}
	}
	if err := s.RegisterCollectionExecutionCoordinator(nil, uuid.NewString()); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("nil registration context accepted", err)
	}
	before := s.Status().CommittedIndex
	var wg sync.WaitGroup
	results := make(chan error, 24)
	for range cap(results) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString())
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrCollectionExecutionCoordinatorRegistered) {
			t.Fatal(err)
		}
	}
	if won != 1 || s.Status().CommittedIndex != before {
		t.Fatal("registration was not one-shot and local", won)
	}
	if err := s.RegisterCollectionExecutionCoordinator(context.Background(), s.executionRunID); !errors.Is(err, ErrCollectionExecutionCoordinatorRegistered) {
		t.Fatal("same identity reacquired coordinator slot", err)
	}
	if err := s.RegisterCollectionValidationCoordinator(context.Background(), uuid.NewString()); err != nil {
		t.Fatal("independent validation slot was consumed", err)
	}
	for _, operation := range []string{"select", "register"} {
		for _, owner := range []string{"store", "fsm"} {
			t.Run(operation+"/"+owner, func(t *testing.T) {
				probe := openCatalogMemory(t)
				base, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := &authenticationWaitingContext{Context: base, waiting: make(chan struct{})}
				lock := &probe.mu
				if owner == "fsm" {
					lock = &probe.fsm.mu
				}
				lock.Lock()
				defer lock.Unlock()
				done := make(chan error, 1)
				go func() {
					if operation == "register" {
						done <- probe.RegisterCollectionExecutionCoordinator(ctx, uuid.NewString())
						return
					}
					_, err := probe.CollectionExecutionWork(ctx, time.Now().UTC())
					done <- err
				}()
				select {
				case <-ctx.waiting:
				case <-time.After(time.Second):
					t.Fatal("did not enter ownership wait")
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("cancellation did not end ownership wait")
				}
				if probe.executionRunID != "" {
					t.Fatal("canceled registration consumed slot")
				}
			})
		}
	}
}

func TestCollectionExecutionWorkExplicitRestoreCannotResume(t *testing.T) {
	config := testConfig(t)
	admin := openAuthenticationAdmin(t, config)
	if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	head := executionWorkAdmit(t, s)
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	at := head.Activation.At.Add(time.Second)
	if err := MarkRestored(config.Storage.Directory, at); err != nil {
		t.Fatal(err)
	}
	admin, err = OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	state, err := admin.Authentication()
	if err != nil || !state.ResetRequired {
		t.Fatal("restore did not invalidate authority", err)
	}
	provision := authenticationBootstrap()
	provision.Mode, provision.Epoch, provision.ExpectedEpoch, provision.ExpectedRevision = "provision", state.Epoch, state.Epoch, state.Revision
	provision.At = at.Add(time.Second)
	provision.Principals[0].ExpiresAt = provision.At.Add(time.Hour)
	if _, err := admin.CommitAuthentication(context.Background(), provision); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString()); err != nil {
		t.Fatal("fresh restored owner could not register", err)
	}
	if got, err := s.CollectionExecutionWork(context.Background(), provision.At.Add(time.Second)); err != nil || len(got) != 0 {
		t.Fatal("restored old admission became executable work", err)
	}
	s.fsm.mu.RLock()
	retained := s.fsm.image.Collections[head.ID].Clone()
	s.fsm.mu.RUnlock()
	if retained.Phase != "invalidated" || retained.InvalidatedByRestore == "" || !collectionActivationsEqual(retained.Activation, head.Activation) {
		t.Fatal("restored historical admission changed or was not invalidated")
	}
}

func TestCollectionExecutionWorkRealRaftReopen(t *testing.T) {
	config := testConfig(t)
	admin := openAuthenticationAdmin(t, config)
	if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	if err := admin.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString()); !errors.Is(err, ErrCollectionUnavailable) || admin.executionRunID != "" {
		t.Fatal("administrative owner registered execution", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	head, _ := executionWorkPrepare(t, s)
	index, tx := s.Status().CommittedIndex, ledgerTestTransactionID(t, s.fsm.collections)
	work, err := s.CollectionExecutionWork(context.Background(), head.ExpiresAt.Add(time.Hour))
	if err != nil || len(work) != 1 || work[0].Prepared == nil {
		t.Fatal("Raft work", err)
	}
	if err := s.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if s.Status().CommittedIndex != index || ledgerTestTransactionID(t, s.fsm.collections) != tx {
		t.Fatal("selection or registration wrote storage")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.CollectionExecutionWork(context.Background(), head.ExpiresAt.Add(time.Hour)); err != nil || !reflect.DeepEqual(got, work) {
		t.Fatal("reopen changed original prepared work", err)
	}
	if err := reopened.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString()); err != nil {
		t.Fatal("reopen inherited registration", err)
	}
	if err := reopened.raft.Shutdown().Error(); err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.CollectionExecutionWork(context.Background(), time.Now().UTC()); !errors.Is(err, ErrCollectionUnavailable) || got != nil {
		t.Fatal("non-leader selected execution", err)
	}
	if err := reopened.RegisterCollectionExecutionCoordinator(context.Background(), uuid.NewString()); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("duplicate registration bypassed health", err)
	}
}
