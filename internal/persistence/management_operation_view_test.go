package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
	bolt "go.etcd.io/bbolt"
)

func managementOperationAll(t *testing.T, v ManagementOperationView, monitor string, limit int) []OperationObservation {
	t.Helper()
	var rows []OperationObservation
	after := ""
	for range 10000 {
		page, next, err := v.Page(t.Context(), monitor, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page {
			if (row.Operation == nil) == (row.Collection == nil) {
				t.Fatal("invalid receipt union")
			}
		}
		rows = append(rows, page...)
		if next == "" {
			return rows
		}
		if next == after {
			t.Fatal("cursor did not advance")
		}
		after = next
	}
	t.Fatal("unbounded pagination")
	return nil
}
func managementCollectionCreate(t *testing.T, s *Store, actor string) CollectionState {
	t.Helper()
	c := collectionCreateFixture(t, s, 1)
	c.Create.Actor = actor
	return validationApplyAllowed(t, collectionCommand(t, s, c, c.Create.ActivityAt))
}
func managementCollectionTerminal(t *testing.T, s *Store, actor string) CollectionState {
	t.Helper()
	head := managementCollectionCreate(t, s, actor)
	at := head.ActivityAt.Add(time.Second)
	return validationApplyAllowed(t, collectionCommand(t, s, collectionCancelFixture(head, at), at))
}

func TestManagementOperationViewFrozenUnionCleanupAndOwnership(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s := openCatalogMemory(t)
			if disk {
				s = validationExpiryDiskStore(t)
			}
			reserved, _ := allocatedCommand(t, s, reservationCommand(t, s, "ordinary-shared", time.Now().UTC()))
			live := managementCollectionCreate(t, s, "oncall")
			terminal := managementCollectionTerminal(t, s, "oncall")
			foreign := managementCollectionTerminal(t, s, "other")
			sealed, _ := resultViewSealed(t, s, 1)
			at := sealed.ActivityAt.Add(time.Second)
			// Keep the ordinary reservation active at the fixture's future timestamp.
			s.fsm.mu.Lock()
			r := s.fsm.image.OperationReservations[reserved.ID]
			r.ExpiresAt = at.Add(time.Hour)
			s.fsm.image.OperationReservations[reserved.ID] = r
			s.fsm.mu.Unlock()
			view, err := s.ManagementOperationSnapshot(t.Context(), "oncall", at, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			before := managementOperationAll(t, view, "", 1)
			seen := map[string]bool{}
			for _, row := range before {
				if row.Operation != nil {
					seen[row.Operation.ID] = true
				} else {
					if row.Collection.Actor != "oncall" {
						t.Fatal("foreign collection disclosed")
					}
					seen[row.Collection.ID] = true
				}
			}
			if len(before) != 4 || !seen[live.ID] || !seen[terminal.ID] || !seen[sealed.ID] || !seen[reserved.ID] || seen[foreign.ID] {
				t.Fatal("snapshot membership", len(before))
			}
			cancelAt := at.Add(time.Second)
			live = validationApplyAllowed(t, collectionCommand(t, s, collectionCancelFixture(live, cancelAt), cancelAt))
			resultViewCleanup(t, s, live, cancelAt)
			resultViewCleanup(t, s, terminal, cancelAt)
			managementCollectionTerminal(t, s, "oncall")
			after := managementOperationAll(t, view, "", 2)
			if !reflect.DeepEqual(after, before) {
				t.Fatal("cleanup/insertion changed frozen membership or phases")
			}
			for _, row := range after {
				if row.Collection != nil {
					row.Collection.Actor = "mutated"
					if row.Collection.Validation != nil {
						row.Collection.Validation.Header.Issue = "mutated"
					}
				}
			}
			if !reflect.DeepEqual(managementOperationAll(t, view, "", 2), before) {
				t.Fatal("page aliases captured metadata")
			}
			ordinary, err := s.ManagementOperationSnapshot(t.Context(), "", at, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			plain := managementOperationAll(t, ordinary, "", 100)
			if len(plain) != 1 || plain[0].Operation == nil {
				t.Fatal("empty actor exposed collections")
			}
			filtered := managementOperationAll(t, view, "ordinary-shared", 100)
			if len(filtered) != 1 || filtered[0].Operation == nil {
				t.Fatal("exact monitor filter included collections")
			}
			raw, err := json.Marshal(before)
			if err != nil || strings.Contains(string(raw), "payload") || strings.Contains(string(raw), "ciphertext") || strings.Contains(string(raw), "source_fingerprint") {
				t.Fatal("snapshot retained resource payload")
			}
		})
	}
}

func managementAppendCollectionHistory(t *testing.T, s *Store, count int, actorAtEnd string) time.Time {
	t.Helper()
	s.fsm.mu.Lock()
	defer s.fsm.mu.Unlock()
	epoch := s.fsm.image.OperationEpoch
	if epoch == "" {
		epoch = uuid.NewString()
		s.fsm.image.OperationEpoch = epoch
	}
	at := time.Now().UTC()
	index := s.fsm.image.Index + 1
	events := make([]Event, count)
	for i := range events {
		actor := "foreign"
		if i == count-1 {
			actor = actorAtEnd
		}
		receipt := CollectionReceipt{ID: operationHandle(epoch, uint64(i+1)), UploadID: uuid.NewString(), Actor: actor, IdentityFormat: commitment.Format, ContentDigest: strings.Repeat("a", 64), ItemCount: 1, Phase: "canceled", CreatedAt: at, ActivityAt: at, ExpiresAt: at.Add(CollectionInactivityLifetime), TerminalAt: at, CancellationID: uuid.NewString()}
		event := collectionReceiptEvent(receipt)
		event.ID = fmt.Sprintf("%020d:%08d", index, i)
		events[i] = event
	}
	if err := s.fsm.history.append(index, events, at); err != nil {
		t.Fatal(err)
	}
	s.fsm.image.Index = index
	s.fsm.image.OperationHighWater = uint64(count)
	return at
}

func TestManagementOperationViewBoundedForeignHistoryAndMemoryDiskParity(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s := openCatalogMemory(t)
			if disk {
				s = validationExpiryDiskStore(t)
			}
			at := managementAppendCollectionHistory(t, s, 3001, "owner")
			view, err := s.ManagementOperationSnapshot(t.Context(), "owner", at, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			page, next, err := view.Page(t.Context(), "", "c:", 100)
			if err != nil || len(page) != 0 || next == "" || next == "c:" {
				t.Fatal("bounded foreign prefix did not return an advancing empty page", len(page), next, err)
			}
			emptyPages := 1
			for range 10 {
				page, after, err := view.Page(t.Context(), "", next, 100)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) != 0 {
					if len(page) != 1 || page[0].Collection == nil || page[0].Collection.Actor != "owner" {
						t.Fatal("wrong owned row")
					}
					if emptyPages < 2 {
						t.Fatal("fixture did not cross multiple work budgets")
					}
					return
				}
				if after == "" || after == next {
					t.Fatal("filtered work silently truncated")
				}
				next = after
				emptyPages++
			}
			t.Fatal("bounded iterator failed to reach owned terminal")
		})
	}
}

func TestManagementOperationViewQuotaExpiryAndSummaryOnly(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := resultViewSealed(t, s, 1)
	at := head.ActivityAt.Add(time.Second)
	view, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, view.EstimatedBytes()-1); !errors.Is(err, ErrOperationSnapshotQuota) {
		t.Fatal("collection copies not charged", err)
	}
	if _, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, view.EstimatedBytes()); err != nil {
		t.Fatal("exact charged budget rejected", err)
	}
	s.History().mu.Lock()
	delete(s.History().memoryValidationResults[head.ID].items, 1)
	s.History().mu.Unlock()
	rows, _, err := view.Page(t.Context(), "", "l:", 100)
	if err != nil || len(rows) != 1 || rows[0].Collection.Phase != "rejected" {
		t.Fatal("list accessed item rows instead of original summary", err)
	}
	s.History().mu.Lock()
	s.History().memoryValidationResults[head.ID].summary = nil
	s.History().mu.Unlock()
	if _, _, err := view.Page(t.Context(), "", "l:", 100); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("missing seal fabricated verdict", err)
	}
	expired, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, head.ExpiresAt, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err = expired.Page(t.Context(), "", "l:", 100)
	if err != nil || len(rows) != 1 || rows[0].Collection.Phase != "expired" || !rows[0].Collection.TerminalAt.IsZero() {
		t.Fatal("elapsed staging forged terminal event", err)
	}
	original, ok, err := s.CollectionGet(head.ID)
	if err != nil || !ok || original.Phase != "rejected" || !original.TerminalAt.IsZero() {
		t.Fatal("listing mutated original state", err)
	}
	if err := s.History().Expire(at.Add(31 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := view.Page(t.Context(), "", "", 100); !errors.Is(err, ErrOperationCursorExpired) {
		t.Fatal("retention did not expire cursor", err)
	}
}

func TestManagementOperationViewCorruptHistory(t *testing.T) {
	for _, failure := range []string{"index", "primary", "segment", "memory-index", "memory-tree"} {
		t.Run(failure, func(t *testing.T) {
			s := openCatalogMemory(t)
			if !strings.HasPrefix(failure, "memory") {
				s = validationExpiryDiskStore(t)
			}
			head := managementCollectionTerminal(t, s, "owner")
			at := head.TerminalAt
			view, err := s.ManagementOperationSnapshot(t.Context(), "owner", at, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			h := s.History()
			switch failure {
			case "index", "primary":
				h.mu.Lock()
				db := h.databases[at.UTC().Format("2006-01-02")]
				err = db.Update(func(tx *bolt.Tx) error {
					if failure == "index" {
						return tx.Bucket(collectionReceiptBucket).Delete([]byte(head.ID))
					}
					b := tx.Bucket([]byte("events"))
					key, _ := b.Cursor().Seek([]byte("collection/" + head.ID + "\x00"))
					return b.Delete(key)
				})
				h.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "segment":
				path := filepath.Join(h.dir, at.UTC().Format("2006-01-02")+".db")
				if err := os.Rename(path, path+".missing"); err != nil {
					t.Fatal(err)
				}
			case "memory-index":
				h.mu.Lock()
				delete(h.memoryCollectionReceipts, head.ID)
				h.mu.Unlock()
			case "memory-tree":
				h.mu.Lock()
				h.memoryCollectionTree.Delete(operationMemoryItem{key: head.ID})
				h.mu.Unlock()
			}
			if _, _, err := view.Page(t.Context(), "", "c:", 100); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("missing evidence returned empty success", err)
			}
		})
	}
}

func TestManagementOperationViewContextHealthAndNoLedgerAccess(t *testing.T) {
	s := openCatalogMemory(t)
	head := managementCollectionCreate(t, s, "owner")
	// The header is enough. No staged resource ledger is consulted by listing.
	s.fsm.mu.Lock()
	ledger := s.fsm.collections
	s.fsm.collections = nil
	s.fsm.mu.Unlock()
	defer func() { s.fsm.mu.Lock(); s.fsm.collections = ledger; s.fsm.mu.Unlock() }()
	view, err := s.ManagementOperationSnapshot(t.Context(), "owner", head.ActivityAt, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if rows := managementOperationAll(t, view, "", 100); len(rows) != 1 {
		t.Fatal("metadata-only list needed ledger")
	}
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
	s.History().mu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := view.Page(ctx, "", "", 100); done <- err }()
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		s.History().mu.Unlock()
		t.Fatal("page did not reach history wait")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		s.History().mu.Unlock()
		t.Fatal("history wait ignored context")
	}
	s.History().mu.Unlock()
	s.fsm.mu.Lock()
	s.fsm.image.OperationEpoch = uuid.NewString()
	s.fsm.mu.Unlock()
	if _, _, err := view.Page(t.Context(), "", "", 100); !errors.Is(err, ErrOperationCursorExpired) {
		t.Fatal("restore epoch did not fence old view", err)
	}
}

func TestManagementOperationViewPostReadEpochAndConstructionCancellation(t *testing.T) {
	for _, constructor := range []bool{false, true} {
		t.Run(fmt.Sprint(constructor), func(t *testing.T) {
			s := openCatalogMemory(t)
			head := managementCollectionCreate(t, s, "owner")
			view, err := s.ManagementOperationSnapshot(t.Context(), "owner", head.ActivityAt, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
			s.History().mu.Lock()
			locked := true
			defer func() {
				if locked {
					s.History().mu.Unlock()
				}
			}()
			done := make(chan error, 1)
			go func() {
				if constructor {
					_, err := s.ManagementOperationSnapshot(ctx, "owner", head.ActivityAt, 16<<20)
					done <- err
				} else {
					_, _, err := view.Page(ctx, "", "l:", 100)
					done <- err
				}
			}()
			select {
			case <-ctx.waiting:
			case <-time.After(time.Second):
				t.Fatal("read did not reach protected history wait")
			}
			want := ErrOperationCursorExpired
			if constructor {
				cancel()
				want = context.Canceled
			} else {
				// Page releases Store/FSM ownership before disk/history work. Restore can
				// change its epoch while the read is waiting, and the final fence must win.
				s.fsm.mu.Lock()
				s.fsm.image.OperationEpoch = uuid.NewString()
				s.fsm.mu.Unlock()
				s.History().mu.Unlock()
				locked = false
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatal("lost post-read epoch/context fence", err)
				}
			case <-time.After(time.Second):
				t.Fatal("protected read remained blocked")
			}
		})
	}
}

func TestManagementOperationViewStrictBounds(t *testing.T) {
	s := openCatalogMemory(t)
	head := managementCollectionCreate(t, s, "owner")
	v, err := s.ManagementOperationSnapshot(t.Context(), "owner", head.ActivityAt, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, after := range []string{"x:", "o:", "o:l:", "o:h:bad", "c:bad", "l:" + operationHandle(uuid.NewString(), 1), "c:" + operationHandle(v.ordinary.epoch, v.ordinary.highWater+1), strings.Repeat("x", 261)} {
		if _, _, err := v.Page(t.Context(), "", after, 100); !errors.Is(err, ErrOperationReservation) {
			t.Fatal("malformed cursor accepted", after, err)
		}
	}
	for _, limit := range []int{-1, 0, 501} {
		if _, _, err := v.Page(t.Context(), "", "", limit); !errors.Is(err, ErrOperationReservation) {
			t.Fatal("invalid limit accepted", err)
		}
	}
	if _, _, err := v.Page(nil, "", "", 100); !errors.Is(err, ErrOperationReservation) {
		t.Fatal("nil context accepted", err)
	}
	s.fsm.mu.Lock()
	for i := 0; i < maxCollectionOperations; i++ {
		copy := head
		copy.ID = fmt.Sprintf("extra-%d", i)
		s.fsm.image.Collections[copy.ID] = copy
	}
	s.fsm.mu.Unlock()
	if _, err := s.ManagementOperationSnapshot(t.Context(), "owner", head.ActivityAt, 16<<20); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("header count bound not enforced before cloning", err)
	}
}

func TestManagementOperationViewFillsExhaustedPhasesInFirstPage(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, family := range []string{"ordinary", "collection", "mixed"} {
			t.Run(fmt.Sprintf("%t/%s", disk, family), func(t *testing.T) {
				s := openCatalogMemory(t)
				if disk {
					s = validationExpiryDiskStore(t)
				}
				at := time.Now().UTC()
				want := map[string]bool{}
				if family != "collection" {
					r, _ := allocatedCommand(t, s, reservationCommand(t, s, "ordinary", at))
					want[r.ID] = true
				}
				if family != "ordinary" {
					live := managementCollectionCreate(t, s, "owner")
					terminal := managementCollectionTerminal(t, s, "owner")
					want[live.ID], want[terminal.ID] = true, true
					at = terminal.TerminalAt
				}
				view, err := s.ManagementOperationSnapshot(t.Context(), "owner", at, 16<<20)
				if err != nil {
					t.Fatal(err)
				}
				rows, next, err := view.Page(t.Context(), "", "", 100)
				if err != nil || next != "" || len(rows) != len(want) {
					t.Fatalf("small complete list split across phases: rows=%d want=%d cursor=%q err=%v", len(rows), len(want), next, err)
				}
				for _, row := range rows {
					id := ""
					if row.Operation != nil {
						id = row.Operation.ID
					} else if row.Collection != nil {
						id = row.Collection.ID
					}
					if !want[id] {
						t.Fatalf("unexpected or duplicated first-page row %q", id)
					}
					delete(want, id)
				}
			})
		}
	}
}

func TestManagementOperationViewSharesInspectionBudgetAcrossPhases(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s := openCatalogMemory(t)
			if disk {
				s = validationExpiryDiskStore(t)
			}
			at := managementAppendCollectionHistory(t, s, 3001, "owner")
			view, err := s.ManagementOperationSnapshot(t.Context(), "owner", at, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			rows, next, err := view.Page(t.Context(), "", "", 100)
			if err != nil || len(rows) != 0 || !strings.HasPrefix(next, "c:") {
				t.Fatalf("bounded first page: rows=%d cursor=%q err=%v", len(rows), next, err)
			}
			_, seq, err := ParseOperationHandle(strings.TrimPrefix(next, "c:"))
			// Ordinary receipts, collection receipts, and execution anchors each
			// pay source-head costs. Transitions must preserve the shared budget.
			fixed := 16 + 8*len(view.ordinary.segments) + 8*max(1, len(view.ordinary.segments))
			if err != nil || seq != uint64((maxOperationInspectedKeys-fixed)/8) {
				t.Fatalf("phase transition reset inspection budget: frontier=%d fixed=%d err=%v", seq, fixed, err)
			}
		})
	}
}

func TestManagementOperationViewChargesCurrentSummarySegments(t *testing.T) {
	s := validationExpiryDiskStore(t)
	var head CollectionState
	for range 48 {
		head, _ = resultViewSealed(t, s, 1)
	}
	at := head.ActivityAt.Add(time.Second)
	view, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ordinary.segments) != 1 || len(view.collections) != 48 {
		t.Fatal("fixture must begin with one real segment and 48 sealed verdicts")
	}
	// Append retained events from 29 earlier days after capture. Their daily
	// bbolt files really exist, and summary verification visits them despite the
	// original observation's one-day listing boundary. This is controlled history
	// materialization, not another validation or a claim about wall-clock passage.
	s.fsm.mu.Lock()
	index := s.fsm.image.Index + 1
	events := make([]Event, 29)
	for i := range events {
		events[i] = Event{ID: fmt.Sprintf("%020d:%08d", index, i), MonitorID: "ordinary-history", At: at.AddDate(0, 0, -i-1), Type: "incident_opened"}
	}
	err = s.History().append(index, events, at)
	s.fsm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.History().catalog.Segments) != 30 {
		t.Fatal("fixture did not grow the real retained catalog")
	}
	rows, next, err := view.Page(t.Context(), "", "l:", 100)
	want := maxOperationInspectedKeys / (1 + 8*(30+1))
	if err != nil || len(rows) != want || next != "l:"+view.collections[want-1].ID {
		t.Fatalf("summary verification undercharged new segments: rows=%d want=%d cursor=%q err=%v", len(rows), want, next, err)
	}
	more, final, err := view.Page(t.Context(), "", next, 100)
	if err != nil || len(more) != 48-want || final != "" {
		t.Fatalf("remaining original summaries did not reconcile: rows=%d cursor=%q err=%v", len(more), final, err)
	}
}
