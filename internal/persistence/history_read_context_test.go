package persistence

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"sync"
	"testing"
	"time"
)

func TestHistoryViewHonorsReaderLockDeadline(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	now := time.Now().UTC()
	if err := h.append(1, []Event{{ID: fmt.Sprintf("%020d:%08d", 1, 0), MonitorID: "service", At: now, Type: "incident_opened"}}, now); err != nil {
		t.Fatal(err)
	}
	view, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	var once sync.Once
	unlock := func() { once.Do(h.mu.Unlock) }
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	finished, result := make(chan struct{}), make(chan error, 1)
	t.Cleanup(func() { cancel(); unlock(); <-finished })
	go func() { defer close(finished); _, _, err := view.Page(ctx, "service", "", 100); result <- err }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("history lock wait lost deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("history reader waited past deadline")
	}
	unlock()
	rows, next, err := view.Page(t.Context(), "service", "", 100)
	if err != nil || len(rows) != 1 || next != "" {
		t.Fatal("deadline changed retained history", err, len(rows), next)
	}
}

func TestHistoryContextSnapshotsAndV1PagesHonorLockDeadline(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	for _, operation := range []struct {
		name string
		read func(context.Context) error
	}{
		{"snapshot", func(ctx context.Context) error { _, err := h.SnapshotContext(ctx); return err }},
		{"v1-page", func(ctx context.Context) error { _, err := h.PageContext(ctx, "service", "", 100); return err }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			h.mu.Lock()
			var once sync.Once
			unlock := func() { once.Do(h.mu.Unlock) }
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			finished, result := make(chan struct{}), make(chan error, 1)
			t.Cleanup(func() { cancel(); unlock(); <-finished })
			go func() { defer close(finished); result <- operation.read(ctx) }()
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("history read lost deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("history read waited past deadline")
			}
			unlock()
			if err := operation.read(t.Context()); err != nil {
				t.Fatal("deadline prevented later history read", err)
			}
		})
	}
}

func TestHistoryV1BoundedSparsePagesAndFrozenUpper(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	now := time.Now().UTC()
	events := make([]Event, 20003)
	for i := range events {
		id := fmt.Sprintf("%020d:%08d", i+1, 0)
		events[i] = Event{ID: id, MonitorID: "other", At: now, Type: "incident_opened"}
	}
	events[10001].MonitorID, events[20002].MonitorID = "service", "service"
	if err := h.append(20003, events, now); err != nil {
		t.Fatal(err)
	}
	page, err := h.PageContext(t.Context(), "service", "", 1)
	if err != nil || len(page.Events) != 0 || page.NextCursor == "" {
		t.Fatal("sparse first page did not expose bounded continuation", err, len(page.Events))
	}
	if err := h.append(20004, []Event{{ID: fmt.Sprintf("%020d:%08d", 20004, 0), MonitorID: "service", At: now, Type: "later"}}, now); err != nil {
		t.Fatal(err)
	}
	cursor := page.NextCursor
	seen := map[string]bool{}
	for pages := 0; cursor != ""; pages++ {
		if pages > 5 {
			t.Fatal("bounded continuation did not finish")
		}
		page, err = h.PageContext(t.Context(), "service", cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if seen[e.ID] || e.Type == "later" {
				t.Fatal("v1 page repeated or included newer event")
			}
			seen[e.ID] = true
		}
		if cursor == page.NextCursor {
			t.Fatal("v1 cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 2 {
		t.Fatal("sparse pages lost events", len(seen))
	}
}

func TestHistoryContextCancellationDuringScanReturnsNoPartialPage(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			dir := ""
			if persistent {
				dir = t.TempDir()
			}
			h, err := openHistory(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.Close() })
			now := time.Now().UTC()
			events := make([]Event, 20)
			for i := range events {
				events[i] = Event{ID: fmt.Sprintf("%020d:%08d", i+1, 0), MonitorID: "service", At: now, Type: "incident_opened"}
			}
			if err := h.append(20, events, now); err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := &controllerReadCancelContext{Context: parent, cancel: cancel, remaining: 8}
			page, err := h.PageContext(ctx, "service", "", 100)
			if !errors.Is(err, context.Canceled) || len(page.Events) != 0 || page.NextCursor != "" {
				t.Fatal("cancelled scan returned partial events", err, len(page.Events), page.NextCursor)
			}
			page, err = h.PageContext(t.Context(), "service", "", 100)
			if err != nil || len(page.Events) != 20 {
				t.Fatal("cancelled scan altered retained events", err, len(page.Events))
			}
		})
	}
}

func TestHistoryV1CursorBoundsAndUnreadNewerRows(t *testing.T) {
	h, err := openHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	now := time.Now().UTC()
	for i := 1; i <= 3; i++ {
		if err := h.append(uint64(i), []Event{{ID: fmt.Sprintf("%020d:%08d", i, 0), MonitorID: "service", At: now, Type: "incident_opened"}}, now); err != nil {
			t.Fatal(err)
		}
	}
	first, err := h.PageContext(t.Context(), "service", "", 1)
	if err != nil || len(first.Events) != 1 || first.NextCursor == "" {
		t.Fatal("fixture did not create cursor", err)
	}
	newer := fmt.Sprintf("%020d:%08d", 4, 0)
	if err := h.append(4, []Event{{ID: newer, MonitorID: "service", At: now, Type: "later"}}, now); err != nil {
		t.Fatal(err)
	}
	// A row past the frozen bound must not be decoded by this older cursor.
	db := h.databases[now.Format("2006-01-02")]
	if err := db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("events")).Put([]byte("service\x00"+newer), []byte("invalid-json-canary"))
	}); err != nil {
		t.Fatal(err)
	}
	rest, err := h.PageContext(t.Context(), "service", first.NextCursor, 100)
	if err != nil || len(rest.Events) != 2 || rest.NextCursor != "" {
		t.Fatal("frozen page inspected newer row or lost older events", err, len(rest.Events))
	}
	for _, cursor := range []historyCursor{
		{MonitorID: "other", Upper: fmt.Sprintf("%020d:%08d", 3, 99999999)},
		{MonitorID: "service", Upper: fmt.Sprintf("%020d:%08d", 5, 99999999)},
		{MonitorID: "service", After: "malformed", Upper: fmt.Sprintf("%020d:%08d", 3, 99999999)},
		{MonitorID: "service", After: newer, Upper: fmt.Sprintf("%020d:%08d", 3, 99999999)},
	} {
		raw, _ := json.Marshal(cursor)
		page, err := h.PageContext(t.Context(), "service", base64.RawURLEncoding.EncodeToString(raw), 100)
		if err == nil || len(page.Events) != 0 || page.NextCursor != "" {
			t.Fatal("invalid cursor returned data", err)
		}
	}
}
