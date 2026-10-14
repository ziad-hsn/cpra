package persistence

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestCollectionWriteContentionPreservesHealthAndOriginalOutcome(t *testing.T) {
	if runCollectionContentionChild(t) {
		return
	}
	for _, cancelWait := range []bool{false, true} {
		name := "wait"
		if cancelWait {
			name = "cancel-wait"
		}
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			s, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if s != nil {
					if err := s.Close(); err != nil {
						t.Error("close collection owner", err)
					}
				}
			})
			monitor, guard := managedControlMonitor(t, s, "contended-upload")
			head := createCollectionFixture(t, s, 3)
			first := collectionItemFixture(t, s, head, 1, "first")
			head = validationApplyAllowed(t, uploadCollectionFixture(t, s, head, first, time.Now().UTC()))
			second := collectionItemFixture(t, s, head, 2, "second")
			command := Command{Kind: "collection", At: time.Now().UTC(), Collection: &CollectionCommand{
				Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &second,
			}}
			before := s.Status().CommittedIndex
			ledger := s.fsm.collections
			if ledger == nil || ledger.db == nil || ledger.db.NoSync {
				t.Fatal("fixture requires the real synchronously written collection ledger")
			}
			// Hold bbolt's single writer. The production Raft/FSM submission must
			// reach beginUploadTransaction and wait here, without an I/O mock or
			// a production test hook. Nothing in this transaction is committed.
			blocked, err := ledger.db.Begin(true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocked.Rollback() }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan reply, 1)
			go func() {
				results, err := s.Submit(ctx, []Command{command})
				done <- reply{results: results, err: err}
			}()
			waitBudgetCondition(t, "Raft upload waiting for the ledger writer", func() bool {
				if ledger.mu.TryRLock() {
					ledger.mu.RUnlock()
					return false
				}
				return true
			})
			if s.fsm.mu.TryRLock() {
				s.fsm.mu.RUnlock()
				t.Fatal("upload did not retain the authoritative FSM write lock")
			}
			select {
			case result := <-done:
				t.Fatal("blocked upload returned before the writer was released", result.err)
			default:
			}
			probe, stopProbe := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer stopProbe()
			if err := s.ControllerHealthContext(probe); err != nil {
				t.Fatal("ledger contention poisoned routine health", err)
			}
			if status, err := s.StatusContext(probe); err != nil || !status.Ready || status.CommittedIndex != before {
				t.Fatal("routine observation waited for or advertised the uncommitted prefix", status, err)
			}
			admit, stopAdmission := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer stopAdmission()
			if err := s.CheckCheckAdmissionContext(admit, monitor.ID, guard, monitor.ControlRevision, time.Now().UTC()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("execution admission bypassed the busy authoritative state", err)
			}
			if cancelWait {
				cancel()
				select {
				case result := <-done:
					if len(result.results) != 0 || !errors.Is(result.err, ErrCommitUnconfirmed) || !errors.Is(result.err, context.Canceled) {
						t.Fatal("cancellation lost the admitted write's uncertain outcome", result.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("canceled caller remained blocked on the ledger")
				}
			}
			if err := blocked.Rollback(); err != nil {
				t.Fatal(err)
			}
			if !cancelWait {
				select {
				case result := <-done:
					if result.err != nil || len(result.results) != 1 || result.results[0].Err != nil || !result.results[0].Allowed {
						t.Fatal("upload failed after temporary contention", result.err, result.results)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("upload did not finish after the writer was released")
				}
			}
			// A barrier follows the original submission, including when its caller
			// stopped waiting. Reconcile that outcome; do not repeat the upload.
			barrier := submit(t, s, Command{Kind: "barrier", At: time.Now().UTC()})
			if len(barrier) != 1 || !barrier[0].Allowed || barrier[0].Err != nil {
				t.Fatal("storage could not make progress after contention", barrier)
			}
			current, exists, err := s.CollectionGet(head.ID)
			rows, pageErr := s.CollectionPage(head.ID, 0, 3)
			if err != nil || pageErr != nil || !exists || current.Uploaded != 2 ||
				!reflect.DeepEqual(rows, []CollectionItem{first, second}) || !s.Status().Ready {
				t.Fatal("original accepted prefix was lost, duplicated or changed", err, pageErr)
			}
			if err := s.CheckCheckAdmissionContext(t.Context(), monitor.ID, guard, monitor.ControlRevision, time.Now().UTC()); err != nil {
				t.Fatal("temporary observation/admission deadlines left execution unavailable", err)
			}
			third := collectionItemFixture(t, s, current, 3, "third")
			if result := uploadCollectionFixture(t, s, current, third, time.Now().UTC()); result.Err != nil || !result.Allowed || result.Collection == nil || result.Collection.Uploaded != 3 {
				t.Fatal("subsequent original upload could not continue", result.Err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			rows, err = s.CollectionPage(head.ID, 0, 3)
			if err != nil || !reflect.DeepEqual(rows, []CollectionItem{first, second, third}) || !s.Status().Ready {
				t.Fatal("confirmed original prefix failed real Raft restart", err)
			}
		})
	}
}

func TestCollectionLedgerFailureRemainsUnavailable(t *testing.T) {
	if runCollectionContentionChild(t) {
		return
	}
	s, err := Open(t.Context(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error("close failed collection owner", err)
		}
	})
	head := createCollectionFixture(t, s, 1)
	item := collectionItemFixture(t, s, head, 1, "first")
	ledger := s.fsm.collections
	// A missing materialization bucket is a real ledger failure, unlike a
	// temporarily occupied writer. Restore/reopening is an operator decision.
	ledger.mu.Lock()
	err = ledger.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket(collectionLedgerMeta) })
	ledger.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Submit(t.Context(), []Command{{Kind: "collection", At: time.Now().UTC(), Collection: &CollectionCommand{
		Action: "upload", OperationID: head.ID, UploadID: head.UploadID, Item: &item,
	}}})
	if !errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatal("damaged ledger did not retain an uncertain committed write", err)
	}
	for range 3 {
		status := s.Status()
		if status.Ready || status.Error == "" || strings.Contains(status.Error, s.config.Storage.Directory) || s.ControllerHealth() == nil {
			t.Fatal("permanent ledger failure was cleared or leaked a local path", status)
		}
		if _, err := s.Submit(t.Context(), []Command{{Kind: "barrier", At: time.Now().UTC()}}); err == nil {
			t.Fatal("new submission bypassed permanent storage failure")
		}
	}
}

// The child owns the blocking transaction and all Store goroutines. A broken
// context-aware read or shutdown cannot prevent the parent from terminating
// the fixture. Temporary files remain under the parent's cleanup on every OS.
func runCollectionContentionChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv("CPRA_COLLECTION_CONTENTION_CHILD") == t.Name() {
		return false
	}
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v", "-test.timeout=60s")
	cmd.WaitDelay = 5 * time.Second
	directory := t.TempDir()
	cmd.Env = append(os.Environ(), "CPRA_COLLECTION_CONTENTION_CHILD="+t.Name(), "TMPDIR="+directory, "TMP="+directory, "TEMP="+directory)
	output, err := cmd.CombinedOutput()
	t.Log(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("collection contention process failed: %v (deadline: %v)", err, ctx.Err())
	}
	return true
}
