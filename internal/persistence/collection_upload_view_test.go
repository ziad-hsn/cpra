package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func uploadViewFixture(t *testing.T, disk bool, declared uint64, uploaded int) (*Store, CollectionState, *CollectionUploadView) {
	t.Helper()
	s := collectionReadStore(t, disk)
	head := createCollectionFixture(t, s, declared)
	for i := 0; i < uploaded; i++ {
		item := collectionItemFixture(t, s, head, uint64(i+1), fmt.Sprintf("input-%d", i))
		result := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Second))
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		head = *result.Collection
	}
	view, protected, err := s.CollectionUploadView(context.Background(), head.ID, head.ActivityAt)
	if err != nil || !reflect.DeepEqual(head, protected) {
		t.Fatal("protected upload view", err)
	}
	return s, head, view
}

func uploadViewAssertions(t *testing.T, view *CollectionUploadView, at time.Time, expected error) {
	t.Helper()
	checks := []func() error{
		func() error { return view.Check(context.Background(), at) },
		func() error { _, err := view.Page(context.Background(), 0, 256, at); return err },
		func() error {
			_, _, err := view.Find(context.Background(), CatalogKey{Kind: "Credential", ID: "input-0"}, at)
			return err
		},
	}
	if view.view.header.Uploaded > 0 {
		checks = append(checks, func() error { _, err := view.Item(context.Background(), 1, at); return err })
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, expected) {
			t.Errorf("read %d: got %v want %v", i, err, expected)
		}
	}
}

func TestCollectionUploadViewZeroPrefixAndReadOnly(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s := collectionReadStore(t, disk)
			head := createCollectionFixture(t, s, 5)
			before, err := json.Marshal(s.fsm.image)
			if err != nil {
				t.Fatal(err)
			}
			var txid uint64
			if disk {
				txid = uint64(ledgerTestTransactionID(t, s.fsm.collections))
			}
			view, protected, err := s.CollectionUploadView(context.Background(), head.ID, head.ActivityAt)
			if err != nil || protected.Uploaded != 0 || protected.ItemCount != 5 {
				t.Fatal("zero prefix unavailable", err)
			}
			protected.Secret.Ciphertext[0] ^= 1
			if !bytes.Equal(view.view.header.Secret.Ciphertext, head.Secret.Ciphertext) {
				t.Fatal("protected header aliases view")
			}
			page, err := view.Page(context.Background(), 0, 256, head.ActivityAt)
			if err != nil || len(page) != 0 {
				t.Fatal("empty prefix page", err)
			}
			if _, found, err := view.Find(context.Background(), CatalogKey{Kind: "Credential", ID: "absent"}, head.ActivityAt); err != nil || found {
				t.Fatal("empty prefix requires nonexistent buckets", err)
			}
			if _, err := view.Item(context.Background(), 1, head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("unsent item treated as uploaded", err)
			}
			if _, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
				t.Fatal("upload view relaxed complete validation", err)
			}
			uploadViewAssertions(t, view, head.ExpiresAt, ErrOperationExpired)
			after, _ := json.Marshal(s.fsm.image)
			if !bytes.Equal(before, after) || disk && uint64(ledgerTestTransactionID(t, s.fsm.collections)) != txid {
				t.Fatal("constructor or read mutated durable state")
			}
			item := collectionItemFixture(t, s, head, 1, "first")
			result := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Second))
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			uploadViewAssertions(t, view, result.Collection.ActivityAt, ErrCollectionConflict)
			fresh, _, err := s.CollectionUploadView(context.Background(), head.ID, result.Collection.ActivityAt)
			if err != nil {
				t.Fatal("fresh prefix failed", err)
			}
			got, err := fresh.Item(context.Background(), 1, result.Collection.ActivityAt)
			if err != nil || !reflect.DeepEqual(got, item) {
				t.Fatal("new prefix changed original ciphertext", err)
			}
		})
	}
}

func TestCollectionUploadViewPartialPrefixIdentityAndRetry(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s, head, view := uploadViewFixture(t, disk, 4, 2)
			page, err := view.Page(context.Background(), 0, 256, head.ActivityAt)
			if err != nil || len(page) != 2 {
				t.Fatal("partial page included uncommitted suffix", len(page), err)
			}
			original := page[0].Clone()
			page[0].Payload.Ciphertext[0] ^= 1
			found, exists, err := view.Find(context.Background(), original.Key, head.ActivityAt)
			if err != nil || !exists || !reflect.DeepEqual(found, original) {
				t.Fatal("partial indexed lookup/ciphertext ownership", err)
			}
			for _, after := range []uint64{3, 4, 5} {
				if _, err := view.Page(context.Background(), after, 1, head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
					t.Fatal("page cursor exceeds captured prefix", after, err)
				}
			}
			if _, err := view.Item(context.Background(), 3, head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("unsent ordinal exposed", err)
			}
			if _, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
				t.Fatal("partial upload became validation input", err)
			}
			renewed := uploadCollectionFixture(t, s, head, original, head.ActivityAt.Add(time.Hour))
			if renewed.Err != nil {
				t.Fatal(renewed.Err)
			}
			uploadViewAssertions(t, view, head.ExpiresAt, nil)
			if !view.view.header.ActivityAt.Equal(head.ActivityAt) {
				t.Fatal("view mutated captured header on retry")
			}
			third := collectionItemFixture(t, s, head, 3, "third")
			changed := uploadCollectionFixture(t, s, *renewed.Collection, third, renewed.Collection.ActivityAt.Add(time.Second))
			if changed.Err != nil {
				t.Fatal(changed.Err)
			}
			uploadViewAssertions(t, view, changed.Collection.ActivityAt, ErrCollectionConflict)
			fresh, _, err := s.CollectionUploadView(context.Background(), head.ID, changed.Collection.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			got, err := fresh.Item(context.Background(), 1, changed.Collection.ActivityAt)
			if err != nil || !reflect.DeepEqual(got, original) {
				t.Fatal("fresh reconciliation changed retry ciphertext", err)
			}
		})
	}
}

func TestCollectionUploadViewZeroBucketCorruption(t *testing.T) {
	for _, corruption := range []string{"asymmetric", "stray-row", "stray-index", "sequence"} {
		t.Run(corruption, func(t *testing.T) {
			s, head, view := uploadViewFixture(t, true, 3, 0)
			if err := s.fsm.collections.db.Update(func(tx *bolt.Tx) error {
				rows, err := tx.Bucket(collectionLedgerRecords).CreateBucket([]byte(head.ID))
				if err != nil || corruption == "asymmetric" {
					return err
				}
				index, err := tx.Bucket(collectionLedgerKeys).CreateBucket([]byte(head.ID))
				if err != nil {
					return err
				}
				switch corruption {
				case "stray-row":
					return rows.Put(collectionOrdinal(1), []byte("unexpected"))
				case "stray-index":
					return index.Put([]byte("Credential/absent"), collectionOrdinal(1))
				default:
					return rows.SetSequence(1)
				}
			}); err != nil {
				t.Fatal(err)
			}
			uploadViewAssertions(t, view, head.ActivityAt, ErrCollectionUnavailable)
			if _, _, err := s.CollectionUploadView(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
				t.Fatal("corrupt empty prefix was reconstructed", err)
			}
		})
	}
}

func TestCollectionUploadViewInheritedFencesAndCancellation(t *testing.T) {
	for _, state := range []string{"canceled", "invalidated", "expired", "epoch", "auth-reset", "restore", "failed-store", "closed-ledger"} {
		t.Run(state, func(t *testing.T) {
			s, head, view := uploadViewFixture(t, false, 3, 1)
			want := error(ErrOperationExpired)
			s.fsm.mu.Lock()
			switch state {
			case "canceled", "invalidated", "expired":
				changed := s.fsm.image.Collections[head.ID]
				changed.Phase = state
				s.fsm.image.Collections[head.ID] = changed
			case "epoch":
				s.fsm.image.OperationEpoch = uuid.NewString()
			case "auth-reset":
				s.fsm.image.Authentication = &AuthenticationState{ResetRequired: true}
				want = ErrCollectionUnavailable
			case "restore":
				s.fsm.image.Restore = &RestoreState{Phase: "begin"}
				want = ErrCollectionUnavailable
			case "failed-store":
				s.fsm.err = errors.New("private storage error")
				want = ErrCollectionUnavailable
			case "closed-ledger":
				if err := s.fsm.collections.Close(); err != nil {
					t.Fatal(err)
				}
				want = ErrCollectionUnavailable
			}
			s.fsm.mu.Unlock()
			uploadViewAssertions(t, view, head.ActivityAt, want)
		})
	}
	s, head, view := uploadViewFixture(t, true, 3, 1)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.CollectionUploadView(canceled, head.ID, head.ActivityAt); !errors.Is(err, context.Canceled) {
		t.Fatal("constructor ignored cancellation", err)
	}
	if _, err := view.Page(canceled, 0, 2, head.ActivityAt); !errors.Is(err, context.Canceled) {
		t.Fatal("page ignored cancellation", err)
	}
	s.fsm.mu.Lock()
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := view.Check(ctx, head.ActivityAt)
	stop()
	s.fsm.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock wait ignored cancellation", err)
	}
	var readers sync.WaitGroup
	for range 12 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 20 {
				if _, err := view.Item(context.Background(), 1, head.ActivityAt); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	readers.Wait()
	if s.fsm.collections.db.Stats().OpenTxN != 0 {
		t.Fatal("partial reads retain open bbolt transactions")
	}
}

func TestCollectionUploadViewCommittedCancellationAndBadIndex(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s, head, view := uploadViewFixture(t, disk, 3, 1)
			at := head.ActivityAt.Add(time.Second)
			cancel := CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID,
				Cancel: &CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}}
			result := collectionCommand(t, s, cancel, at)
			if result.Err != nil || result.Collection == nil || result.Collection.Phase != "canceled" {
				t.Fatal("cancel command failed", result.Err)
			}
			// A previously valid timestamp cannot reopen an explicitly canceled
			// operation, and a fresh constructor cannot bypass the terminal fence.
			uploadViewAssertions(t, view, head.ActivityAt, ErrOperationExpired)
			if _, _, err := s.CollectionUploadView(context.Background(), head.ID, at); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("canceled input reopened", err)
			}
		})
		t.Run(fmt.Sprintf("index-disk=%t", disk), func(t *testing.T) {
			s, head, view := uploadViewFixture(t, disk, 3, 1)
			key := CatalogKey{Kind: "Credential", ID: "input-0"}
			if disk {
				if err := s.fsm.collections.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket(collectionLedgerKeys).Bucket([]byte(head.ID)).Put([]byte(key.indexKey()), collectionOrdinal(2))
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				s.fsm.collections.mu.Lock()
				s.fsm.collections.keys[head.ID][key] = 2
				s.fsm.collections.mu.Unlock()
			}
			if _, _, err := view.Find(context.Background(), key, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
				t.Fatal("index pointed beyond captured prefix", err)
			}
			if _, err := view.Item(context.Background(), 1, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
				t.Fatal("partial item bypassed index consistency", err)
			}
		})
	}
}

func TestCollectionReadViewRejectsOversizedOrEmptyExpectedRow(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, size := range []int{0, collectionLedgerPageBytes + 1} {
			t.Run(fmt.Sprintf("partial=%t/bytes=%d", partial, size), func(t *testing.T) {
				declared := uint64(1)
				if partial {
					declared = 2
				}
				s, head, upload := uploadViewFixture(t, true, declared, 1)
				var view interface {
					Page(context.Context, uint64, int, time.Time) ([]CollectionItem, error)
					Item(context.Context, uint64, time.Time) (CollectionItem, error)
					Find(context.Context, CatalogKey, time.Time) (CollectionItem, bool, error)
				} = upload
				if !partial {
					complete, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt)
					if err != nil {
						t.Fatal(err)
					}
					view = complete
				}
				// Preserve header, bucket sequence and identity index so this
				// reaches the expected row rather than an earlier totals fence.
				if err := s.fsm.collections.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket(collectionLedgerRecords).Bucket([]byte(head.ID)).Put(collectionOrdinal(1), bytes.Repeat([]byte{'x'}, size))
				}); err != nil {
					t.Fatal(err)
				}
				if page, err := view.Page(context.Background(), 0, 256, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) || len(page) != 0 {
					t.Fatal("corrupt expected row became an empty successful page", err)
				}
				if _, err := view.Item(context.Background(), 1, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
					t.Fatal("corrupt expected row was accepted", err)
				}
				if _, _, err := view.Find(context.Background(), CatalogKey{Kind: "Credential", ID: "input-0"}, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
					t.Fatal("corrupt indexed row was accepted or copied without a bound", err)
				}
			})
		}
	}
}
