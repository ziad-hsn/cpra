package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func reselectionTransferFixture(t *testing.T, f *reselectionProofFixture) (*collectionReselectionTransfer, *reselectionSpool) {
	t.Helper()
	spool, sources := f.stage(t, f.raw)
	proof, err := f.prove(t.Context(), spool, sources, "team/operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := newReselectionTransfer(t.Context(), proof)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.closed || proof.verified {
		t.Fatal("constructor did not move proof ownership")
	}
	t.Cleanup(func() {
		if err := holder.Close(); err != nil {
			t.Error(err)
		}
	})
	return holder, spool
}

func reselectionTransferHead(t *testing.T, f *reselectionProofFixture) persistence.CollectionState {
	t.Helper()
	head, found, err := f.store.CollectionGet(f.head.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	return head
}

func TestCollectionReselectionTransferOneRowAndReadOnlyCompletion(t *testing.T) {
	for _, uploaded := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(uploaded), func(t *testing.T) {
			f := newReselectionProofFixture(t, uploaded, nil, nil)
			before, err := f.store.CollectionPage(f.head.ID, 0, 256)
			if err != nil {
				t.Fatal(err)
			}
			holder, spool := reselectionTransferFixture(t, f)
			for next := uploaded + 1; next <= len(f.items); next++ {
				admissions := 0
				progress, err := holder.step(t.Context(), func(commit func() error) error { admissions++; return commit() })
				if err != nil || admissions != 1 || progress.Reconciled || progress.Complete != (next == len(f.items)) || progress.Operation.ID != f.head.ID || progress.Operation.Uploaded == nil || *progress.Operation.Uploaded != int64(next) {
					t.Fatal("step did not commit exactly one original row", progress, admissions, err)
				}
			}
			index := f.store.Status().CommittedIndex
			progress, err := holder.step(t.Context(), func(func() error) error { t.Error("completed transfer invoked admission"); return nil })
			if err != nil || !progress.Complete || f.store.Status().CommittedIndex != index {
				t.Fatal("completed transfer mutated state", err)
			}
			after, err := f.store.CollectionPage(f.head.ID, 0, 256)
			if err != nil || uploaded != 0 && !reflect.DeepEqual(before, after[:uploaded]) {
				t.Fatal("original ciphertext changed", err)
			}
			for i, row := range after {
				plain, err := f.catalog.sealer.Open(t.Context(), row.Binding(f.catalog.storeID, f.head.UploadID), row.Payload)
				if err != nil || string(plain) != string(f.items[i].Resource) {
					t.Error("transferred original bytes changed", err)
				}
				clear(plain)
			}
			active, err := f.store.CatalogSnapshot()
			if err != nil || active.Len() != 0 || reselectionTransferHead(t, f).ValidationRequest != nil {
				t.Fatal("transfer activated/validated configuration", err)
			}
			if _, err := json.Marshal(holder); err == nil {
				t.Fatal("private transfer serialized")
			}
			if err := holder.Close(); err != nil {
				t.Fatal(err)
			}
			if !spool.closed || spool.key != [32]byte{} {
				t.Fatal("transfer did not close owned spool")
			}
		})
	}
}

func TestCollectionReselectionTransferLostReplyReconcilesExactCiphertext(t *testing.T) {
	f := newReselectionProofFixture(t, 0, nil, nil)
	holder, _ := reselectionTransferFixture(t, f)
	calls := 0
	holder.submit = func(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
		calls++
		results, err := f.store.Submit(ctx, commands)
		if err != nil || len(results) != 1 || results[0].Err != nil {
			t.Fatal("actual commit failed", err)
		}
		return nil, persistence.ErrCommitUnconfirmed
	}
	if _, err := holder.step(t.Context(), allowCollectionCommit); !errors.Is(err, ErrOutcomeUnconfirmed) {
		t.Fatal(err)
	}
	if holder.pending == nil || !holder.uncertain {
		t.Fatal("lost reply discarded pending encrypted command")
	}
	rows, err := f.store.CollectionPage(f.head.ID, 0, 256)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], *holder.pending.Collection.Item) {
		t.Fatal("lost reply row differs from exact pending ciphertext", err)
	}
	index := f.store.Status().CommittedIndex
	progress, err := holder.step(t.Context(), func(func() error) error { t.Error("reconciliation retried mutation"); return nil })
	if err != nil || !progress.Reconciled || progress.Complete || calls != 1 || holder.pending != nil || f.store.Status().CommittedIndex != index {
		t.Fatal("lost reply did not reconcile read-only", progress, calls, err)
	}
	holder.submit = f.store.Submit
	if progress, err := holder.step(t.Context(), allowCollectionCommit); err != nil || !progress.Complete {
		t.Fatal("known next row failed", err)
	}
}

func TestCollectionReselectionTransferUnconfirmedAbsenceNeverResubmits(t *testing.T) {
	f := newReselectionProofFixture(t, 0, nil, nil)
	holder, _ := reselectionTransferFixture(t, f)
	calls := 0
	holder.submit = func(context.Context, []persistence.Command) ([]persistence.Result, error) {
		calls++
		return nil, persistence.ErrCommitUnconfirmed
	}
	if _, err := holder.step(t.Context(), allowCollectionCommit); !errors.Is(err, ErrOutcomeUnconfirmed) {
		t.Fatal(err)
	}
	frozen, _ := json.Marshal(holder.pending)
	index := f.store.Status().CommittedIndex
	for range 3 {
		if _, err := holder.step(t.Context(), func(func() error) error { t.Error("uncertainty retried admission"); return nil }); !errors.Is(err, ErrOutcomeUnconfirmed) {
			t.Fatal(err)
		}
	}
	current, _ := json.Marshal(holder.pending)
	if string(current) != string(frozen) || calls != 1 || f.store.Status().CommittedIndex != index || reselectionTransferHead(t, f).Uploaded != 0 {
		t.Fatal("unconfirmed absent mutation was changed/retried")
	}
}

func TestCollectionReselectionTransferAdmissionDenialKeepsPreparedEnvelope(t *testing.T) {
	f := newReselectionProofFixture(t, 0, nil, nil)
	holder, _ := reselectionTransferFixture(t, f)
	denied := errors.New("test admission denied")
	if _, err := holder.step(t.Context(), func(func() error) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if holder.pending == nil || holder.uncertain {
		t.Fatal("unsubmitted preparation marked uncertain")
	}
	row := holder.pending.Collection.Item.Clone()
	f.at = f.at.Add(time.Second)
	if _, err := holder.step(t.Context(), allowCollectionCommit); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.CollectionPage(f.head.ID, 0, 256)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], row) {
		t.Fatal("known unsubmitted ciphertext regenerated", err)
	}
}

func TestCollectionReselectionTransferConcurrentUploaderConflicts(t *testing.T) {
	for _, afterOwnCommit := range []bool{false, true} {
		t.Run(fmt.Sprint(afterOwnCommit), func(t *testing.T) {
			f := newReselectionProofFixture(t, 0, nil, nil)
			holder, _ := reselectionTransferFixture(t, f)
			uploadOther := func(index int) {
				_, err := f.catalog.UploadCollection(t.Context(), f.head.ID, "team/operator", f.items[index:index+1], func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at }, allowCollectionCommit)
				if err != nil {
					t.Fatal(err)
				}
			}
			if afterOwnCommit {
				holder.submit = func(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
					results, err := f.store.Submit(ctx, commands)
					if err != nil || len(results) != 1 || results[0].Err != nil {
						t.Fatal(err)
					}
					uploadOther(1)
					return nil, persistence.ErrCommitUnconfirmed
				}
				if _, err := holder.step(t.Context(), allowCollectionCommit); !errors.Is(err, ErrOutcomeUnconfirmed) {
					t.Fatal(err)
				}
			} else {
				if _, err := holder.step(t.Context(), func(commit func() error) error { uploadOther(0); return commit() }); !errors.Is(err, persistence.ErrCollectionConflict) {
					t.Fatal(err)
				}
			}
			index := f.store.Status().CommittedIndex
			if _, err := holder.step(t.Context(), allowCollectionCommit); !errors.Is(err, persistence.ErrCollectionConflict) {
				t.Fatal("concurrent prefix was merged", err)
			}
			if f.store.Status().CommittedIndex != index || holder.next != 0 {
				t.Fatal("conflicting prefix advanced transfer")
			}
		})
	}
}

func TestCollectionReselectionTransferAdmissionRechecksFences(t *testing.T) {
	for _, change := range []string{"context", "authority-expiry", "upload-expiry", "canceled", "permission"} {
		t.Run(change, func(t *testing.T) {
			f := newReselectionProofFixture(t, 0, nil, nil)
			holder, _ := reselectionTransferFixture(t, f)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, err := holder.step(ctx, func(commit func() error) error {
				switch change {
				case "context":
					cancel()
				case "authority-expiry":
					f.at = f.policy.Principals[0].ExpiresAt
				case "upload-expiry":
					f.at = f.head.ExpiresAt
				case "permission":
					holder.proof.canWrite = func(persistence.CatalogKey) bool { return false }
				case "canceled":
					if _, err := f.catalog.CancelCollection(t.Context(), f.head.ID, "team/operator", func() time.Time { return f.at }, allowCollectionCommit); err != nil {
						t.Fatal(err)
					}
				}
				return commit()
			})
			if err == nil || reselectionTransferHead(t, f).Uploaded != 0 {
				t.Fatal("stale admission uploaded suffix", err)
			}
		})
	}
}

func TestCollectionReselectionTransferFSMRejectsAdvancedPrefix(t *testing.T) {
	f := newReselectionProofFixture(t, 0, nil, nil)
	holder, _ := reselectionTransferFixture(t, f)
	var other persistence.CollectionItem
	holder.submit = func(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
		if len(commands) != 1 || commands[0].Collection.UploadFence == nil {
			t.Fatal("conditional transfer omitted atomic upload fence")
		}
		// Advance after all manager checks, immediately before real Submit:
		// only the FSM can reject this stale prefix at this boundary.
		_, err := f.catalog.UploadCollection(ctx, f.head.ID, "team/operator", f.items[:1],
			func(persistence.CatalogKey) bool { return true }, func() time.Time { return f.at }, allowCollectionCommit)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.store.CollectionPage(f.head.ID, 0, 256)
		if err != nil || len(rows) != 1 {
			t.Fatal(err)
		}
		other = rows[0]
		return f.store.Submit(ctx, commands)
	}
	if _, err := holder.step(t.Context(), allowCollectionCommit); !errors.Is(err, persistence.ErrCollectionConflict) {
		t.Fatal("FSM admitted stale transfer prefix", err)
	}
	rows, err := f.store.CollectionPage(f.head.ID, 0, 256)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], other) || holder.next != 0 {
		t.Fatal("stale transfer replaced concurrent ciphertext", err)
	}
}

func TestCollectionReselectionTransferNativePartialRestart(t *testing.T) {
	config := runtimeconfig.Default()
	config.Storage.Directory = t.TempDir()
	catalog, store := reselectionNativeOpen(t, config)
	f := newReselectionProofFixtureStore(t, catalog, store, 0, nil, nil)
	holder, _ := reselectionTransferFixture(t, f)
	if progress, err := holder.step(t.Context(), allowCollectionCommit); err != nil || progress.Complete {
		t.Fatal(err)
	}
	prefix := reselectionNativePrefix(t, store, f.head.ID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.step(t.Context(), allowCollectionCommit); err == nil {
		t.Fatal("stopped owner transferred work")
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	f.catalog, f.store = reselectionNativeOpen(t, config)
	f.at = time.Now().UTC()
	fresh, _ := reselectionTransferFixture(t, f)
	if progress, err := fresh.step(t.Context(), allowCollectionCommit); err != nil || !progress.Complete || progress.Operation.ID != f.head.ID {
		t.Fatal("restart did not finish original operation", err)
	}
	after, err := f.store.CollectionPage(f.head.ID, 0, 256)
	if err != nil || len(after) != 2 || !reflect.DeepEqual(prefix, after[:1]) {
		t.Fatal("restart rewrote prefix", err)
	}
}

func TestCollectionReselectionTransferCloseWaitsForStep(t *testing.T) {
	f := newReselectionProofFixture(t, 0, nil, nil)
	holder, spool := reselectionTransferFixture(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var group sync.WaitGroup
	group.Go(func() {
		_, err := holder.step(ctx, func(commit func() error) error { close(entered); <-release; return commit() })
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	<-entered
	done := make(chan error, 1)
	go func() { done <- holder.Close() }()
	select {
	case <-done:
		t.Fatal("Close abandoned active step")
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	close(release)
	group.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !spool.closed || spool.key != [32]byte{} || reselectionTransferHead(t, f).Uploaded != 0 {
		t.Fatal("shutdown lost exclusive ownership")
	}
}
