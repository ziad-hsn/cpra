package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// All source/plan/result/admission and execution transitions use actual Store
// commands. Candidate payloads are inert test records, not management output.
func preparationCacheFixture(t *testing.T, disk bool, count int) (*Store, CollectionState, CollectionExecuteCommand) {
	t.Helper()
	var s *Store
	if disk {
		config := testConfig(t)
		admin := openAuthenticationAdmin(t, config)
		if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
			t.Fatal(err)
		}
		if err := admin.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		s, err = Open(context.Background(), config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
	} else {
		s = openCatalogMemory(t)
	}
	head, _ := validationPublishFixture(t, s, count, true)
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	}
	auth, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, auth, at), at))
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	c := CollectionExecuteCommand{Action: "begin", Binding: binding, Authority: auth, CapabilitiesDigest: head.Activation.CapabilitiesDigest}
	head = validationApplyAllowed(t, executeStoreCommand(t, s, c, at.Add(time.Second)))
	return s, head, c
}

func preparationCacheAccept(t *testing.T, s *Store, head CollectionState, c CollectionExecuteCommand) (CollectionState, CollectionItemOutcome) {
	t.Helper()
	x := s.fsm.collectionExecutionIndex
	at := head.Execution.LastAt.Add(time.Second)
	prepared := executeCandidate(t, c, x, head.Execution.Processed+1, at)
	head = validationApplyAllowed(t, executeStoreCommand(t, s, prepared, at))
	head = validationApplyAllowed(t, executeStoreCommand(t, s, executeDecision(prepared), at.Add(time.Second)))
	r, found, err := s.fsm.collections.ExecutionRecord(head.ID, collectionExecutionOutcomeSlot(head.Execution.Processed))
	if err != nil || !found || r.Outcome == nil || r.Outcome.Decision != "accepted" {
		t.Fatal("missing original accepted item", err)
	}
	return head, *r.Outcome
}

func TestCollectionExecutionPreparationCacheSelectedCiphertextHash(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head, c := preparationCacheFixture(t, disk, 2)
			x := s.fsm.collectionExecutionIndex
			v := openExecutionPreparationView(t, s, head, c.Authority, head.Execution.LastAt)
			if !v.cachedIndex || v.index != nil || v.ledger != nil {
				t.Fatal("matching cache not reused or reader retained")
			}
			original, err := v.Input(context.Background(), 1, head.Execution.LastAt)
			if err != nil {
				t.Fatal(err)
			}
			_ = v.Close()
			raw, err := collectionItemEncoding(head.ID, original)
			if err != nil || x.rows[0].InputFrameDigest != sha256.Sum256(raw) {
				t.Fatal("index did not bind original encrypted wrapper", err)
			}
			changed := original.Clone()
			changed.Payload.Ciphertext[0] ^= 1
			mutated, err := collectionItemEncoding(head.ID, changed)
			if err != nil || len(mutated) != len(raw) || !collectionPlanInputMatches(x.rows[0].Row, changed) || changed.ContentDigest != original.ContentDigest {
				t.Fatal("corruption fixture changed metadata, MAC or length", err)
			}
			l := s.fsm.collections
			s.fsm.mu.Lock()
			l.mu.Lock()
			if disk {
				err = l.db.Update(func(tx *bolt.Tx) error {
					return tx.Bucket(collectionLedgerRecords).Bucket([]byte(head.ID)).Put(collectionOrdinal(original.Ordinal), mutated)
				})
			} else {
				l.rows[head.ID][original.Ordinal] = mutated
			}
			l.mu.Unlock()
			s.fsm.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.CollectionExecutionPreparationView(context.Background(), head.ID, c.Authority, c.CapabilitiesDigest, head.Execution.LastAt); !errors.Is(err, ErrCollectionUnavailable) || got != nil {
				t.Fatal("same-metadata ciphertext substitution accepted", err)
			}
			if s.fsm.collectionExecutionIndex != x {
				t.Fatal("corruption test accidentally bypassed the hot cache")
			}
		})
	}
}

func TestCollectionExecutionPreparationCacheNativeReadCounts(t *testing.T) {
	s, head, c := preparationCacheFixture(t, true, 64)
	for range 8 {
		head, _ = preparationCacheAccept(t, s, head, c)
	}
	db := s.fsm.collections.db
	read := func(hot bool) int64 {
		before := db.Stats()
		v := openExecutionPreparationView(t, s, head, c.Authority, head.Execution.LastAt)
		if v.cachedIndex != hot || v.ledger != nil || v.index != nil || db.Stats().OpenTxN != 0 {
			t.Fatal("wrong path or pinned transaction")
		}
		if _, _, err := v.Row(context.Background(), 9, head.Execution.LastAt); err != nil {
			t.Fatal(err)
		}
		_ = v.Close()
		after := db.Stats()
		return after.TxStats.GetCursorCount() - before.TxStats.GetCursorCount()
	}
	hot := read(true)
	s.fsm.mu.Lock()
	index, generation := s.fsm.collectionExecutionIndex, s.fsm.collectionExecutionLedger
	s.fsm.collectionExecutionIndex, s.fsm.collectionExecutionLedger = nil, nil
	s.fsm.mu.Unlock()
	cold := read(false)
	s.fsm.mu.Lock()
	s.fsm.collectionExecutionIndex, s.fsm.collectionExecutionLedger = index, generation
	s.fsm.mu.Unlock()
	if hot == 0 || hot > 256 || cold <= hot*4 {
		t.Fatalf("native cursor-read work not bounded: hot=%d cold=%d", hot, cold)
	}
	// Another accepted item changes the authoritative prefix but must not cause
	// a complete input/result/outcome reconstruction on the next read.
	head, _ = preparationCacheAccept(t, s, head, c)
	before := db.Stats()
	v := openExecutionPreparationView(t, s, head, c.Authority, head.Execution.LastAt)
	if _, _, err := v.Row(context.Background(), 10, head.Execution.LastAt); err != nil || !v.cachedIndex {
		t.Fatal("next original row not captured through cache", err)
	}
	_ = v.Close()
	after := db.Stats()
	next := after.TxStats.GetCursorCount() - before.TxStats.GetCursorCount()
	if next > 256 || next > hot+16 {
		t.Fatalf("prefix growth changed hot read complexity: first=%d next=%d", hot, next)
	}
	t.Logf("actual bbolt cursor operations: cold=%d hot=%d next-prefix=%d; 64 original rows, 8 then 9 accepted outcomes", cold, hot, next)
}

func TestCollectionExecutionPreparationCacheChildProgressIsIndependent(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head, c := preparationCacheFixture(t, disk, 3)
			head, accepted := preparationCacheAccept(t, s, head, c)
			at := head.Execution.LastAt.Add(time.Second)
			v := openExecutionPreparationView(t, s, head, c.Authority, at)
			input, err := v.Input(context.Background(), 2, at)
			if err != nil {
				t.Fatal(err)
			}
			update := childCommitUpdate(accepted, true)
			done := make(chan error, 1)
			go func() {
				results, err := s.Submit(context.Background(), []Command{{Kind: "operation", Operation: &update, At: at}})
				if err == nil && (len(results) != 1 || !results[0].Allowed) {
					err = fmt.Errorf("child completion rejected: %+v", results)
				}
				done <- err
			}()
			// The holder is read by only this goroutine; the independent Store
			// writer must be able to retire the prior child without any reader pin.
			for range 8 {
				if err := v.Check(context.Background(), at); err != nil {
					t.Fatal("completion interfered with next input", err)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("child writer blocked behind detached holder")
			}
			current, _, _ := s.CollectionGet(head.ID)
			if current.Execution.ChildApplied != 1 || !reflect.DeepEqual(v.Header(), head) {
				t.Fatal("terminal observation missing or captured header mutated")
			}
			again, err := v.Input(context.Background(), 2, at)
			if err != nil || !reflect.DeepEqual(again, input) {
				t.Fatal("terminal-only progress invalidated original input", err)
			}
			// Installing preparation changes the exact next-item fence, even
			// though child terminal changes were permitted above.
			prepared := executeCandidate(t, c, s.fsm.collectionExecutionIndex, 2, at.Add(time.Second))
			validationApplyAllowed(t, executeStoreCommand(t, s, prepared, prepared.Prepared.At))
			if err := v.Check(context.Background(), prepared.Prepared.At); !errors.Is(err, ErrCollectionConflict) {
				t.Fatal("new prepared slot did not invalidate old holder", err)
			}
		})
	}
}

func TestCollectionExecutionPreparationCacheRejectsChangedAuthoritativeProof(t *testing.T) {
	for _, which := range []string{"outcomes", "terminalRoot", "progress", "authority", "generation"} {
		t.Run(which, func(t *testing.T) {
			s, head, c := preparationCacheFixture(t, false, 2)
			head, _ = preparationCacheAccept(t, s, head, c)
			at := head.Execution.LastAt
			v := openExecutionPreparationView(t, s, head, c.Authority, at)
			s.fsm.mu.Lock()
			switch which {
			case "outcomes":
				s.fsm.collectionOutcomeCommitments[head.ID] = nil
			case "terminalRoot":
				tree, _ := newCollectionExecutionTerminalTree(1) // Valid tree, wrong parent cardinality.
				if s.fsm.collectionTerminalTrees == nil {
					s.fsm.collectionTerminalTrees = make(map[string]*collectionExecutionTerminalTree)
				}
				s.fsm.collectionTerminalTrees[head.ID] = tree
			case "progress":
				bad := head.Clone()
				bad.Execution.OutcomeDigest = strings.Repeat("e", 64)
				s.fsm.image.Collections[head.ID] = bad
			case "authority":
				s.fsm.image.Authentication.Revision = "rotated"
			case "generation":
				v.generation = &collectionLedger{}
			}
			s.fsm.mu.Unlock()
			if err := v.Check(context.Background(), at); err == nil {
				t.Fatal("stale holder accepted changed proof")
			}
		})
	}
}

func TestCollectionExecutionPreparationCacheBindingAndMetadataBudget(t *testing.T) {
	s, head, c := preparationCacheFixture(t, false, 2)
	x := s.fsm.collectionExecutionIndex
	for _, which := range []string{"generation", "audit", "activation", "result"} {
		t.Run(which, func(t *testing.T) {
			s.fsm.mu.Lock()
			candidate := *x
			s.fsm.collectionExecutionIndex, s.fsm.collectionExecutionLedger = &candidate, s.fsm.collections
			switch which {
			case "generation":
				s.fsm.collectionExecutionLedger = &collectionLedger{}
			case "audit":
				candidate.auditOnly = true
			case "activation":
				candidate.activationID = "00000000-0000-4000-8000-000000000000"
			case "result":
				candidate.result.Digest = strings.Repeat("d", 64)
			}
			s.fsm.mu.Unlock()
			v := openExecutionPreparationView(t, s, head, c.Authority, head.Execution.LastAt)
			if v.cachedIndex {
				t.Fatal("unrelated original index reused")
			}
			_ = v.Close()
		})
	}
	view, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	// If the hash charge were omitted, the constructor would reach this closed
	// reader and report that error instead. Quota must win before any read/allocation.
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	limits := defaultCollectionExecutionIndexLimits()
	limits.Bytes = int64(head.ItemCount) * (2048 + sha256.Size - 1)
	if _, err := buildCollectionExecutionIndex(context.Background(), head, view, limits); !errors.Is(err, errCollectionExecutionIndexLimit) {
		t.Fatal("hash storage not charged before row allocation", err)
	}
}
