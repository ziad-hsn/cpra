package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Admission, three accepted decisions, out-of-order child completions, an
// abandoned preparation, cancellation, finalization and publication are actual
// Store transitions. The recovery fixture selects persisted prefix boundaries
// independently of the deletion primitive's batch size.
func executionRetirementRecoverySource(t *testing.T) executionRecoveryFixture {
	t.Helper()
	s, head, begin := preparationCacheFixture(t, false, 4)
	var children []*OperationReceipt
	for range 3 {
		var outcome CollectionItemOutcome
		head, outcome = preparationCacheAccept(t, s, head, begin)
		children = append(children, outcome.Receipt)
	}
	prepared := executeCandidate(t, begin, s.fsm.collectionExecutionIndex, 4, head.Execution.LastAt.Add(time.Second))
	head = validationApplyAllowed(t, executeStoreCommand(t, s, prepared, prepared.Prepared.At))
	for _, n := range []int{2, 0, 1} {
		head = executionResultSettle(t, s, head, children[n], n != 1)
	}
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
	return executionRetirementRecoveryCapture(t, s, head)
}

func executionRetirementRecoveryCapture(t *testing.T, s *Store, head CollectionState) executionRecoveryFixture {
	t.Helper()
	s.fsm.mu.RLock()
	i := catalogDeltaCloneMachine(t, s.fsm).image
	s.fsm.mu.RUnlock()
	var records []collectionExecutionRecord
	view, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	err = view.WalkExecution(context.Background(), func(r collectionExecutionRecord) error {
		records = append(records, r)
		return nil
	})
	if closeErr := view.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	return executionRecoveryFixture{image: i, head: head, ledger: s.fsm.collections, records: records}
}

func TestCollectionExecutionRetiredRecoveryMixedDecisionsAndStoppedSuffix(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		s, head := executionItemReordered(t, false, stopped)
		head = executionResultViewPublish(t, s, head)
		original := executionRetirementRecoveryCapture(t, s, head)
		for _, disk := range []bool{false, true} {
			for prefix := uint64(0); prefix <= head.Execution.Processed; prefix++ {
				t.Run(fmt.Sprintf("stopped=%t/disk=%t/prefix=%d", stopped, disk, prefix), func(t *testing.T) {
					f := executionRetirementRecoveryPrefix(t, original, disk, prefix, false)
					got, err := executionRecoveryCheck(t, f)
					if err != nil || len(got.children) != 0 || len(got.trees) != 0 || len(got.commitments) != 0 {
						t.Fatal("nonaccepted decisions or stopped suffix changed original execution", err)
					}
				})
			}
		}
	}
}

func executionRetirementRecoveryPrefix(t *testing.T, original executionRecoveryFixture, disk bool, prefix uint64, preparedRemoved bool) executionRecoveryFixture {
	t.Helper()
	data, plan, validation := validationLedgerStreams(t, original.ledger)
	ledger := newLedgerTest(t, disk, 32<<20)
	for _, err := range []error{importCollectionLedger(bytes.NewReader(data), ledger), importCollectionPlanLedger(bytes.NewReader(plan), ledger), importCollectionValidationLedger(bytes.NewReader(validation), ledger)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	head := original.head.Clone()
	checkpoint, err := NewCollectionExecutionRetirementCheckpoint(head.Execution.Binding, head.ItemCount, head.Execution.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[uint64]*CollectionItemOutcome{}
	terminals := map[uint64]*CollectionChildObservation{}
	var prepared *CollectionPreparedItem
	for _, r := range original.records {
		if r.Outcome != nil {
			outcomes[r.Outcome.Ordinal] = r.Outcome
		}
		if r.Terminal != nil {
			terminals[r.Terminal.Ordinal] = r.Terminal
		}
		if r.Prepared != nil {
			prepared = r.Prepared
		}
	}
	for ordinal := uint64(1); ordinal <= prefix; ordinal++ {
		checkpoint, err = checkpoint.append(*outcomes[ordinal], terminals[ordinal])
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows []collectionExecutionRecord
	for ordinal := prefix + 1; ordinal <= head.Execution.Processed; ordinal++ {
		rows = append(rows, collectionExecutionRecord{Version: 1, Outcome: outcomes[ordinal]})
	}
	if prepared != nil && !preparedRemoved {
		rows = append(rows, collectionExecutionRecord{Version: 1, Prepared: prepared})
	}
	for ordinal := prefix + 1; ordinal <= head.Execution.Processed; ordinal++ {
		if terminals[ordinal] != nil {
			rows = append(rows, collectionExecutionRecord{Version: 1, Terminal: terminals[ordinal]})
		}
	}
	if len(rows) != 0 {
		if err := ledger.importRetiredExecutionBatch(rows, map[string]uint64{head.ID: prefix}); err != nil {
			t.Fatal(err)
		}
	}
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Minute)
	head.ExecutionRetirement = &CollectionExecutionRetirementState{Version: 1, Result: head.ExecutionResult.Clone(), Checkpoint: &checkpoint,
		PreparedRemoved: preparedRemoved, StartedAt: at, UpdatedAt: at}
	i := catalogDeltaCloneMachine(t, &machine{image: original.image}).image
	i.Version = CollectionExecutionRetirementFormatVersion
	i.Collections[head.ID] = head
	return executionRecoveryFixture{image: i, head: head, ledger: ledger, records: rows}
}

func TestCollectionExecutionRetiredRecoveryReconstructsEveryPrefix(t *testing.T) {
	original := executionRetirementRecoverySource(t)
	for _, disk := range []bool{false, true} {
		for prefix := uint64(0); prefix <= original.head.Execution.Processed; prefix++ {
			for _, removePrepared := range []bool{false, true} {
				if removePrepared && prefix != original.head.Execution.Processed {
					continue
				}
				t.Run(fmt.Sprintf("disk=%t/prefix=%d/preparedRemoved=%t", disk, prefix, removePrepared), func(t *testing.T) {
					f := executionRetirementRecoveryPrefix(t, original, disk, prefix, removePrepared)
					before, _ := f.ledger.Bytes()
					view, err := f.ledger.Freeze()
					if err != nil {
						t.Fatal(err)
					}
					if err := validateCollectionRows(f.image, view); err != nil {
						t.Fatal("outer snapshot validation", err)
					}
					got, err := validateCollectionExecutionInventory(context.Background(), f.image, view)
					if err != nil || len(got.children) != 0 || len(got.trees) != 0 || len(got.commitments) != 0 {
						t.Fatal("retired inventory installed executable ownership/cache", err)
					}
					if _, err := validateCollectionExecutionParent(context.Background(), f.image, f.head, view, nil); !errors.Is(err, ErrCollectionInvalid) {
						t.Fatal("retired parent entered full executable recovery", err)
					}
					if err := view.Close(); err != nil {
						t.Fatal(err)
					}
					after, _ := f.ledger.Bytes()
					remaining, err := collectionExecutionRemainingCharge(f.head)
					base := f.head.EncodedBytes + f.head.Plan.EncodedBytes + f.head.Validation.EncodedBytes
					if err != nil || before != after || after != base+remaining || !reflect.DeepEqual(f.head.Execution, original.head.Execution) {
						t.Fatal("recovery changed original progress or remaining quota", before, after, base, remaining, err)
					}
				})
			}
		}
	}
}

func TestCollectionExecutionRetiredRecoveryRejectsCorruption(t *testing.T) {
	original := executionRetirementRecoverySource(t)
	for _, disk := range []bool{false, true} {
		for _, name := range []string{"missing-outcome", "missing-terminal", "missing-order", "retired-outcome", "retired-terminal", "extra-row", "checkpoint-digest", "publication", "pending-child", "reserved-child", "validation", "source", "prepared"} {
			if disk && name == "missing-order" {
				continue // The native bucket itself provides the physical key order.
			}
			t.Run(fmt.Sprintf("disk=%t/%s", disk, name), func(t *testing.T) {
				f := executionRetirementRecoveryPrefix(t, original, disk, 1, false)
				var suffix *CollectionItemOutcome
				for _, r := range f.records {
					if r.Outcome != nil && r.Outcome.Ordinal == 2 {
						suffix = r.Outcome
					}
				}
				switch name {
				case "missing-order":
					delete(f.ledger.executionOrder, f.head.ID)
				case "missing-outcome", "missing-terminal":
					slot := collectionExecutionOutcomeSlot(2)
					if name == "missing-terminal" {
						slot = collectionExecutionTerminalSlot(2)
					}
					executionRetirementReplace(t, f.ledger, f.head.ID, slot, nil)
				case "retired-outcome", "retired-terminal":
					for _, r := range original.records {
						if name == "retired-outcome" && r.Outcome != nil && r.Outcome.Ordinal == 1 || name == "retired-terminal" && r.Terminal != nil && r.Terminal.Ordinal == 1 {
							raw, _ := collectionExecutionEncoding(r)
							slot := collectionExecutionOutcomeSlot(1)
							if r.Terminal != nil {
								slot = collectionExecutionTerminalSlot(1)
							}
							executionRetirementReplace(t, f.ledger, f.head.ID, slot, raw)
						}
					}
				case "extra-row":
					raw, _ := collectionExecutionEncoding(f.records[0])
					executionRetirementReplace(t, f.ledger, f.head.ID, "unindexed-extra", raw)
				case "checkpoint-digest":
					f.head.ExecutionRetirement.Checkpoint.Progress.OutcomeDigest = strings.Repeat("b", 64)
				case "publication":
					f.head.ExecutionRetirement.Result.PublishedBytes++
				case "pending-child":
					if f.image.Operations == nil {
						f.image.Operations = make(map[string]OperationReceipt)
					}
					f.image.Operations[suffix.Receipt.ID] = *suffix.Receipt
				case "reserved-child":
					if f.image.OperationReservations == nil {
						f.image.OperationReservations = make(map[string]OperationReservation)
					}
					f.image.OperationReservations[suffix.Receipt.ID] = OperationReservation{}
				case "validation", "source":
					if disk {
						err := f.ledger.db.Update(func(tx *bolt.Tx) error {
							root := collectionLedgerValidation
							if name == "source" {
								root = collectionLedgerRecords
							}
							return tx.Bucket(root).Bucket([]byte(f.head.ID)).Put(collectionOrdinal(2), []byte("{}"))
						})
						if err != nil {
							t.Fatal(err)
						}
					} else if name == "source" {
						f.ledger.rows[f.head.ID][2] = []byte("{}")
					} else {
						f.ledger.validationRows[f.head.ID][2] = []byte("{}")
					}
				case "prepared":
					executionRetirementReplace(t, f.ledger, f.head.ID, "prepared", nil)
				}
				f.image.Collections[f.head.ID] = f.head
				view, err := f.ledger.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				defer view.Close()
				if err := validateCollectionExecutionRetiredParent(context.Background(), f.image, f.head, view, nil); err == nil {
					t.Fatal("selected-parent runtime/recovery auditor accepted corruption")
				}
			})
		}
	}
}

func TestCollectionExecutionRetiredRecoveryBeforeBeginAndCancellation(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := executionResultInput(t, s, "create")
	head, _ = activationSnapshotCancel(t, s, head, head.Activation.At.Add(time.Second))
	head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
	if head.Execution != nil {
		t.Fatal("fixture unexpectedly began execution")
	}
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Minute)
	head.ExecutionRetirement = &CollectionExecutionRetirementState{Version: 1, Result: head.ExecutionResult.Clone(), StartedAt: at, UpdatedAt: at}
	s.fsm.mu.RLock()
	i := catalogDeltaCloneMachine(t, s.fsm).image
	s.fsm.mu.RUnlock()
	i.Version = CollectionExecutionRetirementFormatVersion
	i.Collections[head.ID] = head
	view, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	before, _ := view.Bytes()
	if err := validateCollectionRows(i, view); err != nil {
		t.Fatal("canceled original admission failed terminal-only recovery", err)
	}
	got, err := validateCollectionExecutionInventory(context.Background(), i, view)
	if err != nil || len(got.children) != 0 || len(got.trees) != 0 || len(got.commitments) != 0 || !head.ExecutionRetirement.complete(head) {
		t.Fatal("unbegun retirement created executable state", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateCollectionExecutionRetiredParent(ctx, i, head, view, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled recovery did not propagate cancellation", err)
	}
	view.mu.Lock()
	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = validateCollectionExecutionRetiredParent(ctx, i, head, view, nil)
	cancel()
	view.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("frozen-reader wait ignored deadline", err)
	}
	after, _ := view.Bytes()
	if before != after || !reflect.DeepEqual(i.Collections[head.ID], head) {
		t.Fatal("canceled auditor mutated retained evidence")
	}
	i.Version = CollectionExecutionPublicationFormatVersion
	if err := validateCollectionHeaders(i); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("format11 accepted retirement descriptor", err)
	}
}
