package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func executionSourceAuditFixture(t *testing.T, count int) executionRecoveryFixture {
	t.Helper()
	s, head, _ := preparationCacheFixture(t, false, count)
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
	return executionRetirementRecoveryCapture(t, s, head)
}

func executionSourceAuditBatch(t *testing.T, f executionRecoveryFixture) *collectionExecutionSourceRetirementBatch {
	t.Helper()
	view, err := f.ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.ledger.Bytes()
	batch, err := planCollectionExecutionSourceRetirement(t.Context(), f.image, f.head, view)
	if closeErr := view.Close(); err != nil || closeErr != nil {
		t.Fatal("source plan", err, closeErr)
	}
	if after, _ := f.ledger.Bytes(); after != before {
		t.Fatal("audit mutated ledger quota")
	}
	return batch
}

func executionSourceAuditDelete(t *testing.T, f *executionRecoveryFixture, batch *collectionExecutionSourceRetirementBatch) {
	t.Helper()
	s := &f.head
	var actual collectionLedgerDeletion
	var err error
	switch batch.Namespace {
	case "validation":
		actual, err = f.ledger.DeleteValidationPage(s.ID, s.Validation.Uploaded-s.Validation.RemovedRows, s.Validation.EncodedBytes-s.Validation.RemovedBytes)
		s.Validation.RemovedRows += actual.Rows
		s.Validation.RemovedBytes += actual.EncodedBytes
	case "plan":
		actual, err = f.ledger.DeletePlanPage(s.ID, s.Plan.UploadedFragments-s.Plan.RemovedFragments, s.Plan.EncodedBytes-s.Plan.RemovedBytes)
		s.Plan.RemovedFragments += actual.Rows
		s.Plan.RemovedBytes += actual.EncodedBytes
	case "input":
		actual, err = f.ledger.DeletePage(s.ID, s.Uploaded-s.RemovedRows, s.EncodedBytes-s.RemovedBytes)
		s.RemovedRows += actual.Rows
		s.RemovedBytes += actual.EncodedBytes
	default:
		t.Fatalf("unexpected deletion namespace %q", batch.Namespace)
	}
	if err != nil || actual != batch.Removed {
		t.Fatal("planned tail differs from committed deletion", batch.Removed, actual, err)
	}
	if s.ExecutionRetirement.Sources == nil {
		at := s.ExecutionRetirement.UpdatedAt.Add(time.Second)
		s.ExecutionRetirement.Sources = &CollectionExecutionSourceRetirementState{Version: 1, StartedAt: at, UpdatedAt: at}
	}
	source := s.ExecutionRetirement.Sources
	source.InputDigest, source.PlanDigest, source.ValidationDigest = batch.InputDigest, batch.PlanDigest, batch.ValidationDigest
	source.UpdatedAt = source.UpdatedAt.Add(time.Second)
	f.image.Version = CollectionExecutionSourceRetirementFormatVersion
	f.image.Collections[s.ID] = s.Clone()
	if err := s.validate(); err != nil {
		t.Fatal("resulting source header", err)
	}
}

func TestCollectionExecutionSourceAuditExactBoundedTails(t *testing.T) {
	original := executionSourceAuditFixture(t, 260)
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f := executionRetirementRecoveryPrefix(t, original, disk, 0, false)
			before, _ := f.ledger.Bytes()
			var refunded int64
			var namespaces []string
			for step := 0; ; step++ {
				if step > 32 {
					t.Fatal("source deletion failed to converge")
				}
				batch := executionSourceAuditBatch(t, f)
				if batch.Namespace == "header" {
					if batch.Removed != (collectionLedgerDeletion{}) || batch.InputDigest != collectionInitialDigest() || batch.PlanDigest != collectionPlanInitialDigest() || batch.ValidationDigest != CollectionValidationInitialDigest() {
						t.Fatal("empty namespaces did not retain canonical initial commitments")
					}
					break
				}
				if batch.Removed.Rows == 0 || batch.Removed.Rows > collectionLedgerBatchLimit || batch.Removed.EncodedBytes <= 0 || batch.Removed.EncodedBytes > collectionLedgerBatchBytes {
					t.Fatal("unbounded or empty selected tail", batch)
				}
				if step == 0 && (batch.Namespace != "validation" || batch.Removed.Rows != 256 || !batch.Removed.More) {
					t.Fatal("first bounded tail did not leave four validation rows", batch)
				}
				namespaces = append(namespaces, batch.Namespace)
				executionSourceAuditDelete(t, &f, batch)
				refunded += batch.Removed.EncodedBytes
				remaining, _ := f.ledger.Bytes()
				if remaining != before-refunded {
					t.Fatal("source quota refund differs from actual paired metadata", remaining, before, refunded)
				}
				view, err := f.ledger.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				err = validateCollectionExecutionSourcePrefixes(t.Context(), f.image, f.head, view)
				if closeErr := view.Close(); err != nil || closeErr != nil {
					t.Fatal("surviving source audit", err, closeErr)
				}
			}
			if len(namespaces) < 6 || namespaces[0] != "validation" || namespaces[1] != "validation" || namespaces[len(namespaces)-2] != "input" || namespaces[len(namespaces)-1] != "input" {
				t.Fatal("strict validation/plan/input order lost", namespaces)
			}
			if remaining, _ := f.ledger.Bytes(); remaining != 0 || before != refunded {
				t.Fatal("final source deletion retained quota", remaining, before, refunded)
			}
			if !reflect.DeepEqual(f.head.Execution, original.head.Execution) || !reflect.DeepEqual(f.head.ExecutionResult, original.head.ExecutionResult) || f.head.Plan.Descriptor != original.head.Plan.Descriptor || f.head.Validation.Descriptor != original.head.Validation.Descriptor || f.head.ProgressDigest != original.head.ProgressDigest {
				t.Fatal("source deletion replaced original immutable commitments")
			}
		})
	}
}

func executionSourceAuditPut(t *testing.T, f executionRecoveryFixture, namespace string, ordinal uint64, raw []byte) {
	t.Helper()
	l := f.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	var rows map[uint64][]byte
	var bucket []byte
	switch namespace {
	case "input":
		rows, bucket = l.rows[f.head.ID], collectionLedgerRecords
	case "plan":
		rows, bucket = l.planRows[f.head.ID], collectionLedgerPlans
	case "validation":
		rows, bucket = l.validationRows[f.head.ID], collectionLedgerValidation
	}
	if l.db != nil {
		if err := l.db.Update(func(tx *bolt.Tx) error {
			r := tx.Bucket(bucket).Bucket([]byte(f.head.ID))
			if raw == nil {
				return r.Delete(collectionOrdinal(ordinal))
			}
			return r.Put(collectionOrdinal(ordinal), raw)
		}); err != nil {
			t.Fatal(err)
		}
	} else if raw == nil {
		delete(rows, ordinal)
	} else {
		rows[ordinal] = raw
	}
}

func TestCollectionExecutionSourceAuditShortenedInputCommitment(t *testing.T) {
	original := executionSourceAuditFixture(t, 260)
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f := executionRetirementRecoveryPrefix(t, original, disk, 0, false)
			for f.head.RemovedRows == 0 {
				executionSourceAuditDelete(t, &f, executionSourceAuditBatch(t, f))
			}
			if f.head.Uploaded-f.head.RemovedRows != 4 {
				t.Fatal("fixture did not retain shortened input prefix")
			}
			item, found, err := f.ledger.Item(f.head.ID, 1)
			if err != nil || !found {
				t.Fatal(err)
			}
			originalRaw, _ := collectionItemEncoding(f.head.ID, item)
			item.Payload.Ciphertext[0] ^= 1
			changed, err := collectionItemEncoding(f.head.ID, item)
			if err != nil || len(changed) != len(originalRaw) || bytes.Equal(changed, originalRaw) {
				t.Fatal("fixture did not make a canonical same-size substitution", err)
			}
			executionSourceAuditPut(t, f, "input", 1, changed)
			view, err := f.ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			if err := validateCollectionExecutionSourcePrefixes(t.Context(), f.image, f.head, view); err == nil {
				t.Fatal("same-size ciphertext substitution passed surviving-prefix commitment")
			}
			if batch, err := planCollectionExecutionSourceRetirement(t.Context(), f.image, f.head, view); err == nil || batch != nil {
				t.Fatal("corrupt prefix produced a deletion batch", err)
			}
		})
	}
}

func TestCollectionExecutionSourceAuditShortenedPlanCommitment(t *testing.T) {
	original := executionSourceAuditFixture(t, 260)
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			f := executionRetirementRecoveryPrefix(t, original, disk, 0, false)
			for f.head.Plan.RemovedFragments == 0 {
				executionSourceAuditDelete(t, &f, executionSourceAuditBatch(t, f))
			}
			remaining := f.head.Plan.UploadedFragments - f.head.Plan.RemovedFragments
			view, err := f.ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := view.encodedPlanPart(f.head.ID, remaining)
			originalRaw := bytes.Clone(raw)
			if closeErr := view.Close(); err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			part, err := decodeCollectionPlanLedgerRow(originalRaw)
			if err != nil || part.Part.Fragment.Row == nil || part.Part.Fragment.Row.GuardCount != 0 {
				t.Fatal("fixture must end at an unfinished row before its row-end commitment", err)
			}
			part.Part.Fragment.Row.GuardCount = 1
			changed, err := collectionPlanLedgerEncoding(f.head.ID, part.Part)
			if err != nil || len(changed) != len(originalRaw) {
				t.Fatal("fixture must retain canonical shape and encoded size", err)
			}
			executionSourceAuditPut(t, f, "plan", remaining, changed)
			view, err = f.ledger.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			// The old terminal-prefix codec check accepts this unfinished row.
			// Only the retained-prefix commitment distinguishes the substitution.
			prefix := newCollectionPlanPrefix(f.head.Plan.Header)
			defer prefix.close()
			for ordinal := uint64(1); ordinal <= remaining; ordinal++ {
				data, readErr := view.encodedPlanPart(f.head.ID, ordinal)
				row, decodeErr := decodeCollectionPlanLedgerRow(data)
				if readErr != nil || decodeErr != nil || prefix.add(t.Context(), f.head.ID, row.Part) != nil {
					t.Fatal("substitution was not a valid terminal prefix", readErr, decodeErr)
				}
			}
			if err := prefix.matches(f.head.Plan, true); err != nil {
				t.Fatal("substitution already rejected by old structural check", err)
			}
			if err := validateCollectionExecutionSourcePrefixes(t.Context(), f.image, f.head, view); err == nil {
				t.Fatal("same-size unfinished-row substitution passed retained plan commitment")
			}
		})
	}
}

func TestCollectionExecutionSourceAuditInputIndexAndExecutionAbsence(t *testing.T) {
	original := executionRetirementRecoverySource(t)
	for _, disk := range []bool{false, true} {
		for _, corruption := range []string{"extra-index", "wrong-index", "retired-execution"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, corruption), func(t *testing.T) {
				f := executionRetirementRecoveryPrefix(t, original, disk, original.head.Execution.Processed, true)
				if corruption == "retired-execution" {
					if err := f.ledger.importExecutionBatch(original.records); err != nil {
						t.Fatal(err)
					}
				} else {
					item, found, err := f.ledger.Item(f.head.ID, 1)
					if err != nil || !found {
						t.Fatal(err)
					}
					key := item.Key
					ordinal := uint64(2)
					if corruption == "extra-index" {
						key.ID += "-orphan"
						ordinal = 1
					}
					if disk {
						err = f.ledger.db.Update(func(tx *bolt.Tx) error {
							return tx.Bucket(collectionLedgerKeys).Bucket([]byte(f.head.ID)).Put([]byte(key.indexKey()), collectionOrdinal(ordinal))
						})
					} else {
						f.ledger.keys[f.head.ID][key] = ordinal
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				view, err := f.ledger.Freeze()
				if err != nil {
					t.Fatal(err)
				}
				defer view.Close()
				if batch, err := planCollectionExecutionSourceRetirement(t.Context(), f.image, f.head, view); err == nil || batch != nil {
					t.Fatal("uncertified inventory produced source deletion", err)
				}
			})
		}
	}
}

func TestCollectionExecutionSourceAuditPhysicalInventory(t *testing.T) {
	original := executionSourceAuditFixture(t, 3)
	for _, disk := range []bool{false, true} {
		for _, namespace := range []string{"input", "plan", "validation"} {
			for _, corruption := range []string{"missing", "extra", "sparse", "noncanonical"} {
				t.Run(fmt.Sprintf("disk=%t/%s/%s", disk, namespace, corruption), func(t *testing.T) {
					f := executionRetirementRecoveryPrefix(t, original, disk, 0, false)
					view, err := f.ledger.Freeze()
					if err != nil {
						t.Fatal(err)
					}
					var raw []byte
					var count uint64
					switch namespace {
					case "input":
						raw, err = view.encodedItem(f.head.ID, 1)
						count = f.head.Uploaded
					case "plan":
						raw, err = view.encodedPlanPart(f.head.ID, 1)
						count = f.head.Plan.UploadedFragments
					case "validation":
						raw, err = view.encodedValidationItem(f.head.ID, 1)
						count = f.head.Validation.Uploaded
					}
					raw = bytes.Clone(raw)
					if closeErr := view.Close(); err != nil || closeErr != nil {
						t.Fatal(err, closeErr)
					}
					switch corruption {
					case "missing":
						executionSourceAuditPut(t, f, namespace, 1, nil)
					case "extra":
						executionSourceAuditPut(t, f, namespace, count+1, raw)
					case "sparse":
						executionSourceAuditPut(t, f, namespace, 1, nil)
						executionSourceAuditPut(t, f, namespace, count+1, raw)
					case "noncanonical":
						executionSourceAuditPut(t, f, namespace, 1, append(raw, ' '))
					}
					before, _ := f.ledger.Bytes()
					view, err = f.ledger.Freeze()
					if err != nil {
						t.Fatal(err)
					}
					batch, auditErr := planCollectionExecutionSourceRetirement(t.Context(), f.image, f.head, view)
					if err := view.Close(); err != nil || auditErr == nil || batch != nil {
						t.Fatal("invalid physical source namespace produced deletion", auditErr, err)
					}
					if after, _ := f.ledger.Bytes(); after != before {
						t.Fatal("failed audit changed quota")
					}
				})
			}
		}
	}
}

func TestCollectionExecutionSourceAuditCancellationAndIsolation(t *testing.T) {
	original := executionSourceAuditFixture(t, 3)
	f := executionRetirementRecoveryPrefix(t, original, false, 0, false)
	view, err := f.ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	// An unrelated malformed namespace must not be traversed by a selected-parent audit.
	view.rows["unrelated"] = map[uint64][]byte{1: []byte("invalid")}
	view.planRows["unrelated"] = map[uint64][]byte{1: []byte("invalid")}
	view.validationRows["unrelated"] = map[uint64][]byte{1: []byte("invalid")}
	if err := validateCollectionExecutionSourcePrefixes(t.Context(), f.image, f.head, view); err != nil {
		t.Fatal("selected audit scanned unrelated parent", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if batch, err := planCollectionExecutionSourceRetirement(ctx, f.image, f.head, view); !errors.Is(err, context.Canceled) || batch != nil {
		t.Fatal("canceled audit produced deletion plan", err)
	}
	view.mu.Lock()
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
	batch, err := planCollectionExecutionSourceRetirement(ctx, f.image, f.head, view)
	cancel()
	view.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || batch != nil {
		t.Fatal("contended frozen view ignored audit deadline", err)
	}
	if batch, err := planCollectionExecutionSourceRetirement(nil, f.image, f.head, view); err == nil || batch != nil {
		t.Fatal("nil context accepted")
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := planCollectionExecutionSourceRetirement(t.Context(), f.image, f.head, view); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal("closed view accepted", err)
	}
}
