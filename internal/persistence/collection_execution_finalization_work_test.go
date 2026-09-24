package persistence

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCollectionExecutionFinalizationWorkEligibility(t *testing.T) {
	for _, stage := range []string{"admitted", "prepared", "child pending", "settled", "canceled before begin", "canceled prepared", "canceled child pending", "canceled settled", "finalized"} {
		t.Run(stage, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, begin := executionResultInput(t, s, "create")
			at := head.Activation.At.Add(time.Second)
			if stage == "prepared" || stage == "canceled prepared" {
				head = validationApplyAllowed(t, executeStoreCommand(t, s, begin, at))
				prepared := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 1, at.Add(time.Second))
				head = validationApplyAllowed(t, executeStoreCommand(t, s, prepared, prepared.Prepared.At))
			}
			if stage == "child pending" || stage == "settled" || stage == "canceled child pending" || stage == "canceled settled" || stage == "finalized" {
				var child *OperationReceipt
				head, child = executionResultDecide(t, s, head, begin, "accepted")
				if stage == "settled" || stage == "canceled settled" || stage == "finalized" {
					head = executionResultSettle(t, s, head, child, true)
				}
			}
			if head.Execution != nil {
				at = head.Execution.LastAt.Add(time.Second)
			}
			if stage == "canceled before begin" || stage == "canceled prepared" || stage == "canceled child pending" || stage == "canceled settled" {
				head, _ = activationSnapshotCancel(t, s, head, at)
			}
			if stage == "finalized" {
				head = validationApplyAllowed(t, executeStoreCommand(t, s, executionResultFinalizeCommand(t, head), at))
			}
			before := catalogDeltaJSON(t, s.fsm.image)
			// Metadata selection must not use ledger/history or operator credentials.
			probe := &Store{stop: make(chan struct{}), fsm: &machine{image: s.fsm.image}}
			work, err := probe.CollectionExecutionFinalizationWork(context.Background(), head.Activation.At.Add(-time.Second))
			eligible := stage == "settled" || stage == "canceled before begin" || stage == "canceled prepared" || stage == "canceled settled"
			if err != nil || len(work) != map[bool]int{true: 1, false: 0}[eligible] {
				t.Fatal("wrong finalization eligibility", work, err)
			}
			if eligible {
				wantBinding, _ := collectionExecutionBindingFor(head)
				if work[0].Binding != wantBinding || !reflect.DeepEqual(work[0].Fence, *CollectionExecutionFinalizeFenceFor(head)) || work[0].EarliestAt.Before(head.TerminalAt) || work[0].EarliestAt.Before(head.Activation.At) {
					t.Fatal("selector changed immutable finalization fence")
				}
				if work[0].Fence.Progress != nil {
					work[0].Fence.Progress.Processed++
					if work[0].Fence.Progress.Prepared != nil {
						work[0].Fence.Progress.Prepared.ID = "changed"
					}
				}
			}
			if !reflect.DeepEqual(before, catalogDeltaJSON(t, s.fsm.image)) {
				t.Fatal("selection or returned data changed original state")
			}
		})
	}
}

func TestCollectionExecutionFinalizationWorkOldEpochAndBounds(t *testing.T) {
	s := openCatalogMemory(t)
	head := executionWorkAdmit(t, s)
	at := head.Activation.At.Add(time.Second)
	head, _ = activationSnapshotCancel(t, s, head, at)
	probe := &Store{stop: make(chan struct{}), fsm: &machine{image: s.fsm.image}}
	probe.fsm.image.OperationEpoch = uuid.NewString()
	for _, phase := range []string{"canceled", "invalidated"} {
		stopped := head.Clone()
		if phase == "invalidated" {
			stopped.Phase, stopped.Cancellation, stopped.InvalidatedByRestore = phase, nil, uuid.NewString()
		}
		if err := stopped.validate(); err != nil {
			t.Fatal("invalid stopped fixture", err)
		}
		probe.fsm.image.Collections = map[string]CollectionState{head.ID: stopped}
		work, err := probe.CollectionExecutionFinalizationWork(context.Background(), at)
		if err != nil || len(work) != 1 || work[0].Fence.Phase != phase {
			t.Fatal("old-epoch known facts were discarded", phase, err)
		}
	}
	probe.fsm.image.Collections = make(map[string]CollectionState)
	for sequence := uint64(1); sequence <= maxCollectionOperations+1; sequence++ {
		copy := head.Clone()
		copy.ID = operationHandle(s.fsm.image.OperationEpoch, sequence)
		copy.Plan.Header.OperationID, copy.Validation.Header.OperationID = copy.ID, copy.ID
		probe.fsm.image.Collections[copy.ID] = copy
		if sequence == maxCollectionOperations {
			work, err := probe.CollectionExecutionFinalizationWork(context.Background(), at)
			if err != nil || len(work) != maxCollectionOperations {
				t.Fatal("maximum bounded work set rejected", len(work), err)
			}
		}
	}
	if _, err := probe.CollectionExecutionFinalizationWork(context.Background(), at); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("oversized retained-header set accepted", err)
	}
}

func TestCollectionExecutionFinalizationWorkCannotResumeOldApplyingParent(t *testing.T) {
	s := openCatalogMemory(t)
	head, begin := executionResultInput(t, s, "create")
	head, child := executionResultDecide(t, s, head, begin, "accepted")
	head = executionResultSettle(t, s, head, child, true)
	probe := &Store{stop: make(chan struct{}), fsm: &machine{image: s.fsm.image}}
	if work, err := probe.CollectionExecutionFinalizationWork(context.Background(), head.Execution.LastAt); err != nil || len(work) != 1 {
		t.Fatal("current settled parent missing", work, err)
	}
	probe.fsm.image.OperationEpoch = uuid.NewString()
	if work, err := probe.CollectionExecutionFinalizationWork(context.Background(), head.Execution.LastAt); err != nil || len(work) != 0 {
		t.Fatal("old applying parent obtained a finalization grant", work, err)
	}
}

func TestCollectionExecutionFinalizationWorkLockCancellation(t *testing.T) {
	s := openCatalogMemory(t)
	for _, storeLock := range []bool{true, false} {
		var release func()
		if storeLock {
			s.mu.Lock()
			release = s.mu.Unlock
		} else {
			s.fsm.mu.Lock()
			release = s.fsm.mu.Unlock
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		finished := make(chan error, 1)
		go func() { _, err := s.CollectionExecutionFinalizationWork(ctx, time.Now()); finished <- err }()
		select {
		case err := <-finished:
			release()
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			release()
			cancel()
			<-finished
			t.Fatal("selector ignored lock deadline")
		}
	}
}
