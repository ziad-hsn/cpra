package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func collectionReadStore(t *testing.T, disk bool) *Store {
	t.Helper()
	s := openCatalogMemory(t)
	if disk {
		ledger, err := openCollectionLedger(filepath.Join(t.TempDir(), "collections"), maxCollectionLedgerBytes)
		if err != nil {
			t.Fatal(err)
		}
		s.fsm.mu.Lock()
		s.fsm.collections = ledger
		s.fsm.mu.Unlock()
	}
	return s
}

func collectionReadFixture(t *testing.T, disk bool, count int) (*Store, CollectionState, *CollectionValidationView) {
	t.Helper()
	s := collectionReadStore(t, disk)
	head := collectionFill(t, s, count)
	v, got, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt)
	if err != nil || !reflect.DeepEqual(got, head) {
		t.Fatal("completed collection view", err)
	}
	return s, head, v
}

func collectionReadAssertions(t *testing.T, v *CollectionValidationView, at time.Time, want error) {
	t.Helper()
	checks := []struct {
		name string
		run  func() error
	}{
		{"check", func() error { return v.Check(context.Background(), at) }},
		{"page", func() error { _, err := v.Page(context.Background(), 0, 256, at); return err }},
		{"item", func() error { _, err := v.Item(context.Background(), 1, at); return err }},
		{"find", func() error {
			_, _, err := v.Find(context.Background(), CatalogKey{Kind: "Credential", ID: "item-0000"}, at)
			return err
		}},
	}
	for _, check := range checks {
		if err := check.run(); !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", check.name, err, want)
		}
	}
}

func TestCollectionValidationViewCompleteOnlyAndBoundedCount(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s := collectionReadStore(t, disk)
			head := createCollectionFixture(t, s, 2)
			if _, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
				t.Fatal("empty upload admitted", err)
			}
			item := collectionItemFixture(t, s, head, 1, "first")
			if r := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Second)); r.Err != nil {
				t.Fatal(r.Err)
			}
			if _, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt.Add(time.Second)); !errors.Is(err, ErrCollectionConflict) {
				t.Fatal("partial upload admitted", err)
			}
			head = collectionFill(t, s, 600)
			v, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			var after uint64
			for _, count := range []int{256, 256, 88, 0} {
				page, err := v.Page(context.Background(), after, 256, head.ActivityAt)
				if err != nil || len(page) != count {
					t.Fatal("bounded page", len(page), count, err)
				}
				for _, row := range page {
					after++
					if row.Ordinal != after {
						t.Fatal("gap or duplicate", row.Ordinal, after)
					}
				}
			}
			for _, limit := range []int{0, 257} {
				if _, err := v.Page(context.Background(), 0, limit, head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
					t.Fatal("invalid page limit", err)
				}
			}
			if _, err := v.Page(context.Background(), 601, 1, head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("invalid cursor", err)
			}
			for _, ordinal := range []uint64{0, 601} {
				if _, err := v.Item(context.Background(), ordinal, head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
					t.Fatal("invalid ordinal", err)
				}
			}
			if disk && s.fsm.collections.db.Stats().OpenTxN != 0 {
				t.Fatal("read transactions retained between calls")
			}
		})
	}
}

func TestCollectionValidationViewBoundsEncodedBytesAndDetachesPayloads(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s := collectionReadStore(t, disk)
			head := createCollectionFixture(t, s, 4)
			for n := uint64(1); n <= 4; n++ {
				item := ledgerTestItem(n)
				item.Payload.Ciphertext = bytes.Repeat([]byte{42}, 1<<20)
				r := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Second))
				if r.Err != nil {
					t.Fatal(r.Err)
				}
				head = *r.Collection
			}
			v, protected, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			protected.Secret.Ciphertext[0] ^= 1
			if !reflect.DeepEqual(v.header.Secret, head.Secret) {
				t.Fatal("protected header aliases view")
			}
			stored, _, _ := s.CollectionGet(head.ID)
			if !reflect.DeepEqual(stored.Secret, head.Secret) {
				t.Fatal("protected header aliases durable state")
			}
			var after uint64
			for range 2 {
				page, err := v.Page(context.Background(), after, 256, head.ActivityAt)
				if err != nil || len(page) != 2 {
					t.Fatal("byte-limited page", len(page), err)
				}
				var size int64
				for _, item := range page {
					cost, _ := collectionItemCost(head.ID, item)
					size += cost
					after++
					if item.Ordinal != after {
						t.Fatal("byte pages skipped input")
					}
				}
				if size > collectionLedgerPageBytes {
					t.Fatal("page exceeded encoded-byte bound")
				}
				page[0].Payload.Ciphertext[0] = 99
				fresh, err := v.Item(context.Background(), page[0].Ordinal, head.ActivityAt)
				if err != nil || fresh.Payload.Ciphertext[0] != 42 {
					t.Fatal("returned payload aliases ledger", err)
				}
			}
			found, exists, err := v.Find(context.Background(), ledgerTestItem(3).Key, head.ActivityAt)
			if err != nil || !exists || found.Ordinal != 3 {
				t.Fatal("indexed lookup", err)
			}
			found.Payload.Ciphertext[0] = 99
			again, _, _ := v.Find(context.Background(), found.Key, head.ActivityAt)
			if again.Payload.Ciphertext[0] != 42 {
				t.Fatal("Find aliases ledger")
			}
			if _, exists, err := v.Find(context.Background(), CatalogKey{Kind: "Monitor", ID: "absent"}, head.ActivityAt); err != nil || exists {
				t.Fatal("absent identity became an error", err)
			}
		})
	}
}

func TestCollectionValidationViewFreshTimeRenewalAndNoReadEffects(t *testing.T) {
	s, head, v := collectionReadFixture(t, true, 2)
	capture := func() []byte {
		s.fsm.mu.RLock()
		defer s.fsm.mu.RUnlock()
		data, err := json.Marshal(s.fsm.image)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := capture()
	tx := ledgerTestTransactionID(t, s.fsm.collections)
	collectionReadAssertions(t, v, head.ExpiresAt.Add(-time.Nanosecond), nil)
	collectionReadAssertions(t, v, head.ExpiresAt, ErrOperationExpired)
	after := capture()
	if !bytes.Equal(before, after) || tx != ledgerTestTransactionID(t, s.fsm.collections) {
		t.Fatal("reads changed state, activity or ledger")
	}
	item, err := v.Item(context.Background(), 1, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	renewed := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Hour))
	if renewed.Err != nil {
		t.Fatal(renewed.Err)
	}
	// Exactly the same contents remain usable through the old deadline. The
	// original protected header does not freeze the live inactivity deadline.
	collectionReadAssertions(t, v, head.ExpiresAt, nil)
	collectionReadAssertions(t, v, renewed.Collection.ExpiresAt, ErrOperationExpired)
	if len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 || len(s.fsm.image.OperationReservations) != 0 {
		t.Fatal("validation reads admitted active work")
	}
	if r := collectionCommand(t, s, cleanupCommand(*renewed.Collection), renewed.Collection.ExpiresAt); r.Err != nil {
		t.Fatal(r.Err)
	}
	// Retirement wins even if a caller supplies a previously valid instant.
	collectionReadAssertions(t, v, head.ActivityAt, ErrOperationExpired)
}

func TestCollectionValidationViewFencesCurrentIdentityAndAvailability(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*machine, string)
		want   error
	}{
		{"epoch", func(f *machine, _ string) { f.image.OperationEpoch = uuid.NewString() }, ErrOperationExpired},
		{"invalidated", func(f *machine, id string) {
			h := f.image.Collections[id]
			h.Phase = "invalidated"
			f.image.Collections[id] = h
		}, ErrOperationExpired},
		{"bootstrap", func(f *machine, _ string) { f.image.Bootstrap = &BootstrapState{Phase: "loading"} }, ErrCollectionUnavailable},
		{"restore", func(f *machine, _ string) { f.image.Restore = &RestoreState{Phase: "begin"} }, ErrCollectionUnavailable},
		{"authentication reset", func(f *machine, _ string) { f.image.Authentication = &AuthenticationState{ResetRequired: true} }, ErrCollectionUnavailable},
		{"failed storage", func(f *machine, _ string) { f.err = errors.New("sensitive-path-or-provider-message") }, ErrCollectionUnavailable},
		{"embedded identity", func(f *machine, id string) {
			h := f.image.Collections[id]
			h.ID = operationHandle(f.image.OperationEpoch, 99)
			f.image.Collections[id] = h
		}, ErrCollectionUnavailable},
		{"upload identity", func(f *machine, id string) {
			h := f.image.Collections[id]
			h.UploadID = uuid.NewString()
			f.image.Collections[id] = h
		}, ErrCollectionConflict},
		{"content identity", func(f *machine, id string) {
			h := f.image.Collections[id]
			h.ContentDigest = strings.Repeat("c", 64)
			f.image.Collections[id] = h
		}, ErrCollectionConflict},
		{"inventory identity", func(f *machine, id string) {
			h := f.image.Collections[id]
			h.ProgressDigest = strings.Repeat("d", 64)
			f.image.Collections[id] = h
		}, ErrCollectionConflict},
		{"count", func(f *machine, id string) {
			h := f.image.Collections[id]
			h.ItemCount++
			h.Uploaded++
			f.image.Collections[id] = h
		}, ErrCollectionConflict},
		{"partial", func(f *machine, id string) { h := f.image.Collections[id]; h.Uploaded--; f.image.Collections[id] = h }, ErrCollectionConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, head, v := collectionReadFixture(t, false, 2)
			s.fsm.mu.Lock()
			test.mutate(s.fsm, head.ID)
			s.fsm.mu.Unlock()
			collectionReadAssertions(t, v, head.ActivityAt, test.want)
		})
	}
}

func TestCollectionValidationViewMissingRowsAndIndexCorruptionUnavailable(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, kind := range []string{"missing-middle", "bad-index", "bad-row", "wrong-count", "closed"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, kind), func(t *testing.T) {
				s, head, v := collectionReadFixture(t, disk, 3)
				l := s.fsm.collections
				key := CatalogKey{Kind: "Credential", ID: "item-0001"}
				if kind == "closed" {
					if err := l.Close(); err != nil {
						t.Fatal(err)
					}
				} else if disk {
					if err := l.db.Update(func(tx *bolt.Tx) error {
						rows := tx.Bucket(collectionLedgerRecords).Bucket([]byte(head.ID))
						keys := tx.Bucket(collectionLedgerKeys).Bucket([]byte(head.ID))
						switch kind {
						case "missing-middle":
							return rows.Delete(collectionOrdinal(2))
						case "bad-index":
							return keys.Put([]byte(key.indexKey()), collectionOrdinal(1))
						case "bad-row":
							return rows.Put(collectionOrdinal(2), []byte("invalid ciphertext row"))
						case "wrong-count":
							return rows.SetSequence(2)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				} else {
					l.mu.Lock()
					switch kind {
					case "missing-middle":
						delete(l.rows[head.ID], 2)
					case "bad-index":
						l.keys[head.ID][key] = 1
					case "bad-row":
						l.rows[head.ID][2] = []byte("invalid ciphertext row")
					case "wrong-count":
						l.operationBytes[head.ID]--
					}
					l.mu.Unlock()
				}
				if _, err := v.Page(context.Background(), 0, 3, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
					t.Fatal("page silently truncated or accepted damaged index", err)
				}
				if _, err := v.Item(context.Background(), 2, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
					t.Fatal("exact ordinal hid damaged input", err)
				}
				if _, _, err := v.Find(context.Background(), key, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
					t.Fatal("index hid damaged input", err)
				}
			})
		}
	}
}

func TestCollectionValidationViewCancellationAndConcurrentReads(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s, head, v := collectionReadFixture(t, disk, 3)
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, _, err := s.CollectionValidationView(canceled, head.ID, head.ActivityAt); !errors.Is(err, context.Canceled) {
				t.Fatal("constructor ignored cancellation", err)
			}
			if _, err := v.Page(canceled, 0, 3, head.ActivityAt); !errors.Is(err, context.Canceled) {
				t.Fatal("page ignored cancellation", err)
			}
			if _, err := v.Item(canceled, 1, head.ActivityAt); !errors.Is(err, context.Canceled) {
				t.Fatal("item ignored cancellation", err)
			}
			if _, _, err := v.Find(canceled, CatalogKey{Kind: "Credential", ID: "item-0000"}, head.ActivityAt); !errors.Is(err, context.Canceled) {
				t.Fatal("Find ignored cancellation", err)
			}
			for _, lock := range []*sync.RWMutex{&s.fsm.mu, &s.fsm.collections.mu} {
				lock.Lock()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				err := v.Check(ctx, head.ActivityAt)
				cancel()
				lock.Unlock()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("locked read ignored context", err)
				}
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					for range 8 {
						page, err := v.Page(context.Background(), 0, 3, head.ActivityAt)
						if err != nil || len(page) != 3 {
							t.Error("concurrent read", err)
							return
						}
						page[0].Payload.Ciphertext[0] ^= 1
					}
				})
			}
			wg.Wait()
			if err := v.Check(context.Background(), head.ActivityAt); err != nil {
				t.Fatal("canceled read leaked locks", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			collectionReadAssertions(t, v, head.ActivityAt, ErrCollectionUnavailable)
		})
	}
}

func TestCollectionValidationViewRaftRestartAndExplicitRestore(t *testing.T) {
	config := testConfig(t)
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	head := collectionFill(t, s, 3)
	v, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	collectionReadAssertions(t, v, head.ActivityAt, ErrCollectionUnavailable)
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	recovered, protected, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt)
	if err != nil || !reflect.DeepEqual(protected, head) {
		t.Fatal("view after Raft recovery", err)
	}
	collectionReadAssertions(t, recovered, head.ActivityAt, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MarkRestored(config.Storage.Directory, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	s, err = OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("explicit restore authentication fence bypassed", err)
	}
	if s.fsm.image.Collections[head.ID].Phase != "invalidated" || s.fsm.image.OperationEpoch == v.store.fsm.image.OperationEpoch {
		t.Fatal("explicit restore did not fence original collection")
	}
}
