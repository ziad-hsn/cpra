package persistence

import (
	"bytes"
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

func collectionReceiptFixture(index uint64, at time.Time) Event {
	r := CollectionReceipt{ID: operationHandle(uuid.NewString(), 1), UploadID: uuid.NewString(), Actor: "team/oncall", IdentityFormat: commitment.Format,
		ContentDigest: strings.Repeat("a", 64), ItemCount: 100, Uploaded: 27, Phase: "canceled", CreatedAt: at.Add(-time.Hour), ActivityAt: at.Add(-time.Minute),
		ExpiresAt: at.Add(-time.Minute).Add(CollectionInactivityLifetime), TerminalAt: at, CancellationID: uuid.NewString()}
	e := collectionReceiptEvent(r)
	e.ID = fmt.Sprintf("%020d:%08d", index, 0)
	return e
}

func collectionHistoryFixture(t *testing.T, disk bool) *HistoryStore {
	t.Helper()
	dir := ""
	if disk {
		dir = t.TempDir()
	}
	h, err := openHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestCollectionReceiptObservationSurvivesHeaderCleanup(t *testing.T) {
	for _, phase := range []string{"canceled", "expired"} {
		t.Run(phase, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := collectionFill(t, s, 300)
			active, err := s.CollectionReceipt(context.Background(), head.ID, head.ActivityAt)
			if err != nil || active.Phase != "uploading" || active.Uploaded != 300 || active.UploadID != head.UploadID || !active.TerminalAt.IsZero() {
				t.Fatal("active observation", active, err)
			}
			at := head.ActivityAt.Add(time.Second)
			if phase == "canceled" {
				c := collectionCancelFixture(head, at)
				if got := collectionCommand(t, s, c, at); got.Err != nil {
					t.Fatal(got.Err)
				}
			} else {
				at = head.ExpiresAt
				if got := collectionCommand(t, s, cleanupCommand(head), at); got.Err != nil {
					t.Fatal(got.Err)
				}
			}
			receipt, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || receipt.Phase != phase || receipt.Uploaded != 300 || receipt.ItemCount != 300 || !receipt.TerminalAt.Equal(at) {
				t.Fatal("terminal observation", receipt, err)
			}
			for len(s.fsm.image.Collections) > 0 {
				if err := s.maintainCollections(at); err != nil {
					t.Fatal(err)
				}
			}
			after, err := s.CollectionReceipt(context.Background(), head.ID, at.AddDate(0, 0, 29))
			if err != nil || !collectionReceiptsEqual(after, receipt) {
				t.Fatal("cleanup erased receipt", err)
			}
			after.Actor = "edited"
			fresh, _ := s.CollectionReceipt(context.Background(), head.ID, at)
			if fresh.Actor != head.Actor {
				t.Fatal("receipt aliases stored evidence")
			}
			if _, err := s.CollectionReceipt(context.Background(), head.ID, at.AddDate(0, 0, 31)); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("retired receipt outlived deadline", err)
			}
			if _, err := s.CollectionReceipt(context.Background(), operationHandle(s.fsm.image.OperationEpoch, 999), at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("unissued handle misclassified", err)
			}
			if _, err := s.CollectionReceipt(context.Background(), operationHandle(uuid.NewString(), 1), at); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("wrong epoch admitted", err)
			}
			if len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Operations) != 0 {
				t.Fatal("receipt lookup caused active writes")
			}
			raw, _ := json.Marshal(receipt)
			for _, private := range []string{"ciphertext", "secret", "source_fingerprint", "private-inventory", "never-write"} {
				if bytes.Contains(raw, []byte(private)) {
					t.Fatal("private data in receipt")
				}
			}
		})
	}
}

func TestCollectionReceiptHistoryReplayRetentionAndCopies(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			h := collectionHistoryFixture(t, disk)
			at := time.Now().UTC()
			event := collectionReceiptFixture(1, at)
			id := event.Collection.ID
			if err := h.append(1, []Event{event}, at); err != nil {
				t.Fatal(err)
			}
			if err := h.append(1, []Event{event}, at); err != nil {
				t.Fatal(err)
			}
			if _, err := h.collectionReceipt(context.Background(), id, 0, at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("ahead-of-FSM receipt exposed", err)
			}
			got, err := h.collectionReceipt(context.Background(), id, 1, at)
			if err != nil || !collectionReceiptsEqual(got, *event.Collection) {
				t.Fatal("point lookup", err)
			}
			page, err := h.Page(event.MonitorID, "", 100)
			if err != nil || len(page.Events) != 1 {
				t.Fatal("duplicate receipt event", err)
			}
			page.Events[0].Collection.Actor = "mutated"
			got, err = h.collectionReceipt(context.Background(), id, 1, at)
			if err != nil || got.Actor != "team/oncall" {
				t.Fatal("timeline leaked receipt pointer", err)
			}
			if err := h.Expire(at.AddDate(0, 0, 29)); err != nil {
				t.Fatal(err)
			}
			if _, err := h.collectionReceipt(context.Background(), id, 1, at.AddDate(0, 0, 29)); err != nil {
				t.Fatal("early expiry", err)
			}
			if disk {
				if err := h.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := openHistory(h.dir)
				if err != nil {
					t.Fatal(err)
				}
				h = reopened
				defer h.Close()
				if _, err := h.collectionReceipt(context.Background(), id, 1, at.AddDate(0, 0, 29)); err != nil {
					t.Fatal("restart lost receipt", err)
				}
			}
			if err := h.Expire(at.AddDate(0, 0, 31)); err != nil {
				t.Fatal(err)
			}
			if _, err := h.collectionReceipt(context.Background(), id, 1, at.AddDate(0, 0, 31)); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("retained expired receipt", err)
			}
			if disk {
				if _, err := os.Stat(filepath.Join(h.dir, at.Format("2006-01-02")+".db")); !os.IsNotExist(err) {
					t.Fatal("segment not reclaimed", err)
				}
			}
		})
	}
}

func TestCollectionReceiptLegacyAuditStaysReadableButUnavailableAsReceipt(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			h := collectionHistoryFixture(t, disk)
			at := time.Now().UTC()
			event := collectionReceiptFixture(1, at)
			id := event.Collection.ID
			event.Collection = nil
			if err := h.append(1, []Event{event}, at); err != nil {
				t.Fatal(err)
			}
			if _, err := h.collectionReceipt(context.Background(), id, 1, at); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("legacy terminal evidence called expired", err)
			}
			page, err := h.Page(event.MonitorID, "", 100)
			if err != nil || len(page.Events) != 1 || page.Events[0].Collection != nil {
				t.Fatal("legacy timeline lost or fabricated", err)
			}
			if disk {
				if err := h.Close(); err != nil {
					t.Fatal(err)
				}
				other, err := openHistory(h.dir)
				if err != nil {
					t.Fatal("legacy history rejected", err)
				}
				defer other.Close()
				if _, err := other.collectionReceipt(context.Background(), id, 1, at); !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("restart invented legacy counts", err)
				}
			}
			if _, err := h.collectionReceipt(context.Background(), id, 0, at); !disk && !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("legacy event ignored watermark", err)
			}
		})
	}
}

func TestCollectionReceiptIndexCorruptionUnavailableAndRejectsRestart(t *testing.T) {
	for _, damage := range []string{"missing-index", "missing-bucket", "orphan-index", "wrong-index", "wrong-primary", "oversize", "missing-file"} {
		t.Run(damage, func(t *testing.T) {
			h := collectionHistoryFixture(t, true)
			at := time.Now().UTC()
			event := collectionReceiptFixture(1, at)
			id := event.Collection.ID
			if err := h.append(1, []Event{event}, at); err != nil {
				t.Fatal(err)
			}
			db := h.databases[at.Format("2006-01-02")]
			if damage == "missing-file" {
				if err := os.Remove(filepath.Join(h.dir, at.Format("2006-01-02")+".db")); err != nil {
					t.Skip("platform does not unlink open files")
				}
			} else {
				if err := db.Update(func(tx *bolt.Tx) error {
					index := tx.Bucket(collectionReceiptBucket)
					primary := tx.Bucket([]byte("events"))
					key := []byte(event.MonitorID + "\x00" + event.ID)
					switch damage {
					case "missing-index":
						return index.Delete([]byte(id))
					case "missing-bucket":
						return tx.DeleteBucket(collectionReceiptBucket)
					case "orphan-index":
						return primary.Delete(key)
					case "wrong-index":
						copy := event.Clone()
						copy.Collection.Actor = "another"
						raw, _ := json.Marshal(copy)
						return index.Put([]byte(id), raw)
					case "wrong-primary":
						copy := event.Clone()
						copy.Collection.Actor = "another"
						raw, _ := json.Marshal(copy)
						return primary.Put(key, raw)
					case "oversize":
						raw := append(bytes.Clone(index.Get([]byte(id))), bytes.Repeat([]byte(" "), maxCollectionReceiptEventBytes)...)
						return index.Put([]byte(id), raw)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := validateHistoryOperations(db); !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("offline index validation accepted damage", err)
				}
			}
			if _, err := h.collectionReceipt(context.Background(), id, 1, at); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("damage hidden as expiry", err)
			}
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			if other, err := openHistory(h.dir); err == nil {
				other.Close()
				t.Fatal("damaged history reopened")
			}
		})
	}
}

func TestCollectionReceiptConflictingSameBatchIsAtomic(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			h := collectionHistoryFixture(t, disk)
			at := time.Now().UTC()
			first := collectionReceiptFixture(1, at)
			second := first.Clone()
			second.ID = fmt.Sprintf("%020d:%08d", 1, 1)
			second.Collection.CancellationID = uuid.NewString()
			second.ActionID = second.Collection.CancellationID
			if err := h.append(1, []Event{first, second}, at); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("contradictory outcome accepted", err)
			}
			if h.catalog.Index != 0 || len(h.memory) != 0 {
				t.Fatal("failed append advanced history")
			}
			if disk {
				for _, db := range h.databases {
					if err := db.View(func(tx *bolt.Tx) error {
						if b := tx.Bucket(collectionReceiptBucket); b != nil && b.Stats().KeyN != 0 {
							t.Fatal("failed receipt transaction committed prefix")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestCollectionReceiptReplayRepairsSegmentAheadOfWatermark(t *testing.T) {
	h := collectionHistoryFixture(t, true)
	at := time.Now().UTC()
	event := collectionReceiptFixture(2, at)
	id := event.Collection.ID
	if err := h.append(2, []Event{event}, at); err != nil {
		t.Fatal(err)
	}
	dir := h.dir
	catalog := h.catalog
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	// Reproduce completed segment sync followed by a missing catalog update.
	// The event exists physically, but a reader must honor both watermarks.
	catalog.Index = 1
	if err := atomicJSON(filepath.Join(dir, "catalog.json"), catalog); err != nil {
		t.Fatal(err)
	}
	recovered, err := openHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err := recovered.collectionReceipt(context.Background(), id, 1, at); !errors.Is(err, ErrOperationNotFound) {
		t.Fatal("unpublished history receipt exposed", err)
	}
	if _, err := recovered.collectionReceipt(context.Background(), id, 2, at); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("missing history progress treated as absent", err)
	}
	if err := recovered.append(2, []Event{event}, at); err != nil {
		t.Fatal("exact segment replay rejected", err)
	}
	got, err := recovered.collectionReceipt(context.Background(), id, 2, at)
	if err != nil || !collectionReceiptsEqual(got, *event.Collection) {
		t.Fatal("watermark repair lost receipt", err)
	}
	page, err := recovered.Page(event.MonitorID, "", 100)
	if err != nil || len(page.Events) != 1 {
		t.Fatal("watermark repair duplicated receipt", err)
	}
}

func TestCollectionReceiptContextAndIndexBounds(t *testing.T) {
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CollectionReceipt(ctx, head.ID, head.ActivityAt); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.fsm.mu.Lock()
	waiting, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err := s.CollectionReceipt(waiting, head.ID, head.ActivityAt)
	stop()
	s.fsm.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock ignored context", err)
	}
	h := collectionHistoryFixture(t, false)
	for n := 0; n < maxOperationSegments+1; n++ {
		h.catalog.Segments[fmt.Sprint(n)] = true
	}
	if _, err := h.collectionReceipt(context.Background(), head.ID, 0, head.ActivityAt); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("unbounded segment set", err)
	}
}

func TestCollectionReceiptInvalidPayloadAndHeaderMismatch(t *testing.T) {
	event := collectionReceiptFixture(1, time.Now().UTC())
	for _, change := range []func(*Event){
		func(e *Event) { e.Collection.ItemCount = 0 }, func(e *Event) { e.Collection.Uploaded = e.Collection.ItemCount + 1 },
		func(e *Event) { e.Collection.Phase = "applied" }, func(e *Event) { e.Collection.TerminalAt = time.Time{} },
		func(e *Event) { e.Collection.CancellationID = "invalid" }, func(e *Event) { e.Collection.InvalidatedByRestore = uuid.NewString() },
		func(e *Event) { e.Actor = "other" }, func(e *Event) { e.Note = "secret should not be accepted" },
		func(e *Event) {
			e.Collection.Phase = "uploading"
			e.Collection.TerminalAt = time.Time{}
			e.Collection.CancellationID = ""
		},
	} {
		copy := event.Clone()
		change(&copy)
		if validateCollectionReceiptEvent(copy) == nil {
			t.Fatal("invalid terminal receipt accepted")
		}
	}
	s := openCatalogMemory(t)
	head := createCollectionFixture(t, s, 1)
	c := collectionCancelFixture(head, head.ActivityAt.Add(time.Second))
	collectionCommand(t, s, c, c.Cancel.At)
	h := s.History()
	h.mu.Lock()
	original := h.memoryCollectionReceipts[head.ID].Clone()
	changed := original.Clone()
	changed.Collection.Uploaded = 1
	h.memoryCollectionReceipts[head.ID] = changed
	h.memoryCollectionEvidence[head.ID] = changed
	h.mu.Unlock()
	if _, err := s.CollectionReceipt(context.Background(), head.ID, c.Cancel.At); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("header/history mismatch ignored", err)
	}
	h.mu.Lock()
	delete(h.memoryCollectionReceipts, head.ID)
	delete(h.memoryCollectionEvidence, head.ID)
	h.mu.Unlock()
	if _, err := s.CollectionReceipt(context.Background(), head.ID, c.Cancel.At); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("known terminal receipt disappeared silently", err)
	}
}

func TestCollectionReceiptRaftCleanupRestartAndRestore(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot=%t", snapshot), func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			head := collectionFill(t, s, 3)
			at := head.ActivityAt.Add(time.Hour)
			c := collectionCancelFixture(head, at)
			if got := collectionCommand(t, s, c, at); got.Err != nil {
				t.Fatal(got.Err)
			}
			want, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.maintainCollections(at); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err := LockOffline(config.Storage.Directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || !collectionReceiptsEqual(got, want) {
				t.Fatal("retired receipt lost after replay", err)
			}
			if len(s.fsm.image.Collections) != 0 {
				t.Fatal("receipt rebuilt active upload")
			}
			second := collectionFill(t, s, 1)
			oldEpoch, _, _ := ParseOperationHandle(second.ID)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			restoreAt := second.ActivityAt.Add(time.Second)
			if err := MarkRestored(config.Storage.Directory, restoreAt); err != nil {
				t.Fatal(err)
			}
			s, err = OpenAdministrative(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			if s.fsm.image.OperationEpoch == oldEpoch {
				t.Fatal("restore kept old operation epoch")
			}
			invalidated, err := s.History().collectionReceipt(context.Background(), second.ID, s.fsm.image.Index, restoreAt)
			if err != nil || invalidated.Phase != "invalidated" || invalidated.InvalidatedByRestore == "" || invalidated.Uploaded != 1 {
				t.Fatal("restore terminal receipt", invalidated, err)
			}
			prior, err := s.History().collectionReceipt(context.Background(), head.ID, s.fsm.image.Index, restoreAt)
			if err != nil || !reflect.DeepEqual(prior, want) {
				t.Fatal("restore rewrote already-terminal receipt", err)
			}
			if err := s.Snapshot(); err != nil {
				t.Fatal(err)
			}
			page, err := s.History().Page("collection/"+second.ID, "", 100)
			if err != nil || len(page.Events) != 1 {
				t.Fatal("duplicate invalidation receipt", err)
			}
		})
	}
}

func TestCollectionReceiptRestoreSharesBoundedPageAndDeterministicOrder(t *testing.T) {
	s := openCatalogMemory(t)
	for n := 0; n < maxCollectionOperations; n++ {
		createCollectionFixture(t, s, 1)
	}
	at := time.Now().UTC().Add(time.Minute)
	s.fsm.mu.Lock()
	defer s.fsm.mu.Unlock()
	for n := 0; n < restorePageSize; n++ {
		receipt := viewTerminal(fmt.Sprintf("legacy-%04d", n), "fixture", at.Add(-time.Second))
		s.fsm.putOperation(receipt)
	}
	state := RestoreState{Phase: "receipts", Marker: RestoreMarker{ID: uuid.NewString(), At: at}}
	first := s.fsm.restoreReceipts(&state)
	if first.Err != nil || len(first.Events) != restorePageSize || state.Phase == "complete" {
		t.Fatal("restore page overflow or premature completion", first.Err)
	}
	previous := ""
	for n, event := range first.Events {
		if n < maxCollectionOperations {
			if event.Collection == nil || event.Collection.ID <= previous || event.Collection.Phase != "invalidated" {
				t.Fatal("nondeterministic collection event ordering")
			}
			previous = event.Collection.ID
		} else if event.Collection != nil {
			t.Fatal("unexpected collection receipt")
		}
	}
	second := s.fsm.restoreReceipts(&state)
	if second.Err != nil || len(second.Events) != maxCollectionOperations || state.Phase != "complete" {
		t.Fatal("restore receipt continuation", second.Err)
	}
	for _, event := range second.Events {
		if event.Collection != nil {
			t.Fatal("restore retry duplicated collection receipt")
		}
	}
}
