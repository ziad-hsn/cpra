package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func executionItemFinalize(t *testing.T, s *Store, head CollectionState) CollectionState {
	t.Helper()
	at := head.Activation.At
	if head.Execution != nil && head.Execution.LastAt.After(at) {
		at = head.Execution.LastAt
	}
	if head.TerminalAt.After(at) {
		at = head.TerminalAt
	}
	return validationApplyAllowed(t, executeStoreCommand(t, s, executionResultFinalizeCommand(t, head), at.Add(time.Second)))
}

func executionItemPage(t *testing.T, s *Store, head CollectionState, cached *collectionExecutionItemIndex, after uint64, limit int) (collectionExecutionItemPage, *collectionExecutionItemIndex, error) {
	t.Helper()
	s.fsm.mu.RLock()
	i := image{Index: s.fsm.image.Index, OperationEpoch: s.fsm.image.OperationEpoch,
		OperationHighWater: s.fsm.image.OperationHighWater, CatalogMutationSequence: s.fsm.image.CatalogMutationSequence}
	s.fsm.mu.RUnlock()
	view, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	page, cache, err := buildCollectionExecutionItemPage(context.Background(), i, head, view, cached, after, limit)
	if !view.closed || s.fsm.collections.db != nil && s.fsm.collections.db.Stats().OpenTxN != 0 {
		t.Fatal("joined page retained its source transaction")
	}
	return page, cache, err
}

func executionItemReordered(t *testing.T, disk, stopped bool) (*Store, CollectionState) {
	t.Helper()
	s, head := executionIndexFixture(t, disk, "normal")
	// This structural plan fixture uses explicit original identities. Install
	// its row2 snapshot baseline so the failed prerequisite, rather than an
	// earlier target mismatch, determines dependencyBlocked. Source/plan/result
	// admission, decisions, cancellation and finalization use real commands.
	target := catalogDeltaRecord("Credential", "item-00003")
	target.UID, target.Revision = "original-uid", "original-revision"
	s.fsm.mu.Lock()
	target.CommittedIndex, target.DependentsVersion = s.fsm.image.Index, 7
	if s.fsm.image.Catalog == nil {
		s.fsm.image.Catalog = make(map[string]CatalogRecord)
	}
	s.fsm.image.Catalog[target.Key.indexKey()] = target
	s.fsm.image.CatalogMutationSequence = max(s.fsm.image.CatalogMutationSequence, 7)
	s.fsm.rebuildCatalogIndexes()
	s.fsm.mu.Unlock()
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, at), at))
	begin := executeBoundaryBegin(t, head, authority)
	head = validationApplyAllowed(t, executeStoreCommand(t, s, begin, at.Add(time.Second)))
	for ordinal := uint64(1); ordinal <= head.ItemCount; ordinal++ {
		c := begin
		c.Action, c.Ordinal = "decide", ordinal
		if ordinal == 2 {
			row, _ := s.fsm.collectionExecutionIndex.row(ordinal)
			candidate := target.Clone()
			candidate.Revision, candidate.Generation = "updated-version", 2
			candidate.CommittedIndex, candidate.DependentsVersion = 0, 0
			candidate.UpdatedAt = head.Execution.LastAt.Add(time.Second)
			prepared := begin
			prepared.Action, prepared.Ordinal = "prepare", ordinal
			prepared.Prepared = &CollectionPreparedItem{Binding: begin.Binding, ID: uuid.NewString(), Ordinal: ordinal,
				InputOrdinal: row.Row.InputOrdinal, RowDigest: row.RowDigest, At: candidate.UpdatedAt, Record: candidate}
			head = validationApplyAllowed(t, executeStoreCommand(t, s, prepared, prepared.Prepared.At))
			c = executeDecision(prepared)
		}
		head = validationApplyAllowed(t, executeStoreCommand(t, s, c, head.Execution.LastAt.Add(time.Second)))
		if stopped {
			head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
			break
		}
	}
	return s, executionItemFinalize(t, s, head)
}

func TestCollectionExecutionItemOriginalOrderAndStoppedSuffix(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, stopped := range []bool{false, true} {
			t.Run(fmt.Sprintf("disk=%t/stopped=%t", disk, stopped), func(t *testing.T) {
				s, head := executionItemReordered(t, disk, stopped)
				page, cache, err := executionItemPage(t, s, head, nil, 0, 1)
				if err != nil || len(page.Items) != 1 || page.Next != 1 || cache == nil {
					t.Fatal("first original input page", err)
				}
				next, reused, err := executionItemPage(t, s, head, cache, page.Next, 256)
				if err != nil || reused != cache || len(next.Items) != 2 || next.Next != 3 {
					t.Fatal("cached continuation", err)
				}
				items := append(page.Items, next.Items...)
				want := []string{"conflict", "conflict", "dependencyBlocked"}
				if stopped {
					want = []string{"unattempted", "conflict", "unattempted"}
				}
				for n, item := range items {
					if item.InputOrdinal != uint64(n+1) || item.PlanOrdinal != []uint64{3, 1, 2}[n] || item.Decision != want[n] || item.Child != nil || item.Source != "source.00000000000000000001" || item.SourceDocument != 1 || item.SourceItem != uint64(n+1) {
						t.Fatal("joined row lost original order, opaque coordinates or decision", n, item)
					}
					if err := item.validateSummary(head.ExecutionResult.Summary); err != nil {
						t.Fatal(err)
					}
				}
				if final, _, err := executionItemPage(t, s, head, cache, 3, 1); err != nil || len(final.Items) != 0 || final.Next != 3 {
					t.Fatal("exhausted page advanced or failed", err)
				}
			})
		}
	}
}

func TestCollectionExecutionItemDecisionAndChildRemainSeparate(t *testing.T) {
	for _, tc := range []struct {
		name, change, decision, state, outcome string
		applied                                bool
	}{
		{"applied", "create", "accepted", "completed", "applied", true},
		{"projection failed", "create", "accepted", "failed", "projection_failed", false},
		{"unchanged", "unchanged", "unchanged", "", "", false},
		{"conflict", "create", "conflict", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, begin := executionResultInput(t, s, tc.change)
			head, child := executionResultDecide(t, s, head, begin, tc.decision)
			if child != nil {
				head = executionResultSettle(t, s, head, child, tc.applied)
			}
			head = executionItemFinalize(t, s, head)
			page, cache, err := executionItemPage(t, s, head, nil, 0, 256)
			if err != nil || len(page.Items) != 1 || !cache.matches(head) {
				t.Fatal("joined decision", err)
			}
			item := page.Items[0]
			if item.Decision != tc.decision || item.CommittedIndex == 0 || item.DecidedAt.IsZero() || (item.Child != nil) != (child != nil) {
				t.Fatal("catalog decision changed with child state", item)
			}
			if child != nil && (item.Child.ID != child.ID || item.Child.State != tc.state || item.Child.Outcome != tc.outcome || item.UID != child.UID || item.NewVersion != child.NewVersion) {
				t.Fatal("original child identity or terminal lost", item)
			}
			if tc.change == "unchanged" && (item.OriginalUID != item.UID || item.OldVersion != item.NewVersion) {
				t.Fatal("unchanged row invented a new incarnation or version")
			}
			raw, err := collectionExecutionItemEncoding(item)
			decoded, decodeErr := decodeCollectionExecutionItem(raw)
			if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, item) || int64(len(raw)) != page.Bytes {
				t.Fatal("canonical joined row did not round trip", err, decodeErr)
			}
			if item.Child != nil {
				page.Items[0].Child.Outcome = "caller-mutation"
				repeated, _, err := executionItemPage(t, s, head, cache, 0, 256)
				if err != nil || repeated.Items[0].Child.Outcome != tc.outcome {
					t.Fatal("detached row altered retained certificate", err)
				}
			}
		})
	}
}

func TestCollectionExecutionItemBoundsAndNativeCacheWork(t *testing.T) {
	s, head, _ := preparationCacheFixture(t, true, 300)
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	head = executionItemFinalize(t, s, head)
	db := s.fsm.collections.db
	cursorCount := func() int64 { stats := db.Stats(); return stats.TxStats.GetCursorCount() }
	before := cursorCount()
	first, cache, err := executionItemPage(t, s, head, nil, 0, 1)
	cold := cursorCount() - before
	if err != nil || len(first.Items) != 1 {
		t.Fatal(err)
	}
	before = cursorCount()
	if page, reused, err := executionItemPage(t, s, head, cache, 1, 1); err != nil || reused != cache || len(page.Items) != 1 {
		t.Fatal("cached native item", err)
	}
	hot := cursorCount() - before
	if hot == 0 || hot > 256 || cold <= hot*4 {
		t.Fatalf("cached page rescanned source: cold=%d hot=%d", cold, hot)
	}
	if page, _, err := executionItemPage(t, s, head, cache, 0, 256); err != nil || len(page.Items) != 256 || page.Next != 256 || page.Bytes > collectionExecutionItemPageBytes {
		t.Fatal("bounded page size", err)
	}
	if _, _, err := executionItemPage(t, s, head, cache, 0, 257); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("oversized row-count request accepted", err)
	}
	view, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if page, err := collectionExecutionItemsFromVerified(context.Background(), head, view, cache.index, cache.proof, 0, 256, first.Bytes-1); !errors.Is(err, ErrCollectionQuota) || len(page.Items) != 0 || page.Next != 0 {
		t.Fatal("oversized first row advanced an empty page", err)
	}
	if page, err := collectionExecutionItemsFromVerified(context.Background(), head, view, cache.index, cache.proof, 0, 256, first.Bytes+1); err != nil || len(page.Items) != 1 || page.Next != 1 || page.Bytes != first.Bytes {
		t.Fatal("byte-boundary page lost the continuation", err)
	}
	t.Logf("native cursor operations: cold=%d hot=%d for a 300-item source", cold, hot)
}

func TestCollectionExecutionItemCachedCorruptionAndCancellation(t *testing.T) {
	for _, corruption := range []string{"input", "plan", "outcome", "terminal", "missing terminal", "binding"} {
		t.Run(corruption, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, begin := executionResultInput(t, s, "create")
			head, child := executionResultDecide(t, s, head, begin, "accepted")
			head = executionResultSettle(t, s, head, child, true)
			head = executionItemFinalize(t, s, head)
			_, cache, err := executionItemPage(t, s, head, nil, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			l := s.fsm.collections
			switch corruption {
			case "input":
				r, err := decodeCollectionLedgerRow(l.rows[head.ID][1])
				if err != nil {
					t.Fatal(err)
				}
				r.Item.Payload.Ciphertext[0] ^= 1
				l.rows[head.ID][1], err = collectionItemEncoding(head.ID, r.Item)
				if err != nil {
					t.Fatal(err)
				}
			case "plan":
				ordinal := cache.index.rows[0].FirstFragment
				r, err := decodeCollectionPlanLedgerRow(l.planRows[head.ID][ordinal])
				if err != nil {
					t.Fatal(err)
				}
				r.Part.Fragment.Row.Document++
				l.planRows[head.ID][ordinal], err = collectionPlanLedgerEncoding(head.ID, r.Part)
				if err != nil {
					t.Fatal(err)
				}
			case "outcome", "terminal":
				slot := collectionExecutionOutcomeSlot(1)
				if corruption == "terminal" {
					slot = collectionExecutionTerminalSlot(1)
				}
				r, err := decodeCollectionExecutionRecord(l.executionRows[head.ID][slot])
				if err != nil {
					t.Fatal(err)
				}
				if r.Outcome != nil {
					r.Outcome.At = r.Outcome.At.Add(time.Millisecond)
					r.Outcome.Receipt.At, r.Outcome.Receipt.UpdatedAt = r.Outcome.At, r.Outcome.At
				} else {
					r.Terminal.State, r.Terminal.Outcome = "failed", "projection_failed"
				}
				l.executionRows[head.ID][slot], err = collectionExecutionEncoding(r)
				if err != nil {
					t.Fatal(err)
				}
			case "missing terminal":
				delete(l.executionRows[head.ID], collectionExecutionTerminalSlot(1))
			case "binding":
				head.ExecutionResult.Summary.FinalizedAt = head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
				head.TerminalAt = head.ExecutionResult.Summary.FinalizedAt
			}
			if page, reused, err := executionItemPage(t, s, head, cache, 0, 1); err == nil || reused != nil || len(page.Items) != 0 || page.Next != 0 {
				t.Fatal("cached selected corruption escaped", err)
			}
		})
	}
	s := openCatalogMemory(t)
	head, _ := executionResultInput(t, s, "create")
	head, _ = activationSnapshotCancel(t, s, head, head.Activation.At.Add(time.Second))
	head = executionItemFinalize(t, s, head)
	view, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, cache, err := buildCollectionExecutionItemPage(ctx, s.fsm.image, head, view, nil, 0, 1)
	if !errors.Is(err, context.Canceled) || !view.closed || cache != nil || len(page.Items) != 0 {
		t.Fatal("cancellation did not close its frozen reader", err)
	}
	page, _, err = executionItemPage(t, s, head, nil, 0, 1)
	if err != nil || page.Items[0].Decision != "unattempted" || !page.Items[0].DecidedAt.IsZero() || page.Items[0].CommittedIndex != 0 {
		t.Fatal("before-begin stop fabricated a decision", err)
	}
}

func TestCollectionExecutionItemCodecRejectsUnpublishableMetadata(t *testing.T) {
	s := openCatalogMemory(t)
	head, begin := executionResultInput(t, s, "create")
	head, child := executionResultDecide(t, s, head, begin, "accepted")
	head = executionResultSettle(t, s, head, child, true)
	head = executionItemFinalize(t, s, head)
	page, _, err := executionItemPage(t, s, head, nil, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	raw, _ := collectionExecutionItemEncoding(item)
	for _, mutate := range []func(*CollectionExecutionItem){
		func(i *CollectionExecutionItem) { i.Source = "/private/config.yaml" },
		func(i *CollectionExecutionItem) { i.Source = "https://secret.invalid/?token=secret" },
		func(i *CollectionExecutionItem) { i.Decision = "provider failed with secret" },
		func(i *CollectionExecutionItem) { i.UID = strings.Repeat("x", collectionExecutionItemMaxBytes) },
		func(i *CollectionExecutionItem) { i.Child.Outcome = "provider secret" },
		func(i *CollectionExecutionItem) { i.Child.ID = i.Binding.OperationID },
		func(i *CollectionExecutionItem) { i.Child.InvalidatedByRestore = "/private/restore-path" },
	} {
		bad := item.Clone()
		mutate(&bad)
		if _, err := collectionExecutionItemEncoding(bad); err == nil {
			t.Fatal("unpublishable row encoded")
		}
	}
	for _, bad := range [][]byte{
		append(bytes.Clone(raw), '\n'),
		append([]byte(`{"version":1,`), raw[1:]...),
		append([]byte(`{"plaintext":"secret",`), raw[1:]...),
		bytes.Repeat([]byte(" "), collectionExecutionItemMaxBytes+1),
	} {
		if _, err := decodeCollectionExecutionItem(bad); err == nil {
			t.Fatal("noncanonical or oversized row decoded")
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"payload", "ciphertext", "record", "config", "error", "path", "url", "row_digest", "content_digest"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatal("private source material escaped", forbidden)
		}
	}
}

func TestCollectionExecutionItemNativeSelectedCommitments(t *testing.T) {
	for _, kind := range []string{"input", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			s, head, command := preparationCacheFixture(t, true, 1)
			head, accepted := preparationCacheAccept(t, s, head, command)
			head = executionResultSettle(t, s, head, accepted.Receipt, true)
			head = executionItemFinalize(t, s, head)
			_, cache, err := executionItemPage(t, s, head, nil, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			l := s.fsm.collections
			if err := l.db.Update(func(tx *bolt.Tx) error {
				if kind == "input" {
					b := tx.Bucket(collectionLedgerRecords).Bucket([]byte(head.ID))
					r, err := decodeCollectionLedgerRow(b.Get(collectionOrdinal(1)))
					if err != nil {
						return err
					}
					r.Item.Payload.Ciphertext[0] ^= 1
					raw, err := collectionItemEncoding(head.ID, r.Item)
					if err != nil {
						return err
					}
					return b.Put(collectionOrdinal(1), raw)
				}
				b := tx.Bucket(collectionLedgerExecution).Bucket([]byte(head.ID))
				slot := []byte(collectionExecutionTerminalSlot(1))
				r, err := decodeCollectionExecutionRecord(b.Get(slot))
				if err != nil {
					return err
				}
				r.Terminal.State, r.Terminal.Outcome = "failed", "projection_failed"
				raw, err := collectionExecutionEncoding(r)
				if err != nil {
					return err
				}
				return b.Put(slot, raw)
			}); err != nil {
				t.Fatal(err)
			}
			if page, reused, err := executionItemPage(t, s, head, cache, 0, 1); err == nil || reused != nil || len(page.Items) != 0 {
				t.Fatal("native same-identity substitution escaped selected proof", err)
			}
		})
	}
}

func TestCollectionExecutionItemRestoredChildDisposition(t *testing.T) {
	s, head, command := preparationCacheFixture(t, true, 2)
	head, accepted := preparationCacheAccept(t, s, head, command)
	config := s.config
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	at := head.Execution.LastAt.Add(time.Second)
	if err := MarkRestored(config.Storage.Directory, at); err != nil {
		t.Fatal(err)
	}
	admin, err := OpenAdministrative(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	state, err := admin.Authentication()
	if err != nil || !state.ResetRequired {
		t.Fatal("missing reset fence", err)
	}
	provision := authenticationBootstrap()
	provision.Mode, provision.Epoch, provision.ExpectedEpoch = "provision", state.Epoch, state.Epoch
	provision.ExpectedRevision, provision.At = state.Revision, at.Add(time.Second)
	provision.Principals[0].ExpiresAt = provision.At.Add(time.Hour)
	if _, err := admin.CommitAuthentication(context.Background(), provision); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	restored.fsm.mu.RLock()
	head = restored.fsm.image.Collections[head.ID].Clone()
	restored.fsm.mu.RUnlock()
	head = executionItemFinalize(t, restored, head)
	page, _, err := executionItemPage(t, restored, head, nil, 0, 256)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("restore join", err)
	}
	first, second := page.Items[0], page.Items[1]
	if first.Decision != "accepted" || first.Child == nil || first.Child.ID != accepted.Receipt.ID || first.Child.State != "partial" || first.Child.Outcome != "superseded" || first.Child.InvalidatedByRestore != head.InvalidatedByRestore || first.Child.InvalidatedByRestore == "" || second.Decision != "unattempted" || second.Child != nil {
		t.Fatal("restore changed original acceptance or invented remaining work", first, second)
	}
}

func TestCollectionExecutionItemCloseFailureIsUnavailable(t *testing.T) {
	s, head, _ := preparationCacheFixture(t, true, 1)
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	head = executionItemFinalize(t, s, head)
	view, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	// Roll back the real transaction behind its still-open view to exercise a
	// failing Close without a production injection hook. Validation also fails;
	// neither failure may be returned as an ordinary bounded quota rejection.
	if err := view.tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	page, cache, err := buildCollectionExecutionItemPage(context.Background(), s.fsm.image, head, view, nil, head.ItemCount+1, 1)
	if !errors.Is(err, ErrCollectionUnavailable) || !errors.Is(err, ErrCollectionInvalid) || !errors.Is(err, bolt.ErrTxClosed) || err == errCollectionExecutionIndexLimit || len(page.Items) != 0 || cache != nil || !view.closed {
		t.Fatal("close failure lost fatal classification or returned a result", err)
	}
}

func TestCollectionExecutionItemAbandonedPreparationIsUnattempted(t *testing.T) {
	s := openCatalogMemory(t)
	head, command := executionResultInput(t, s, "create")
	head = validationApplyAllowed(t, executeStoreCommand(t, s, command, head.Activation.At.Add(time.Second)))
	prepared := executeCandidate(t, command, s.fsm.collectionExecutionIndex, 1, head.Execution.LastAt.Add(time.Second))
	head = validationApplyAllowed(t, executeStoreCommand(t, s, prepared, prepared.Prepared.At))
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	head = executionItemFinalize(t, s, head)
	if head.ExecutionResult.Summary.Fence.Progress.Prepared == nil {
		t.Fatal("fixture lost the abandoned preparation")
	}
	page, _, err := executionItemPage(t, s, head, nil, 0, 1)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	i := page.Items[0]
	if i.Decision != "unattempted" || i.UID != "" || i.NewVersion != "" || i.CommittedIndex != 0 || i.Child != nil || !i.DecidedAt.IsZero() {
		t.Fatal("unaccepted preparation became a committed item", i)
	}
}
