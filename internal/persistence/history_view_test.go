package persistence

import (
	"context"
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"testing"
	"time"
)

func TestHistoryViewFrozenPagingAndRetention(t *testing.T) {
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
			defer h.Close()
			now := time.Now().UTC()
			for i := 1; i <= 9; i++ {
				e := Event{ID: fmt.Sprintf("%020d:%08d", i, 0), MonitorID: "service", At: now.Add(-time.Duration(i%3) * 24 * time.Hour), Type: "control_acknowledge", Actor: "operator", Note: "Investigating"}
				if err := h.append(uint64(i), []Event{e}, now); err != nil {
					t.Fatal(err)
				}
			}
			view, err := h.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			first, next, err := view.Page(context.Background(), "service", "", 2)
			if err != nil || len(first) != 2 || next != first[1].ID {
				t.Fatalf("first: %+v %s %v", first, next, err)
			}
			if err := h.append(10, []Event{{ID: fmt.Sprintf("%020d:%08d", 10, 0), MonitorID: "service", At: now, Type: "later"}}, now); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			after := ""
			for {
				rows, n, err := view.Page(context.Background(), "service", after, 2)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range rows {
					if seen[e.ID] || e.Type == "later" {
						t.Fatal("duplicate or post-snapshot event")
					}
					seen[e.ID] = true
				}
				if n == "" {
					break
				}
				after = n
			}
			if len(seen) != 9 {
				t.Fatal("lost events", len(seen))
			}
			again, againNext, err := view.Page(context.Background(), "service", "", 2)
			if err != nil || againNext != next || again[0].ID != first[0].ID {
				t.Fatal("retry changed", err)
			}
			if err := h.Expire(now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := view.Page(context.Background(), "service", next, 2); !errors.Is(err, ErrHistoryCursorExpired) {
				t.Fatal("retention change silently changed view", err)
			}
		})
	}
}

func TestHistoryViewMemoryScanHasBoundedContinuation(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now().UTC()
	rows := make([]Event, 10001)
	for i := range rows {
		rows[i] = Event{ID: fmt.Sprintf("%020d:%08d", i+1, 0), MonitorID: "other", At: now, Type: "event"}
	}
	rows[len(rows)-1].MonitorID = "service"
	if err := h.append(10001, rows, now); err != nil {
		t.Fatal(err)
	}
	v, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	page, next, err := v.Page(context.Background(), "service", "", 100)
	if err != nil || len(page) != 0 || next == "" {
		t.Fatalf("missing empty-page continuation: %d %s %v", len(page), next, err)
	}
	page, next, err = v.Page(context.Background(), "service", next, 100)
	if err != nil || len(page) != 1 || next != "" {
		t.Fatal("scan lost target event", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := v.Page(ctx, "service", "", 100); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

func TestHistoryEvidenceOwnership(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now().UTC()
	e := Event{ID: fmt.Sprintf("%020d:%08d", 1, 0), MonitorID: "m", At: now, Type: "action_review", EvidenceRefs: []string{"evidence:original"}}
	if err := h.append(1, []Event{e}, now); err != nil {
		t.Fatal(err)
	}
	e.EvidenceRefs[0] = "caller-mutated"
	view, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := view.Page(context.Background(), "m", "", 100)
	if err != nil || len(rows) != 1 || rows[0].EvidenceRefs[0] != "evidence:original" {
		t.Fatalf("append ownership: %+v %v", rows, err)
	}
	rows[0].EvidenceRefs[0] = "view-mutated"
	legacy, err := h.Page("m", "", 100)
	if err != nil || legacy.Events[0].EvidenceRefs[0] != "evidence:original" {
		t.Fatalf("view ownership: %+v %v", legacy, err)
	}
	legacy.Events[0].EvidenceRefs[0] = "legacy-mutated"
	rows, _, err = view.Page(context.Background(), "m", "", 100)
	if err != nil || rows[0].EvidenceRefs[0] != "evidence:original" {
		t.Fatalf("legacy ownership: %+v %v", rows, err)
	}
}

func TestHistoryViewClosedAndMissingSegmentBucketAreUnavailable(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprint(closed), func(t *testing.T) {
			h, err := openHistory(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			now := time.Now().UTC()
			if err := h.append(1, []Event{{ID: fmt.Sprintf("%020d:%08d", 1, 0), MonitorID: "m", At: now, Type: "event"}}, now); err != nil {
				t.Fatal(err)
			}
			view, err := h.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if closed {
				if err := h.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, db := range h.databases {
					if err := db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("events")) }); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, _, err := view.Page(context.Background(), "m", "", 100); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("frozen view silently lost history", err)
			}
			if _, err := h.Page("m", "", 100); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("legacy page silently lost history", err)
			}
			if closed {
				if _, err := h.Snapshot(); !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("closed snapshot opened", err)
				}
				if err := h.Expire(now); !errors.Is(err, ErrHistoryUnavailable) {
					t.Fatal("closed retention allowed", err)
				}
			}
		})
	}
}

func TestHistoryViewDiskScanFrontierDoesNotSkipNewerSegment(t *testing.T) {
	h, err := openHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	cutoff := now.AddDate(0, 0, -30)
	events := make([]Event, 10001)
	for i := range events {
		events[i] = Event{ID: fmt.Sprintf("%020d:%08d", i+1, 0), MonitorID: "m", At: cutoff.Add(-time.Hour), Type: "expired"}
	}
	events[10000].At = now
	events[10000].Type = "retained"
	if err := h.append(10001, events, now); err != nil {
		t.Fatal(err)
	}
	if err := h.Expire(now); err != nil {
		t.Fatal(err)
	}
	view, err := h.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rows, next, err := view.Page(context.Background(), "m", "", 100)
	if err != nil || len(rows) != 0 || next == "" {
		t.Fatalf("expected bounded empty frontier: %d %s %v", len(rows), next, err)
	}
	rows, next, err = view.Page(context.Background(), "m", next, 100)
	if err != nil || len(rows) != 1 || rows[0].Type != "retained" || next != "" {
		t.Fatalf("frontier skipped later segment: %+v %s %v", rows, next, err)
	}
}
