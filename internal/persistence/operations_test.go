package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func operationMutation(m CatalogMutation) CatalogMutation {
	m.OperationID, m.Actor = m.Record.Revision, "team/oncall"
	return m
}

func createOperationResource(t *testing.T, s *Store, id, revision string) Result {
	t.Helper()
	r := catalogRecord(t, s, "Monitor", id, id+"-uid", revision, "private-runtime-config")
	r.CreatedAt, r.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	result := catalogSubmit(t, s, operationMutation(CatalogMutation{Record: r, Create: true}))
	if result.Err != nil || result.Operation == nil {
		t.Fatalf("missing committed receipt: %v", result.Err)
	}
	return result
}

func completeReceipt(t *testing.T, s *Store, r OperationReceipt, applied bool) Result {
	t.Helper()
	result := submit(t, s, Command{Kind: "operation", At: r.At.Add(time.Second), Operation: &OperationUpdate{
		ID: r.ID, Key: r.Key, UID: r.UID, Revision: r.NewVersion, Applied: applied}})[0]
	if result.Err != nil || result.Operation == nil {
		t.Fatalf("missing projection receipt: %v", result.Err)
	}
	return result
}

func TestCatalogOperationReceiptsAndSupersession(t *testing.T) {
	s := openCatalogMemory(t)
	created := createOperationResource(t, s, "service", "operation-one")
	r, err := s.Operation("operation-one")
	if err != nil || r.State != "committed" || r.Outcome != "committed" || r.Actor != "team/oncall" {
		t.Fatalf("save incorrectly reported application: %+v %v", r, err)
	}
	// Public result mutation cannot edit the stored receipt or timeline.
	created.Operation.Actor = "changed-by-caller"
	created.Events[0].Operation.Actor = "changed-event"
	updated := catalogSubmit(t, s, operationMutation(updateCatalogMutation(t, s, *created.Catalog, "operation-two", "new-private-config")))
	if updated.Err != nil || updated.Operation == nil {
		t.Fatal(updated.Err)
	}
	r, err = s.Operation("operation-one")
	if err != nil || r.State != "partial" || r.Outcome != "superseded" || r.Actor != "team/oncall" {
		t.Fatalf("superseded receipt lost or mutable: %+v %v", r, err)
	}
	stale := submit(t, s, Command{Kind: "operation", At: updated.Operation.At, Operation: &OperationUpdate{
		ID: r.ID, Key: r.Key, UID: r.UID, Revision: r.NewVersion, Applied: true}})[0]
	if !errors.Is(stale.Err, ErrOperationNotFound) {
		t.Fatalf("old completion applied to replacement: %v", stale.Err)
	}
	completed := completeReceipt(t, s, *updated.Operation, true)
	completed.Operation.State = "changed"
	completed.Events[0].Operation.State = "changed"
	r, err = s.Operation("operation-two")
	if err != nil || r.State != "completed" || r.Outcome != "applied" {
		t.Fatalf("completion was lost or caller mutated history: %+v %v", r, err)
	}
	page, err := s.History().Page("service", "", 100)
	if err != nil || len(page.Events) != 4 {
		t.Fatalf("unexpected event timeline: %d %v", len(page.Events), err)
	}
	for _, event := range page.Events {
		if event.Operation == nil || event.Operation.Actor != "team/oncall" {
			t.Fatal("missing authenticated audit actor")
		}
		event.Operation.Actor = "page-caller"
	}
	if r, err = s.Operation("operation-two"); err != nil || r.Actor != "team/oncall" {
		t.Fatal("timeline reader changed stored receipt", err)
	}
	deleted := catalogSubmit(t, s, operationMutation(deleteCatalogMutation(*updated.Catalog, "operation-delete")))
	if deleted.Err != nil || !deleted.Operation.Removed {
		t.Fatal("deletion omitted receipt", deleted.Err)
	}
	completeReceipt(t, s, *deleted.Operation, true)
	if _, exists, _ := s.CatalogGet(r.Key); exists {
		t.Fatal("deleted resource remained active")
	}
	if r, err = s.Operation("operation-delete"); err != nil || !r.Removed || r.State != "completed" {
		t.Fatal("deleted resource cannot be reconciled by receipt", err)
	}
}

func TestOperationCapacityPermitsSupersessionAndDraining(t *testing.T) {
	s := openCatalogMemory(t)
	var first CatalogRecord
	for start := 0; start < maxPendingCatalogOperations; start += 256 {
		commands := make([]Command, 0, 256)
		for i := start; i < start+256; i++ {
			id := fmt.Sprintf("monitor-%04d", i)
			r := catalogRecord(t, s, "Monitor", id, id+"-uid", id+"-operation", "private")
			m := operationMutation(CatalogMutation{Record: r, Create: true})
			commands = append(commands, Command{Kind: "catalog", At: r.UpdatedAt, Catalog: &m})
		}
		results := submit(t, s, commands...)
		for _, result := range results {
			if result.Err != nil || result.Operation == nil {
				t.Fatal("failed to fill pending capacity", result.Err)
			}
		}
		if start == 0 {
			first = *results[0].Catalog
		}
	}
	r := catalogRecord(t, s, "Monitor", "overflow", "overflow-uid", "overflow-op", "private")
	if result := catalogSubmit(t, s, operationMutation(CatalogMutation{Record: r, Create: true})); !errors.Is(result.Err, ErrCatalogBusy) {
		t.Fatalf("capacity overflow admitted: %v", result.Err)
	}
	deleted := catalogSubmit(t, s, operationMutation(deleteCatalogMutation(first, "deleted-at-capacity")))
	if deleted.Err != nil || deleted.Operation == nil {
		t.Fatal("same-slot supersession was rejected", deleted.Err)
	}
	completeReceipt(t, s, *deleted.Operation, true)
	if result := catalogSubmit(t, s, operationMutation(CatalogMutation{Record: r, Create: true})); result.Err != nil {
		t.Fatal("draining did not restore admission", result.Err)
	}
	if !s.Status().Ready {
		t.Fatal("expected admission rejection poisoned storage")
	}
}

func TestCatalogClockRollbackCannotCorruptSupersededReceipt(t *testing.T) {
	s := openCatalogMemory(t)
	r := catalogRecord(t, s, "Monitor", "service", "uid", "before-clock-step", "private")
	m := operationMutation(CatalogMutation{Record: r, Create: true})
	created := submit(t, s, Command{Kind: "catalog", At: r.UpdatedAt.Add(time.Minute), Catalog: &m})[0]
	if created.Err != nil {
		t.Fatal(created.Err)
	}
	// Preparation ordering remains valid while the observed commit time moves
	// backwards. Reject before replacing either catalog state or its receipt.
	changed := operationMutation(updateCatalogMutation(t, s, *created.Catalog, "after-clock-step", "private"))
	result := catalogSubmit(t, s, changed)
	if !errors.Is(result.Err, ErrCatalogConflict) {
		t.Fatalf("clock rollback persisted invalid receipt: %v", result.Err)
	}
	if got := requireCatalog(t, s, r.Key); got.Revision != r.Revision {
		t.Fatal("rejected clock update partially changed resource")
	}
	if got, err := s.Operation(r.Revision); err != nil || got.State != "committed" || got.validate() != nil {
		t.Fatal("original receipt no longer valid", err)
	}
}

func TestPendingReceiptSnapshotRejectsMismatchedResource(t *testing.T) {
	s := openCatalogMemory(t)
	createOperationResource(t, s, "service", "snapshot-op")
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	raw, _ := json.Marshal(snapshot.(*frozenSnapshot).image)
	for name, corrupt := range map[string]func(*OperationReceipt){
		"generation": func(r *OperationReceipt) { r.Generation++ },
		"removed":    func(r *OperationReceipt) { r.Removed = !r.Removed },
		"index":      func(r *OperationReceipt) { r.CommittedIndex-- },
		"uid":        func(r *OperationReceipt) { r.UID = "other" },
		"state":      func(r *OperationReceipt) { r.State, r.Outcome = "completed", "applied" },
	} {
		t.Run(name, func(t *testing.T) {
			var state image
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			r := state.Operations["snapshot-op"]
			corrupt(&r)
			state.Operations[r.ID] = r
			encoded, _ := json.Marshal(state)
			if _, err := decodeImage(bytes.NewReader(encoded)); err == nil {
				t.Fatal("corrupt receipt restored")
			}
		})
	}
}

func TestReceiptHistoryRetentionAndMissingSegment(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk-%t", disk), func(t *testing.T) {
			dir := ""
			if disk {
				dir = t.TempDir()
			}
			h, err := openHistory(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			at := time.Now().UTC().Add(-31 * 24 * time.Hour)
			r := OperationReceipt{ID: "old", Key: CatalogKey{Kind: "Monitor", ID: "service"}, UID: "uid", NewVersion: "old", Generation: 1, CommittedIndex: 1, Actor: "oncall", At: at, UpdatedAt: at, State: "completed", Outcome: "applied"}
			one := receiptEvent(r)
			one.ID = fmt.Sprintf("%020d:%08d", 1, 0)
			if err := h.append(1, []Event{one}, at); err != nil {
				t.Fatal(err)
			}
			r.ID, r.NewVersion, r.At, r.UpdatedAt, r.CommittedIndex = "new", "new", at.Add(31*24*time.Hour), at.Add(31*24*time.Hour), 2
			two := receiptEvent(r)
			two.ID = fmt.Sprintf("%020d:%08d", 2, 0)
			if err := h.append(2, []Event{two}, r.At); err != nil {
				t.Fatal(err)
			}
			if err := h.Expire(r.At); err != nil {
				t.Fatal(err)
			}
			if _, err := h.operation("old"); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("expired operation remained visible", err)
			}
			if got, err := h.operation("new"); err != nil || got.ID != "new" {
				t.Fatal("newer receipt expired", err)
			}
			if disk {
				if _, err := os.Stat(filepath.Join(dir, at.Format("2006-01-02")+".db")); !os.IsNotExist(err) {
					t.Fatal("expired segment was not reclaimed", err)
				}
				// Unix open descriptors must not hide a missing durable file.
				if err := os.Remove(filepath.Join(dir, r.At.Format("2006-01-02")+".db")); err != nil {
					t.Skip("platform does not permit removing open database")
				}
				if _, err := h.operation("new"); !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("unlinked receipt still reported available", err)
				}
			}
		})
	}
}

func TestReceiptHistoryIndexCorruptionRejectsRestart(t *testing.T) {
	for _, damage := range []string{"missing", "stale", "mismatched"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			h, err := openHistory(dir)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			r := OperationReceipt{ID: "op", Key: CatalogKey{Kind: "Monitor", ID: "service"}, UID: "uid", NewVersion: "op", Generation: 1, CommittedIndex: 1, Actor: "oncall", At: at, UpdatedAt: at, State: "committed", Outcome: "committed"}
			first := receiptEvent(r)
			first.ID = fmt.Sprintf("%020d:%08d", 1, 0)
			if err := h.append(1, []Event{first}, at); err != nil {
				t.Fatal(err)
			}
			r.State, r.Outcome = "completed", "applied"
			last := receiptEvent(r)
			last.ID = fmt.Sprintf("%020d:%08d", 2, 0)
			if err := h.append(2, []Event{last}, at); err != nil {
				t.Fatal(err)
			}
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(filepath.Join(dir, at.Format("2006-01-02")+".db"), 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte("operations"))
				if damage == "missing" {
					return b.Delete([]byte("op"))
				}
				event := first
				if damage == "mismatched" {
					event.Operation.Actor = "different"
				}
				raw, _ := json.Marshal(event)
				return b.Put([]byte("op"), raw)
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if other, err := openHistory(dir); err == nil {
				other.Close()
				t.Fatal("corrupt operation index opened")
			}
		})
	}
}

func TestOperationCanceledBeforeAdmissionHasNoReceipt(t *testing.T) {
	s := openCatalogMemory(t)
	r := catalogRecord(t, s, "Monitor", "service", "uid", "never-committed", "private")
	m := operationMutation(CatalogMutation{Record: r, Create: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Submit(ctx, []Command{{Kind: "catalog", At: r.UpdatedAt, Catalog: &m}}); !errors.Is(err, context.Canceled) || errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatal("pre-admission outcome misclassified", err)
	}
	if _, err := s.Operation(r.Revision); !errors.Is(err, ErrOperationNotFound) {
		t.Fatal("uncommitted request acquired receipt", err)
	}
}
