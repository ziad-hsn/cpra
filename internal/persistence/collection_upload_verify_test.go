package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func TestCollectionUploadVerifyPrefixBoundedPagesAndReadOnly(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, count := range []int{0, 1, collectionLedgerBatchLimit + 1} {
			t.Run(fmt.Sprintf("disk=%t/count=%d", disk, count), func(t *testing.T) {
				s, head, view := uploadViewFixture(t, disk, uint64(count+1), count)
				before, err := json.Marshal(s.fsm.image)
				if err != nil {
					t.Fatal(err)
				}
				var txid int
				if disk {
					txid = ledgerTestTransactionID(t, s.fsm.collections)
				}
				if err := view.VerifyPrefix(context.Background(), head.ActivityAt); err != nil {
					t.Fatal("original prefix rejected", err)
				}
				after, err := json.Marshal(s.fsm.image)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("verification changed durable state", err)
				}
				if disk && (ledgerTestTransactionID(t, s.fsm.collections) != txid || s.fsm.collections.db.Stats().OpenTxN != 0) {
					t.Fatal("verification wrote ledger or retained a read transaction")
				}
			})
		}
	}
}

func TestCollectionUploadVerifyPrefixRejectsEquivalentReencryption(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s, head, view := uploadViewFixture(t, disk, 3, 2)
			original, err := view.Item(context.Background(), 1, head.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			sealer := catalogSealer(t)
			plain, err := sealer.Open(context.Background(), original.Binding(s.nodeID, head.UploadID), original.Payload)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(plain)
			replacement := original.Clone()
			replacement.Payload, err = sealer.Seal(context.Background(), original.Binding(s.nodeID, head.UploadID), plain)
			if err != nil {
				t.Fatal(err)
			}
			oldRaw, err := collectionItemEncoding(head.ID, original)
			if err != nil {
				t.Fatal(err)
			}
			newRaw, err := collectionItemEncoding(head.ID, replacement)
			if err != nil || len(oldRaw) != len(newRaw) || bytes.Equal(oldRaw, newRaw) {
				t.Fatal("fixture must replace ciphertext without changing length", err)
			}
			// Keep the immutable header, identity index and accounting intact.
			// AEAD, item MAC and plaintext equality alone all accept this row.
			if disk {
				err = s.fsm.collections.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket(collectionLedgerRecords).Bucket([]byte(head.ID)).Put(collectionOrdinal(1), newRaw)
				})
			} else {
				s.fsm.collections.mu.Lock()
				s.fsm.collections.rows[head.ID][1] = newRaw
				s.fsm.collections.mu.Unlock()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := view.Item(context.Background(), 1, head.ActivityAt); err != nil {
				t.Fatal("fixture did not reach ciphertext commitment check", err)
			}
			if err := view.VerifyPrefix(context.Background(), head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
				t.Fatal("different original ciphertext accepted", err)
			}
		})
	}
}

func TestCollectionUploadVerifyPrefixRejectsIncorrectEncodedCost(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s, head, _ := uploadViewFixture(t, disk, 2, 1)
			s.fsm.mu.Lock()
			changed := s.fsm.image.Collections[head.ID]
			changed.EncodedBytes++
			s.fsm.image.Collections[head.ID] = changed
			s.fsm.mu.Unlock()
			if disk {
				if err := s.fsm.collections.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket(collectionLedgerKeys).Bucket([]byte(head.ID)).SetSequence(uint64(changed.EncodedBytes))
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				s.fsm.collections.mu.Lock()
				s.fsm.collections.operationBytes[head.ID]++
				s.fsm.collections.mu.Unlock()
			}
			view, _, err := s.CollectionUploadView(context.Background(), head.ID, head.ActivityAt)
			if err != nil {
				t.Fatal("fixture did not reach recomputed encoded cost", err)
			}
			if err := view.VerifyPrefix(context.Background(), head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
				t.Fatal("incorrect stored cost accepted", err)
			}
		})
	}
}

func TestCollectionUploadVerifyPrefixFencesAndCancellation(t *testing.T) {
	for _, state := range []string{"advanced", "canceled", "epoch", "expired", "cancellation", "blocked-lock"} {
		t.Run(state, func(t *testing.T) {
			s, head, view := uploadViewFixture(t, false, 3, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			at := head.ActivityAt
			want := error(ErrOperationExpired)
			switch state {
			case "advanced":
				item := collectionItemFixture(t, s, head, 2, "second")
				result := uploadCollectionFixture(t, s, head, item, at.Add(time.Second))
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				want = ErrCollectionConflict
			case "canceled", "epoch":
				s.fsm.mu.Lock()
				if state == "epoch" {
					s.fsm.image.OperationEpoch = uuid.NewString()
				} else {
					changed := s.fsm.image.Collections[head.ID]
					changed.Phase = "canceled"
					s.fsm.image.Collections[head.ID] = changed
				}
				s.fsm.mu.Unlock()
			case "expired":
				at = head.ExpiresAt
			case "cancellation":
				cancel()
				want = context.Canceled
			case "blocked-lock":
				s.fsm.mu.Lock()
				defer s.fsm.mu.Unlock()
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
				want = context.DeadlineExceeded
			}
			if err := view.VerifyPrefix(ctx, at); !errors.Is(err, want) {
				t.Fatal("verification ignored fence", state, err)
			}
		})
	}
}

// The empty prefix performs one initial metadata/ledger check (six Err calls).
// Gate the next read-lock acquisition so the final check waits across expiry.
// This exercises the boundary after proof completion, not just initial expiry.
type uploadVerifyFinalLockContext struct {
	context.Context
	calls   int
	lock    *sync.RWMutex
	waiting chan struct{}
}

func (c *uploadVerifyFinalLockContext) Err() error {
	c.calls++
	if c.calls == 7 {
		c.lock.Lock()
		close(c.waiting)
	}
	return c.Context.Err()
}
func TestCollectionUploadVerifyPrefixExpiryDuringFinalLock(t *testing.T) {
	s, head, view := uploadViewFixture(t, false, 1, 0)
	ctx := &uploadVerifyFinalLockContext{Context: context.Background(), lock: &s.fsm.mu, waiting: make(chan struct{})}
	released := make(chan struct{})
	go func() {
		<-ctx.waiting
		time.Sleep(60 * time.Millisecond)
		s.fsm.mu.Unlock()
		close(released)
	}()
	err := view.VerifyPrefix(ctx, head.ExpiresAt.Add(-25*time.Millisecond))
	<-released
	if !errors.Is(err, ErrOperationExpired) {
		t.Fatal("final proof accepted time sampled before lock wait", err)
	}
}
