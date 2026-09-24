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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func validationHistoryFixture(t *testing.T, count int, large bool) (CollectionValidationReceipt, []CollectionValidationItem) {
	t.Helper()
	r := CollectionValidationReceipt{Header: CollectionValidationHeader{ResultID: uuid.NewString(), OperationID: operationHandle(uuid.NewString(), 1), UploadID: uuid.NewString(), InputProgressDigest: strings.Repeat("a", 64), ItemCount: uint64(count), Authority: OperatorAuthority{Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "operator"}, CapabilitiesDigest: strings.Repeat("b", 64), Issue: "invalidGraph"}, FinalizedAt: time.Date(2026, 9, 20, 11, 20, 0, 0, time.UTC), Descriptor: CollectionValidationDescriptor{Digest: CollectionValidationInitialDigest()}}
	items := make([]CollectionValidationItem, count)
	for i := range items {
		id := fmt.Sprintf("monitor-%06d", i)
		if large {
			id += strings.Repeat("a", 220)
		}
		items[i] = CollectionValidationItem{Ordinal: uint64(i + 1), Key: CatalogKey{Kind: "Monitor", ID: id}, Source: "source.00000000000000000001", Document: 1, Item: uint64(i + 1), Change: "update", Issue: "conflict", UID: "uid", ResourceVersion: "version"}
		if large {
			items[i].UID = strings.Repeat("u", 250)
			items[i].ResourceVersion = strings.Repeat("v", 250)
		}
		digest, n, err := CollectionValidationNextDigest(r.Descriptor.Digest, items[i])
		if err != nil {
			t.Fatal(err)
		}
		r.Descriptor.Count++
		r.Descriptor.Bytes += n
		r.Descriptor.Digest = digest
	}
	if count == 0 {
		r.Header.ItemCount = CollectionValidationMaxItems + 1
		r.Header.SummaryOnly = true
		r.Header.Issue = "validationLimit"
	}
	if r.validate() != nil {
		t.Fatal("bad fixture")
	}
	return r, items
}

func validationHistoryEvents(r CollectionValidationReceipt, items []CollectionValidationItem, index uint64, seal bool) []Event {
	events := make([]Event, 0, len(items)+1)
	for _, item := range items {
		item := item
		e := collectionValidationHistoryEvent(CollectionValidationHistory{OperationID: r.Header.OperationID, ResultID: r.Header.ResultID, FinalizedAt: r.FinalizedAt, Item: &item})
		e.ID = fmt.Sprintf("%020d:%08d", index, len(events))
		events = append(events, e)
	}
	if seal {
		e := collectionValidationHistoryEvent(CollectionValidationHistory{OperationID: r.Header.OperationID, ResultID: r.Header.ResultID, FinalizedAt: r.FinalizedAt, Summary: &r})
		e.ID = fmt.Sprintf("%020d:%08d", index, len(events))
		events = append(events, e)
	}
	return events
}

func validationHistoryPublish(t *testing.T, h *HistoryStore, r CollectionValidationReceipt, items []CollectionValidationItem) uint64 {
	t.Helper()
	var index uint64
	for start := 0; start < len(items); start += 256 {
		index++
		end := min(start+256, len(items))
		if err := h.append(index, validationHistoryEvents(r, items[start:end], index, false), r.FinalizedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	index++
	if err := h.append(index, validationHistoryEvents(r, nil, index, true), r.FinalizedAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	return index
}

func TestCollectionValidationHistoryPublicationRetentionAndRestart(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			h := collectionHistoryFixture(t, disk)
			r, items := validationHistoryFixture(t, 600, false)
			if err := h.append(1, validationHistoryEvents(r, items[:256], 1, false), r.FinalizedAt); err != nil {
				t.Fatal(err)
			}
			if _, err := h.collectionValidationPage(context.Background(), r, 1, 0, 100, r.FinalizedAt); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("partial published", err)
			}
			if err := h.append(2, validationHistoryEvents(r, items[256:], 2, true), r.FinalizedAt); err != nil {
				t.Fatal(err)
			}
			if _, err := h.collectionValidationPage(context.Background(), r, 1, 0, 100, r.FinalizedAt); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("ahead of FSM published", err)
			}
			if err := h.append(2, validationHistoryEvents(r, items[256:], 2, true), r.FinalizedAt); err != nil {
				t.Fatal(err)
			}
			if disk {
				dir := h.dir
				if err := h.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				h, err = openHistory(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer h.Close()
			}
			var got []CollectionValidationItem
			var after uint64
			for {
				p, err := h.collectionValidationPage(context.Background(), r, 2, after, 100, r.FinalizedAt.Add(29*24*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				if !collectionValidationReceiptsEqual(p.Receipt, r) || len(p.Items) > 100 {
					t.Fatal("wrong page")
				}
				got = append(got, p.Items...)
				after = p.NextAfter
				if after == 0 {
					break
				}
			}
			if !reflect.DeepEqual(got, items) {
				t.Fatal("missing/duplicate/out-of-order items")
			}
			got[0].Key.ID = "mutated"
			p, err := h.collectionValidationPage(context.Background(), r, 2, 0, 1, r.FinalizedAt)
			if err != nil || p.Items[0] != items[0] {
				t.Fatal("aliased page", err)
			}
			if _, err := h.collectionValidationPage(context.Background(), r, 2, 0, 100, r.FinalizedAt.AddDate(0, 0, 30)); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("deadline not exact", err)
			}
			day := r.FinalizedAt.Format("2006-01-02")
			if err := h.Expire(r.FinalizedAt.AddDate(0, 0, 32)); err != nil {
				t.Fatal(err)
			}
			if _, err := h.collectionValidationPage(context.Background(), r, 2, 0, 100, r.FinalizedAt.AddDate(0, 0, 29)); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("retention clock rolled back", err)
			}
			if disk {
				if _, err := os.Stat(filepath.Join(h.dir, day+".db")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("segment not reclaimed", err)
				}
			} else if len(h.memoryValidationResults) != 0 {
				t.Fatal("memory index not expired")
			}
		})
	}
}

func TestCollectionValidationHistoryZeroSummaryAndLargeInventory(t *testing.T) {
	for _, count := range []int{0, 5000} {
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("count=%d/disk=%t", count, disk), func(t *testing.T) {
				h := collectionHistoryFixture(t, disk)
				r, items := validationHistoryFixture(t, count, true)
				index := validationHistoryPublish(t, h, r, items)
				if count > 0 && r.Descriptor.Bytes <= 4<<20 {
					t.Fatal("fixture did not cross 4 MiB")
				}
				var after, seen uint64
				for {
					p, err := h.collectionValidationPage(context.Background(), r, index, after, 500, r.FinalizedAt)
					if err != nil {
						t.Fatal(err)
					}
					if len(p.Items) > 500 {
						t.Fatal("unbounded page")
					}
					seen += uint64(len(p.Items))
					after = p.NextAfter
					if after == 0 {
						break
					}
				}
				if seen != uint64(count) {
					t.Fatal("wrong item count", seen)
				}
				if disk {
					if err := validateHistoryOperations(h.databases[r.FinalizedAt.Format("2006-01-02")]); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestCollectionValidationHistoryRejectsPartialSealAndConflictingReplay(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, mode := range []string{"early-seal", "wrong-digest", "wrong-result", "wrong-cohort", "out-of-order", "same-item-new-event", "after-seal", "unsafe-payload"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, mode), func(t *testing.T) {
				h := collectionHistoryFixture(t, disk)
				r, items := validationHistoryFixture(t, 2, false)
				if err := h.append(1, validationHistoryEvents(r, items[:1], 1, false), r.FinalizedAt); err != nil {
					t.Fatal(err)
				}
				var events []Event
				switch mode {
				case "early-seal":
					events = validationHistoryEvents(r, nil, 2, true)
				case "wrong-digest":
					r.Descriptor.Digest = strings.Repeat("d", 64)
					events = validationHistoryEvents(r, items[1:], 2, true)
				case "wrong-result":
					r.Header.ResultID = uuid.NewString()
					events = validationHistoryEvents(r, items[1:], 2, false)
				case "wrong-cohort":
					r.FinalizedAt = r.FinalizedAt.Add(time.Hour)
					events = validationHistoryEvents(r, items[1:], 2, false)
				case "out-of-order":
					items[1].Ordinal = 3
					events = validationHistoryEvents(r, items[1:], 2, false)
				case "same-item-new-event":
					events = validationHistoryEvents(r, items[:1], 2, false)
				case "after-seal":
					if err := h.append(2, validationHistoryEvents(r, items[1:], 2, true), r.FinalizedAt); err != nil {
						t.Fatal(err)
					}
					items[1].Ordinal = 3
					events = validationHistoryEvents(r, items[1:], 3, false)
				case "unsafe-payload":
					events = validationHistoryEvents(r, items[1:], 2, false)
					events[0].Note = "provider secret/path must not persist"
				}
				before := h.catalog.Index
				if err := h.append(before+1, events, r.FinalizedAt); !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("accepted invalid result", err)
				}
				if h.catalog.Index != before {
					t.Fatal("failed publication advanced watermark")
				}
				if disk {
					db := h.databases[time.Date(2026, 9, 20, 11, 20, 0, 0, time.UTC).Format("2006-01-02")]
					if err := db.View(func(tx *bolt.Tx) error {
						p, err := decodeCollectionValidationHistoryProgress(tx.Bucket(collectionValidationHistoryBucket).Bucket([]byte(r.Header.OperationID)).Get(collectionValidationHistoryProgressKey))
						if err != nil {
							return err
						}
						want := uint64(1)
						if mode == "after-seal" {
							want = 2
						}
						if p.Count != want {
							return fmt.Errorf("partial batch escaped")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				} else {
					want := uint64(1)
					if mode == "after-seal" {
						want = 2
					}
					if h.memoryValidationResults[r.Header.OperationID].progress.Count != want {
						t.Fatal("partial memory batch escaped")
					}
				}
			})
		}
	}
}

func TestCollectionValidationHistoryMissingEvidenceIsUnavailable(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			h := collectionHistoryFixture(t, disk)
			r, _ := validationHistoryFixture(t, 1, false)
			if _, err := h.collectionValidationPage(context.Background(), r, 0, 0, 100, r.FinalizedAt.Add(25*time.Hour)); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("missing unexpired result misclassified", err)
			}
		})
	}
}

func TestCollectionValidationHistoryCorruptionAndStartup(t *testing.T) {
	for _, mode := range []string{"missing-index", "missing-primary", "missing-row", "corrupt-progress", "oversized-row", "unknown-json", "missing-summary", "strip-payload"} {
		t.Run(mode, func(t *testing.T) {
			h := collectionHistoryFixture(t, true)
			r, items := validationHistoryFixture(t, 2, false)
			index := validationHistoryPublish(t, h, r, items)
			db := h.databases[r.FinalizedAt.Format("2006-01-02")]
			if err := db.Update(func(tx *bolt.Tx) error {
				root := tx.Bucket(collectionValidationHistoryBucket)
				b := root.Bucket([]byte(r.Header.OperationID))
				rows := b.Bucket(collectionValidationHistoryItemsKey)
				raw := bytes.Clone(rows.Get(collectionOrdinal(1)))
				e, err := decodeCollectionValidationHistoryEvent(raw)
				if err != nil {
					return err
				}
				primary := tx.Bucket([]byte("events"))
				key := []byte(e.MonitorID + "\x00" + e.ID)
				switch mode {
				case "missing-index":
					return tx.DeleteBucket(collectionValidationHistoryBucket)
				case "missing-primary":
					return primary.Delete(key)
				case "missing-row":
					return rows.Delete(collectionOrdinal(1))
				case "corrupt-progress":
					return b.Put(collectionValidationHistoryProgressKey, []byte("{}"))
				case "oversized-row":
					return rows.Put(collectionOrdinal(1), bytes.Repeat([]byte("x"), maxCollectionValidationHistoryEventBytes+1))
				case "unknown-json":
					return rows.Put(collectionOrdinal(1), append([]byte(`{"unknown":true,`), raw[1:]...))
				case "missing-summary":
					return b.Delete(collectionValidationHistorySummaryKey)
				case "strip-payload":
					e.CollectionValidation = nil
					raw, _ = json.Marshal(e)
					if err := primary.Put(key, raw); err != nil {
						return err
					}
					return rows.Put(collectionOrdinal(1), raw)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.collectionValidationPage(context.Background(), r, index, 0, 100, r.FinalizedAt); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("corruption admitted", err)
			}
			if err := validateHistoryOperations(db); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("startup accepted corruption", err)
			}
		})
	}
}

func TestCollectionValidationHistoryCanceledReadAndSafePayload(t *testing.T) {
	h := collectionHistoryFixture(t, false)
	r, items := validationHistoryFixture(t, 1, false)
	index := validationHistoryPublish(t, h, r, items)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.collectionValidationPage(ctx, r, index, 0, 100, r.FinalizedAt); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, events := range [][]Event{validationHistoryEvents(r, items, 1, true)} {
		for _, e := range events {
			raw, _ := json.Marshal(e)
			for _, private := range []string{"ciphertext", "credential", "source_path", "source_fingerprint", "token", "provider_error"} {
				if bytes.Contains(raw, []byte(private)) {
					t.Fatal("private field", private)
				}
			}
		}
	}
	changed := r
	changed.Header.Authority.Actor = "another-operator"
	if _, err := h.collectionValidationPage(context.Background(), changed, index, 0, 100, r.FinalizedAt); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("wrong expected original receipt", err)
	}
	if _, err := h.collectionValidationPage(context.Background(), r, index, 0, 501, r.FinalizedAt); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("unbounded limit", err)
	}
}

func TestCollectionValidationHistoryReplayBeforeWatermarkAndTimeZones(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			h := collectionHistoryFixture(t, disk)
			r, items := validationHistoryFixture(t, 2, false)
			events := validationHistoryEvents(r, items, 1, true)
			if err := h.append(1, events, r.FinalizedAt); err != nil {
				t.Fatal(err)
			}
			// Model the committed segment being ahead of the history watermark after
			// interruption, without inventing different event IDs or mutating payloads.
			h.mu.Lock()
			h.catalog.Index = 0
			h.mu.Unlock()
			if _, err := h.collectionValidationPage(context.Background(), r, 0, 0, 100, r.FinalizedAt); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("ahead-of-watermark data published", err)
			}
			if err := h.append(1, events, r.FinalizedAt); err != nil {
				t.Fatal("exact replay", err)
			}
			original := r
			r.FinalizedAt = r.FinalizedAt.In(time.FixedZone("other", 19800))
			page, err := h.collectionValidationPage(context.Background(), r, 1, 0, 100, r.FinalizedAt)
			if err != nil || !reflect.DeepEqual(page.Items, items) || !collectionValidationReceiptsEqual(page.Receipt, original) {
				t.Fatal("instant equality / exact replay", err)
			}
			if disk {
				if err := validateHistoryOperations(h.databases[original.FinalizedAt.Format("2006-01-02")]); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCollectionValidationHistoryCancellationWhileLocked(t *testing.T) {
	h := collectionHistoryFixture(t, false)
	r, items := validationHistoryFixture(t, 1, false)
	index := validationHistoryPublish(t, h, r, items)
	if _, err := h.collectionValidationPage(nil, r, index, 0, 100, r.FinalizedAt); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("nil context", err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := h.collectionValidationPage(ctx, r, index, 0, 100, r.FinalizedAt); done <- err }()
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		t.Fatal("read did not wait on owned lock")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked behind history owner")
	}
}

func TestCollectionValidationHistoryRealWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only descriptor fixture is qualified on Unix")
	}
	h := collectionHistoryFixture(t, true)
	r, items := validationHistoryFixture(t, 2, false)
	if err := h.append(1, validationHistoryEvents(r, items[:1], 1, false), r.FinalizedAt); err != nil {
		t.Fatal(err)
	}
	day := r.FinalizedAt.Format("2006-01-02")
	path := h.databases[day].Path()
	if err := h.databases[day].Close(); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, InitialMmapSize: 16 << 20, OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(name) }})
	if err != nil {
		t.Fatal(err)
	}
	h.databases[day] = db
	err = h.append(2, validationHistoryEvents(r, items[1:], 2, true), r.FinalizedAt)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != path {
		t.Fatal("not an actual failed OS write", err)
	}
	if h.catalog.Index != 1 {
		t.Fatal("failed seal advanced watermark")
	}
	if _, err := h.collectionValidationPage(context.Background(), r, 1, 0, 100, r.FinalizedAt); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("failed history admitted reads", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h.databases[day] = db
	if err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(collectionValidationHistoryBucket).Bucket([]byte(r.Header.OperationID))
		p, err := decodeCollectionValidationHistoryProgress(b.Get(collectionValidationHistoryProgressKey))
		if err != nil {
			return err
		}
		if p.Count != 1 || p.SummaryEvent != "" || b.Get(collectionValidationHistorySummaryKey) != nil {
			return errors.New("failed write committed seal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateHistoryOperations(db); err != nil {
		t.Fatal("failed write corrupted prior prefix", err)
	}
}
