package persistence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// moveHistorySegmentFixture keeps the DB readable while removing the path that
// the catalog promises. Close it before moving because Windows locks mappings.
func moveHistorySegmentFixture(t *testing.T, h *HistoryStore, at time.Time) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	day := at.UTC().Format("2006-01-02")
	db := h.databases[day]
	if db == nil {
		t.Fatal("history fixture has no open segment")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.dir, day+".db")
	if err := os.Rename(path, path+".missing"); err != nil {
		t.Fatal(err)
	}
	reopened, err := bolt.Open(path+".missing", 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h.databases[day] = reopened
}

func TestHistoryPaginationInsertionRetentionAndReopen(t *testing.T) {
	dir := t.TempDir()
	h, err := openHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -31)
	events := []Event{{ID: "00000000000000000001:00000000", MonitorID: "m", At: old, Type: "incident_opened"}, {ID: "00000000000000000001:00000001", MonitorID: "m", At: now, Type: "incident_closed"}}
	if err = h.append(1, events, now); err != nil {
		t.Fatal(err)
	}
	first, err := h.Page("m", "", 1)
	if err != nil || len(first.Events) != 1 || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	if err = h.append(2, []Event{{ID: "00000000000000000002:00000000", MonitorID: "m", At: now, Type: "action_started"}}, now); err != nil {
		t.Fatal(err)
	}
	second, err := h.Page("m", first.NextCursor, 1)
	if err != nil || len(second.Events) != 1 || second.NextCursor != "" {
		t.Fatal("cursor admitted newer insert", second, err)
	}
	if err = h.Expire(now); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, old.Format("2006-01-02")+".db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired segment retained", err)
	}
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	h, err = openHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err = h.append(2, events, now); err != nil {
		t.Fatal(err)
	}
	page, err := h.Page("m", "", 100)
	if err != nil || len(page.Events) != 2 {
		t.Fatal("history lost or replayed duplicates", page, err)
	}
}

func TestMissingHistorySegmentPreventsRecovery(t *testing.T) {
	dir := t.TempDir()
	h, err := openHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = h.append(1, []Event{{ID: "1", MonitorID: "m", At: now}}, now); err != nil {
		t.Fatal(err)
	}
	h.Close()
	if err = os.Remove(filepath.Join(dir, now.Format("2006-01-02")+".db")); err != nil {
		t.Fatal(err)
	}
	if _, err = openHistory(dir); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("incomplete backup accepted", err)
	}
}
