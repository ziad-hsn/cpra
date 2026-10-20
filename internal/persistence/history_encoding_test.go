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

	bolt "go.etcd.io/bbolt"
)

func historicalHistoryEvent(index int, at time.Time) Event {
	return Event{ID: fmt.Sprintf("%020d:%08d", index, 0), MonitorID: "service", At: at,
		Type: "action_result", ActionID: "original-action", Revision: strings.Repeat("r", (2<<20)+1024)}
}

func TestHistoryRevisionExceptionDoesNotExpandOtherPayloads(t *testing.T) {
	e := historicalHistoryEvent(1, time.Now().UTC())
	raw, err := encodeHistoryEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeHistoryEvent(raw)
	if err != nil || !reflect.DeepEqual(e, decoded) {
		t.Fatal("original historical event changed", err)
	}
	marshal := func(event Event) []byte {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	largeBody := e
	largeBody.Note = strings.Repeat("n", maxHistoryEventBytes)
	cases := map[string][]byte{
		"empty":           nil,
		"frame-overflow":  bytes.Repeat([]byte{' '}, actionLedgerMaxFrame+1),
		"ordinary-body":   marshal(largeBody),
		"unknown-field":   append([]byte(`{"ignored":"inflation",`), raw[1:]...),
		"duplicate-field": append([]byte(`{"revision":"discarded",`), raw[1:]...),
		"padding":         append([]byte{' '}, raw...),
	}
	for name, set := range map[string]func(*Event){
		"collection-kind":       func(e *Event) { e.Kind = "Collection" },
		"collection-activation": func(e *Event) { e.Type = "collection_activation_admitted" },
		"collection-namespace":  func(e *Event) { e.MonitorID = "collection/old" },
		"operation":             func(e *Event) { e.Operation = &OperationReceipt{} },
		"collection":            func(e *Event) { e.Collection = &CollectionReceipt{} },
		"validation":            func(e *Event) { e.CollectionValidation = &CollectionValidationHistory{} },
		"execution":             func(e *Event) { e.CollectionExecution = &CollectionExecutionSummary{} },
		"execution-history":     func(e *Event) { e.CollectionExecutionHistory = &CollectionExecutionHistory{} },
	} {
		copy := e
		set(&copy)
		cases[name] = marshal(copy)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeHistoryEvent(data); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("oversized non-revision payload accepted", err)
			}
		})
	}
	// Do not impose canonical encoding on normal-sized existing records.
	if _, err := decodeHistoryEvent([]byte(` {"id":"old","revision":"r","unknown":true} `)); err != nil {
		t.Fatal("normal historical encoding rejected", err)
	}
	for _, disk := range []bool{false, true} {
		dir := ""
		if disk {
			dir = t.TempDir()
		}
		h, err := openHistory(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.append(1, []Event{largeBody}, e.At); !errors.Is(err, ErrHistoryUnavailable) {
			t.Fatal("append accepted data that restart rejects", disk, err)
		}
		_ = h.Close()
	}
}

func TestHistoryLargeRevisionPagesMergeDailyFiles(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			dir := ""
			if disk {
				dir = t.TempDir()
			}
			h, err := openHistory(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
			for i := 1; i <= 5; i++ {
				e := historicalHistoryEvent(i, now.Add(-time.Duration(i%3)*24*time.Hour))
				if err := h.append(uint64(i), []Event{e}, now); err != nil {
					t.Fatal(err)
				}
			}
			if disk {
				if err := h.Close(); err != nil {
					t.Fatal(err)
				}
				h, err = openHistory(dir)
				if err != nil {
					t.Fatal("startup rejected writer-admitted history", err)
				}
				defer h.Close()
			}
			view, err := h.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if disk {
				// The frozen view must not decode a later corrupt row.
				for _, db := range h.databases {
					if err := db.Update(func(tx *bolt.Tx) error {
						return tx.Bucket([]byte("events")).Put([]byte("service\x00"+fmt.Sprintf("%020d:%08d", 6, 0)), []byte("bad"))
					}); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			for _, version := range []int{1, 2} {
				cursor, count := "", 0
				for {
					read := func() ([]Event, string, error) {
						if version == 2 {
							return view.Page(t.Context(), "service", cursor, 500)
						}
						p, err := h.Page("service", cursor, 500)
						return p.Events, p.NextCursor, err
					}
					rows, next, err := read()
					if err != nil || len(rows) != 1 {
						t.Fatal("byte-bounded page", version, len(rows), err)
					}
					again, retry, err := read()
					if err != nil || retry != next || !reflect.DeepEqual(rows, again) {
						t.Fatal("retry changed", err)
					}
					count++
					if rows[0].ID != fmt.Sprintf("%020d:%08d", count, 0) || rows[0].Revision != historicalHistoryEvent(count, now).Revision {
						t.Fatal("daily merge skipped, duplicated or changed a record")
					}
					if next == "" {
						break
					}
					if next == cursor || count > 5 {
						t.Fatal("pagination did not advance")
					}
					cursor = next
				}
				if count != 5 {
					t.Fatal("lost history", count)
				}
			}
		})
	}
}

func TestHistoryExpiredLargeRowsConsumePageBudget(t *testing.T) {
	h, err := openHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	cutoff := now.AddDate(0, 0, -30)
	rows := []Event{historicalHistoryEvent(1, cutoff.Add(-time.Hour)), historicalHistoryEvent(2, now), historicalHistoryEvent(3, cutoff.Add(-time.Hour)), historicalHistoryEvent(4, now.Add(-24*time.Hour))}
	if err := h.append(4, rows, now); err != nil {
		t.Fatal(err)
	}
	if err := h.Expire(now); err != nil {
		t.Fatal(err)
	}
	v, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	cursor := ""
	for i := 1; i <= 4; i++ {
		page, next, err := v.Page(t.Context(), "service", cursor, 100)
		if err != nil || len(page) != (i+1)%2 {
			t.Fatal("expired scan budget", i, len(page), err)
		}
		if len(page) == 1 && page[0].ID != rows[i-1].ID {
			t.Fatal("unordered retained event")
		}
		if i < 4 && next != rows[i-1].ID || i == 4 && next != "" {
			t.Fatal("unsafe continuation", i, next)
		}
		cursor = next
	}
}

func TestHistoryIndexMigrationBoundsLargeRevisionBytes(t *testing.T) {
	h, err := openHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now().UTC()
	rows := []Event{historicalHistoryEvent(1, now), historicalHistoryEvent(2, now)}
	if err := h.append(2, rows, now); err != nil {
		t.Fatal(err)
	}
	for _, db := range h.databases {
		if err := db.Update(func(tx *bolt.Tx) error {
			for _, name := range [][]byte{operationIndexMeta, operationTerminalBucket, operationMonitorBucket} {
				if err := tx.DeleteBucket(name); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if done, err := migrateOperationIndexPage(context.Background(), db); err != nil || done {
			t.Fatal("migration exceeded byte budget", done, err)
		}
		if err := db.View(func(tx *bolt.Tx) error {
			if string(tx.Bucket(operationIndexMeta).Get([]byte("after"))) != "service\x00"+rows[0].ID {
				t.Fatal("wrong migration frontier")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if done, err := migrateOperationIndexPage(context.Background(), db); err != nil || !done {
			t.Fatal("migration failed to finish", done, err)
		}
	}
}

func TestHistoryMemoryFullPageDoesNotInspectNextRow(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now().UTC()
	rows := []Event{historicalHistoryEvent(1, now), historicalHistoryEvent(2, now)}
	if err := h.append(2, rows, now); err != nil {
		t.Fatal(err)
	}
	view, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	delete(h.memorySizes, rows[1].ID)
	page, next, err := view.Page(t.Context(), "service", "", 1)
	if err != nil || len(page) != 1 || next != rows[0].ID {
		t.Fatal("full page inspected following record", len(page), next, err)
	}
	if _, _, err := view.Page(t.Context(), "service", next, 1); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("missing size not detected on its own page", err)
	}
}

func TestHistoryMoreThan64DailySegmentsRemainReadable(t *testing.T) {
	h, err := openHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now().UTC()
	rows := make([]Event, 65)
	for i := range rows {
		rows[i] = Event{ID: fmt.Sprintf("%020d:%08d", i+1, 0), MonitorID: "service", At: now.Add(time.Duration(i) * 24 * time.Hour), Type: "event"}
	}
	// Future-dated history can remain after the host clock moves backward.
	if err := h.append(65, rows, now); err != nil {
		t.Fatal(err)
	}
	view, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	page, next, err := view.Page(t.Context(), "service", "", 100)
	if err != nil || next != "" || !reflect.DeepEqual(page, rows) {
		t.Fatal("existing segment inventory became unreadable", len(page), next, err)
	}
}
