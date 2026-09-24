package persistence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryRetentionObservationCommittedMonotonicAndReopen(t *testing.T) {
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
			defer func() { _ = h.Close() }()
			at := time.Date(2600, 1, 1, 0, 0, 0, 0, time.UTC) // No UnixNano range assumption.
			if h.retentionCutoffReached(at) {
				t.Fatal("new history has a retention decision")
			}
			if err := h.Expire(at.AddDate(0, 0, 30)); err != nil {
				t.Fatal(err)
			}
			if !h.retentionCutoffReached(at) || h.retentionCutoffReached(at.Add(time.Nanosecond)) {
				t.Fatal("committed cutoff not published exactly")
			}
			if err := h.Expire(at); err != nil {
				t.Fatal(err)
			}
			h.publishRetentionCutoff(at.Add(-time.Second))
			if !h.retentionCutoffReached(at) {
				t.Fatal("backward clock regressed cutoff")
			}
			if disk {
				if err := h.Close(); err != nil {
					t.Fatal(err)
				}
				h, err = openHistory(dir)
				if err != nil || !h.retentionCutoffReached(at) {
					t.Fatal("reopen lost committed cutoff", err)
				}
			}
		})
	}
}

func TestHistoryRetentionObservationFailedSaveDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	h, err := openHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	// Force the atomic catalog rename to fail without modifying real user data.
	if err := os.Mkdir(filepath.Join(dir, "catalog.json"), 0700); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := h.Expire(at.AddDate(0, 0, 30)); err == nil {
		t.Fatal("fixture did not fail catalog save")
	}
	if h.retentionCutoffReached(at) || h.retainedCutoff.Load() != nil {
		t.Fatal("failed durable save published retention")
	}
}

func TestCollectionExecutionResultRecheckGlobalRetentionAfterPage(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 1)
			head = executionResultViewPublish(t, s, head)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			view, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			page, err := view.Page(t.Context(), 0, 100, at)
			if err != nil || len(page.Items) != 1 {
				t.Fatal(err)
			}
			h := s.History()
			if err := h.Expire(head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 31)); err != nil {
				t.Fatal(err)
			}
			s.fsm.mu.RLock()
			expired := s.fsm.image.Collections[head.ID].ExecutionResult.HistoryExpiredAt
			s.fsm.mu.RUnlock()
			if !expired.IsZero() {
				t.Fatal("fixture unexpectedly set parent expiry")
			}
			// Simulate the final HTTP policy/cursor admission: history may now be
			// busy again, and the caller's supplied time precedes the new cutoff.
			h.mu.Lock()
			err = view.Recheck(t.Context(), at)
			h.mu.Unlock()
			if !errors.Is(err, ErrOperationExpired) {
				t.Fatal("post-page admission ignored global retention", err)
			}
		})
	}
}

func TestHistoryRetentionObservationMemoryAppendPublishes(t *testing.T) {
	h, err := openHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	at := time.Now().UTC()
	if err := h.append(1, nil, at); err != nil {
		t.Fatal(err)
	}
	if !h.retentionCutoffReached(at.AddDate(0, 0, -30)) {
		t.Fatal("memory append did not publish retention")
	}
}
