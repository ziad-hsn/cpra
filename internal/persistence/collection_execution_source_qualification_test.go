package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"
)

// Capture persisted state and all four logical namespaces independently of the
// cleanup planner. No source fixture uses plaintext resources or real secrets.
type sourceQualificationState struct {
	image   image
	streams [4][]byte
	charged int64
}

func captureSourceQualification(t *testing.T, f *machine) sourceQualificationState {
	t.Helper()
	f.mu.RLock()
	i, ledger := catalogDeltaCloneMachine(t, f).image, f.collections
	f.mu.RUnlock()
	input, plan, validation := validationLedgerStreams(t, ledger)
	view, err := ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	var execution bytes.Buffer
	_, err = writeCollectionExecutionLedger(&execution, view)
	if closeErr := view.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	charged, err := ledger.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return sourceQualificationState{image: i, streams: [4][]byte{input, plan, validation, execution.Bytes()}, charged: charged}
}

func assertSourceQualificationUnchanged(t *testing.T, f *machine, before sourceQualificationState, allowLogAdvance bool) {
	t.Helper()
	after := captureSourceQualification(t, f)
	if allowLogAdvance {
		if after.image.Index != before.image.Index+1 {
			t.Fatal("rejected command did not consume exactly its original log position")
		}
		after.image.Index = before.image.Index
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("cleanup changed persisted metadata, logical rows, or exact quota")
	}
}

func TestCollectionExecutionSourceQualificationLiveChildren(t *testing.T) {
	for _, state := range []string{"live", "pending-child", "canceled-pending-child"} {
		t.Run(state, func(t *testing.T) {
			s, head, begin := preparationCacheFixture(t, false, 2)
			if state != "live" {
				var child CollectionItemOutcome
				head, child = preparationCacheAccept(t, s, head, begin)
				if child.Receipt == nil || head.Execution.Accepted != 1 || head.Execution.ChildTerminals != 0 {
					t.Fatal("fixture did not leave an unresolved original child")
				}
			}
			if state == "canceled-pending-child" {
				head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
			}
			if head.ExecutionResult != nil || CollectionExecutionSourceRetirementFenceFor(head) != nil {
				t.Fatal("unfinished execution acquired cleanup authority")
			}
			before := captureSourceQualification(t, s.fsm)
			if handled, err := s.maintainCollectionExecutionRetirement(head.Execution.LastAt.Add(time.Minute)); handled || err != nil {
				t.Fatal("maintenance selected unfinished original execution", handled, err)
			}
			assertSourceQualificationUnchanged(t, s.fsm, before, false)
		})
	}
}

func TestCollectionExecutionSourceQualificationMissingSeal(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, primary := range []bool{false, true} {
			t.Run(fmt.Sprintf("disk=%t/primary=%t", disk, primary), func(t *testing.T) {
				s, head := sourceRetirementFixture(t, disk, 1)
				h := s.History()
				h.mu.Lock()
				if disk {
					day := head.ExecutionResult.Summary.FinalizedAt.UTC().Format("2006-01-02")
					err := h.databases[day].Update(func(tx *bolt.Tx) error {
						p := tx.Bucket(collectionExecutionPublicationBucket).Bucket([]byte(head.ID))
						if !primary {
							return p.Delete(collectionExecutionPublicationSummaryKey)
						}
						e, err := decodeCollectionExecutionPublicationEvent(p.Get(collectionExecutionPublicationSummaryKey))
						if err != nil {
							return err
						}
						return tx.Bucket([]byte("events")).Delete([]byte(e.MonitorID + "\x00" + e.ID))
					})
					if err != nil {
						h.mu.Unlock()
						t.Fatal(err)
					}
				} else if primary {
					evidence := h.memoryExecutionEvidence[head.ID]
					evidence.seal = nil
					h.memoryExecutionEvidence[head.ID] = evidence
				} else {
					h.memoryExecutionResults[head.ID].summary = nil
				}
				h.mu.Unlock()
				before := captureSourceQualification(t, s.fsm)
				handled, err := s.maintainCollectionExecutionRetirement(head.ExecutionRetirement.UpdatedAt.Add(time.Second))
				if !handled || !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("new source deletion did not require both retained seal copies", handled, err)
				}
				assertSourceQualificationUnchanged(t, s.fsm, before, false)
			})
		}
	}
}

func TestCollectionExecutionSourceQualificationCommittedExpiry(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := sourceRetirementFixture(t, disk, 1)
			expiryAt := head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 31)
			before := captureSourceQualification(t, s.fsm)
			if handled, err := s.maintainCollectionExecutionRetirement(expiryAt); handled || err != nil {
				t.Fatal("wall time alone admitted expired result cleanup", handled, err)
			}
			assertSourceQualificationUnchanged(t, s.fsm, before, false)
			// Actual publication expiry is committed before source cleanup. Its
			// missing old history is now intentional, including validation history.
			head = validationApplyAllowed(t, executionPublishStep(t, s, head, expiryAt))
			if !head.ExecutionResult.HistoryExpiredAt.Equal(expiryAt) {
				t.Fatal("fixture did not commit the original result's expiry")
			}
			h := s.History()
			h.mu.RLock()
			day := head.ExecutionResult.Summary.FinalizedAt.UTC().Format("2006-01-02")
			retained := h.catalog.Segments[day]
			if !disk {
				retained = h.memoryExecutionResults[head.ID] != nil
			}
			h.mu.RUnlock()
			if retained {
				t.Fatal("expiry fixture retained the old execution cohort")
			}
			before = captureSourceQualification(t, s.fsm)
			handled, err := s.maintainCollectionExecutionRetirement(expiryAt.Add(time.Second))
			if !handled || err != nil {
				t.Fatal("explicit expiry did not permit absent-history cleanup", handled, err)
			}
			after := captureSourceQualification(t, s.fsm)
			got := after.image.Collections[head.ID]
			if got.Validation.RemovedRows != head.Validation.Uploaded || got.Plan.RemovedFragments != 0 || got.RemovedRows != 0 ||
				before.charged-after.charged != head.Validation.EncodedBytes ||
				!reflect.DeepEqual(got.ExecutionResult, head.ExecutionResult) || !reflect.DeepEqual(got.Execution, head.Execution) || !reflect.DeepEqual(got.Activation, head.Activation) {
				t.Fatal("expiry cleanup rewrote original facts or refunded the wrong namespace")
			}
		})
	}
}

func TestCollectionExecutionSourceQualificationImmutableFence(t *testing.T) {
	s, head := sourceRetirementFixture(t, false, 1)
	at := head.ExecutionRetirement.UpdatedAt.Add(time.Second)
	head = validationApplyAllowed(t, executeStoreCommand(t, s, sourceRetirementCommand(head), at))
	for name, change := range map[string]func(*CollectionExecuteCommand){
		"current-prefix-digest": func(c *CollectionExecuteCommand) {
			c.SourceRetirement.Retirement.Retirement.Sources.InputDigest = identity("another-input-prefix")
		},
		"original-publication": func(c *CollectionExecuteCommand) {
			c.SourceRetirement.Retirement.Result.ProgressDigest = identity("another-publication")
			c.SourceRetirement.Retirement.Retirement.Result.ProgressDigest = identity("another-publication")
		},
		"source-start": func(c *CollectionExecuteCommand) {
			p := c.SourceRetirement.Retirement.Retirement.Sources
			p.StartedAt = p.StartedAt.Add(-time.Nanosecond)
		},
		"original-retirement-start": func(c *CollectionExecuteCommand) {
			p := c.SourceRetirement.Retirement.Retirement
			p.StartedAt = p.StartedAt.Add(-time.Nanosecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := sourceRetirementCommand(head)
			change(&c)
			if err := c.validate(at.Add(time.Second)); err != nil {
				t.Fatal("fixture failed shape validation before original-identity comparison", err)
			}
			before := captureSourceQualification(t, s.fsm)
			if r := executeStoreCommand(t, s, c, at.Add(time.Second)); !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("altered immutable source fence admitted deletion", r.Err)
			}
			assertSourceQualificationUnchanged(t, s.fsm, before, true)
		})
	}
}

// Faults affect a separate native ledger and history store, never the live
// source fixture. Production Apply still performs certification, history expiry
// and the transactional deletion; this is not a mocked deletion return value.
func TestCollectionExecutionSourceQualificationStorageFailure(t *testing.T) {
	for _, fault := range []string{"history-catalog-save", "ledger-commit"} {
		t.Run(fault, func(t *testing.T) {
			if fault == "ledger-commit" && runtime.GOOS == "windows" {
				t.Skip("read-only file-descriptor failure is a Unix fixture")
			}
			s, head := sourceRetirementFixture(t, false, 1)
			at := head.ExecutionRetirement.UpdatedAt.Add(time.Second)
			for head.Validation.RemovedRows != head.Validation.Uploaded || head.Plan.RemovedFragments != head.Plan.UploadedFragments {
				head = validationApplyAllowed(t, executeStoreCommand(t, s, sourceRetirementCommand(head), at))
				at = at.Add(time.Second)
			}
			if head.RemovedRows != 0 || head.ExecutionRetirement.Sources == nil {
				t.Fatal("failure fixture did not leave only the original input")
			}
			original := captureSourceQualification(t, s.fsm)
			f := catalogDeltaCloneMachine(t, &machine{image: original.image})
			f.collections = newLedgerTest(t, true, 32<<20)
			for _, err := range []error{
				importCollectionLedger(bytes.NewReader(original.streams[0]), f.collections),
				importCollectionPlanLedger(bytes.NewReader(original.streams[1]), f.collections),
				importCollectionValidationLedger(bytes.NewReader(original.streams[2]), f.collections),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			historyDir := t.TempDir()
			var err error
			f.history, err = openHistory(historyDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.history.Close() })
			before := captureSourceQualification(t, f)
			if !reflect.DeepEqual(before, original) {
				t.Fatal("isolated failure fixture changed original committed data")
			}
			ledgerPath := f.collections.db.Path()
			if fault == "history-catalog-save" {
				if err := os.Mkdir(filepath.Join(historyDir, "catalog.json"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.collections.db.Close(); err != nil {
					t.Fatal(err)
				}
				f.collections.db, err = bolt.Open(ledgerPath, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20,
					OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(name) }})
				if err != nil {
					t.Fatal(err)
				}
			}
			c := sourceRetirementCommand(head)
			raw, err := json.Marshal(envelope{Version: CollectionExecutionSourceRetirementFormatVersion,
				Commands: []Command{{Kind: "collection_execute", CollectionExecute: &c, At: at}}})
			if err != nil {
				t.Fatal(err)
			}
			result := f.Apply(&raft.Log{Index: f.image.Index + 1, Data: raw})
			applyErr, ok := result.(error)
			if !ok || !errors.Is(applyErr, ErrCollectionUnavailable) || f.err == nil || f.collectionSourceCertificate != nil {
				t.Fatal("failed storage was acknowledged, left replay healthy, or retained its certificate", result)
			}
			if fault == "history-catalog-save" {
				var linkErr *os.LinkError
				if !errors.As(applyErr, &linkErr) || linkErr.New != filepath.Join(historyDir, "catalog.json") || f.history.retainedCutoff.Load() != nil {
					t.Fatal("fixture missed durable history save failure or published its cutoff", applyErr)
				}
			} else {
				var pathErr *os.PathError
				if !errors.As(applyErr, &pathErr) || pathErr.Path != ledgerPath || f.history.retainedCutoff.Load() == nil {
					t.Fatal("fixture missed ledger commit after successful history expiry", applyErr)
				}
				if err := f.collections.db.Close(); err != nil {
					t.Fatal(err)
				}
				f.collections.db, err = bolt.Open(ledgerPath, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20})
				if err != nil {
					t.Fatal(err)
				}
			}
			assertSourceQualificationUnchanged(t, f, before, false)
			assertSourceQualificationUnchanged(t, s.fsm, original, false)
		})
	}
}
