package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func uploadAppendFixture(t *testing.T, disk bool, prefix int) (*Store, CollectionState, CollectionState, CollectionItem, *CollectionUploadView) {
	t.Helper()
	s, prior, original := uploadViewFixture(t, disk, uint64(prefix+3), prefix)
	if err := original.VerifyPrefix(t.Context(), prior.ActivityAt); err != nil {
		t.Fatal(err)
	}
	pending := collectionItemFixture(t, s, prior, prior.Uploaded+1, "appended")
	head := validationApplyAllowed(t, uploadCollectionFixture(t, s, prior, pending, prior.ActivityAt.Add(time.Second)))
	view, _, err := s.CollectionUploadView(t.Context(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	return s, prior, head, pending, view
}

func TestCollectionUploadVerifyAppendReadOnlyAndConstantReads(t *testing.T) {
	for _, disk := range []bool{false, true} {
		var firstReads int64
		for _, prefix := range []int{0, collectionLedgerBatchLimit + 1} {
			t.Run(fmt.Sprintf("disk=%t/prefix=%d", disk, prefix), func(t *testing.T) {
				s, prior, head, pending, view := uploadAppendFixture(t, disk, prefix)
				// An exact ciphertext retry can renew activity after capture.
				result := uploadCollectionFixture(t, s, head, pending, head.ActivityAt.Add(time.Second))
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				before, _ := json.Marshal(s.fsm.image)
				var txid int
				var stats bolt.Stats
				if disk {
					txid = ledgerTestTransactionID(t, s.fsm.collections)
					stats = s.fsm.collections.db.Stats()
				}
				if err := view.VerifyAppend(t.Context(), prior, pending, result.Collection.ActivityAt); err != nil {
					t.Fatal("original one-row append rejected", err)
				}
				after, _ := json.Marshal(s.fsm.image)
				if !bytes.Equal(before, after) {
					t.Fatal("verification mutated state")
				}
				if disk {
					afterStats := s.fsm.collections.db.Stats()
					reads := afterStats.TxStats.GetCursorCount() - stats.TxStats.GetCursorCount()
					if prefix == 0 {
						firstReads = reads
					} else if reads != firstReads {
						t.Fatal("read work grew with the original prefix", firstReads, reads)
					}
					if ledgerTestTransactionID(t, s.fsm.collections) != txid || afterStats.OpenTxN != 0 {
						t.Fatal("verification wrote storage or retained a transaction")
					}
				}
			})
		}
	}
}

func TestCollectionUploadVerifyAppendImmutableIdentity(t *testing.T) {
	s := openCatalogMemory(t)
	prior := reselectionCreate(t, s, "cpra.file.base.v1", 3)
	command := reselectionUpload(t, s, prior, "first")
	head := validationApplyAllowed(t, collectionCommand(t, s, command, prior.ActivityAt.Add(time.Second)))
	view, _, err := s.CollectionUploadView(t.Context(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := view.VerifyAppend(t.Context(), prior, *command.Item, head.ActivityAt); err != nil {
		t.Fatal("named owner/profile/admission append rejected", err)
	}
	mutations := map[string]func(*CollectionState){
		"operation":        func(h *CollectionState) { h.ID = head.ID[:len(head.ID)-1] + "9" },
		"upload":           func(h *CollectionState) { h.UploadID = uuid.NewString() },
		"actor":            func(h *CollectionState) { h.Actor, h.Owner.Actor = "other", "other" },
		"owner":            func(h *CollectionState) { h.Owner.Revision = uuid.NewString() },
		"owner-absent":     func(h *CollectionState) { h.Owner = nil },
		"profile":          func(h *CollectionState) { h.NormalizationProfile = "" },
		"content":          func(h *CollectionState) { h.ContentDigest = strings.Repeat("c", 64) },
		"count":            func(h *CollectionState) { h.ItemCount++ },
		"limit":            func(h *CollectionState) { h.MaxEncodedBytes-- },
		"created":          func(h *CollectionState) { h.CreatedAt = h.CreatedAt.Add(-time.Second) },
		"secret":           func(h *CollectionState) { h.Secret.Ciphertext[0] ^= 1 },
		"admission":        func(h *CollectionState) { h.Admission.RequestDigest = strings.Repeat("d", 64) },
		"admission-expiry": func(h *CollectionState) { h.Admission.ExpiresAt = h.Admission.ExpiresAt.Add(time.Second) },
		"admission-absent": func(h *CollectionState) { h.Admission = nil },
		"activity-regressed": func(h *CollectionState) {
			h.ActivityAt = head.ActivityAt.Add(time.Second)
			h.ExpiresAt = h.ActivityAt.Add(CollectionInactivityLifetime)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := prior.Clone()
			mutate(&changed)
			if err := changed.validate(); err != nil {
				t.Fatal("fixture must reach immutable identity comparison", err)
			}
			if err := view.VerifyAppend(t.Context(), changed, *command.Item, head.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
				t.Fatal("changed original identity accepted", err)
			}
		})
	}
	// Current metadata must remain bound even when the basic captured view's
	// progress fields are unchanged. This is not a request to change authority.
	s.fsm.mu.Lock()
	changed := s.fsm.image.Collections[head.ID].Clone()
	changed.Owner.Revision = uuid.NewString()
	s.fsm.image.Collections[head.ID] = changed
	s.fsm.mu.Unlock()
	if err := view.VerifyAppend(t.Context(), prior, *command.Item, head.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("changed live owner passed the metadata fence", err)
	}
}

func TestCollectionUploadVerifyAppendRejectsStoredCorruption(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, fault := range []string{"ciphertext", "missing", "extra", "index", "noncanonical"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, fault), func(t *testing.T) {
				s, prior, head, pending, view := uploadAppendFixture(t, disk, 1)
				changed := pending.Clone()
				changed.Payload.Ciphertext[0] ^= 1
				data, err := collectionItemEncoding(head.ID, changed)
				if err != nil {
					t.Fatal(err)
				}
				if fault == "noncanonical" {
					data, _ = collectionItemEncoding(head.ID, pending)
					data = append(data, ' ')
				}
				if disk {
					err = s.fsm.collections.db.Update(func(tx *bolt.Tx) error {
						rows := tx.Bucket(collectionLedgerRecords).Bucket([]byte(head.ID))
						switch fault {
						case "missing":
							return rows.Delete(collectionOrdinal(pending.Ordinal))
						case "extra":
							return rows.Put(collectionOrdinal(pending.Ordinal+1), data)
						case "index":
							return tx.Bucket(collectionLedgerKeys).Bucket([]byte(head.ID)).Delete([]byte(pending.Key.indexKey()))
						default:
							return rows.Put(collectionOrdinal(pending.Ordinal), data)
						}
					})
				} else {
					ledger := s.fsm.collections
					ledger.mu.Lock()
					switch fault {
					case "missing":
						delete(ledger.rows[head.ID], pending.Ordinal)
					case "extra":
						ledger.rows[head.ID][pending.Ordinal+1] = data
					case "index":
						delete(ledger.keys[head.ID], pending.Key)
					default:
						ledger.rows[head.ID][pending.Ordinal] = data
					}
					ledger.mu.Unlock()
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := view.VerifyAppend(t.Context(), prior, pending, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
					t.Fatal("stored corruption accepted", err)
				}
			})
		}
	}
}

func TestCollectionUploadVerifyAppendExactPositionAndCommitment(t *testing.T) {
	s, prior, head, pending, view := uploadAppendFixture(t, false, 1)
	for _, fault := range []string{"same-position", "extra-position", "wrong-ordinal", "pending-ciphertext", "prior-digest", "prior-bytes"} {
		t.Run(fault, func(t *testing.T) {
			before, item := prior.Clone(), pending.Clone()
			want := error(ErrCollectionConflict)
			switch fault {
			case "same-position":
				before = head.Clone()
			case "extra-position":
				before.Uploaded, before.EncodedBytes, before.ProgressDigest = 0, 0, collectionInitialDigest()
			case "wrong-ordinal":
				item.Ordinal++
			case "pending-ciphertext":
				item.Payload.Ciphertext[0] ^= 1
				want = ErrCollectionUnavailable
			case "prior-digest":
				before.ProgressDigest = strings.Repeat("e", 64)
				want = ErrCollectionUnavailable
			case "prior-bytes":
				before.EncodedBytes++
				want = ErrCollectionUnavailable
			}
			if err := view.VerifyAppend(t.Context(), before, item, head.ActivityAt); !errors.Is(err, want) {
				t.Fatal("inexact append accepted", err)
			}
		})
	}
	next := collectionItemFixture(t, s, head, head.Uploaded+1, "advanced")
	advanced := validationApplyAllowed(t, uploadCollectionFixture(t, s, head, next, head.ActivityAt.Add(time.Second)))
	if err := view.VerifyAppend(t.Context(), prior, pending, advanced.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("advanced live view accepted", err)
	}
}

func TestCollectionUploadVerifyAppendFencesAndCancellation(t *testing.T) {
	for _, fence := range []string{"canceled", "restore", "expired", "context", "fsm-lock", "ledger-lock", "expiry-during-lock"} {
		t.Run(fence, func(t *testing.T) {
			s, prior, head, pending, view := uploadAppendFixture(t, false, 0)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			at, want := head.ActivityAt, error(ErrOperationExpired)
			switch fence {
			case "canceled", "restore":
				s.fsm.mu.Lock()
				if fence == "restore" {
					s.fsm.image.OperationEpoch = uuid.NewString()
				} else {
					changed := s.fsm.image.Collections[head.ID]
					changed.Phase = "canceled"
					s.fsm.image.Collections[head.ID] = changed
				}
				s.fsm.mu.Unlock()
			case "expired":
				at = head.ExpiresAt
			case "context":
				cancel()
				want = context.Canceled
			case "fsm-lock", "ledger-lock":
				if fence == "fsm-lock" {
					s.fsm.mu.Lock()
					defer s.fsm.mu.Unlock()
				} else {
					s.fsm.collections.mu.Lock()
					defer s.fsm.collections.mu.Unlock()
				}
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
				want = context.DeadlineExceeded
			case "expiry-during-lock":
				s.fsm.collections.mu.Lock()
				at = head.ExpiresAt.Add(-20 * time.Millisecond)
				result := make(chan error, 1)
				go func() { result <- view.VerifyAppend(ctx, prior, pending, at) }()
				// The ledger wait keeps the pre-read timestamp on the live side
				// of expiry. The final fence must evaluate elapsed time again.
				time.Sleep(50 * time.Millisecond)
				s.fsm.collections.mu.Unlock()
				if err := <-result; !errors.Is(err, ErrOperationExpired) {
					t.Fatal("expiry while blocked on the row read was ignored", err)
				}
				return
			}
			if err := view.VerifyAppend(ctx, prior, pending, at); !errors.Is(err, want) {
				t.Fatal("append verification ignored fence", err)
			}
		})
	}
}
