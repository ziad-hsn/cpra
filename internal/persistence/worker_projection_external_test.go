//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func requireWorkerProjection(t *testing.T, s *Store, cursor ControlCursor, id string) ControlCursor {
	t.Helper()
	changes, err := s.ControlsChangedSinceContext(t.Context(), cursor, 100)
	if err != nil || changes.More || len(changes.Changes) != 1 || changes.Changes[0].MonitorID != id {
		t.Fatalf("committed worker projection: %+v, %v", changes, err)
	}
	current, found, err := s.GetContext(t.Context(), id)
	if err != nil || !found || changes.Changes[0] != changeOf(current) {
		t.Fatal("projection does not identify current committed monitor", err)
	}
	raw, err := json.Marshal(changes.Changes)
	if err != nil || bytes.Contains(raw, []byte("private-worker")) || bytes.Contains(raw, []byte("ciphertext")) {
		t.Fatal("projection hint leaked protected data", err)
	}
	return changes.Next
}

func requireWorkerProjectionView(t *testing.T, s *Store, id string) ControlView {
	t.Helper()
	view, err := s.ControlSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for after := ""; ; {
		rows, next, err := view.Page(after, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.MonitorID == id {
				return view
			}
		}
		if next == "" {
			t.Fatal("owner snapshot omitted retained external monitor", id)
		}
		after = next
	}
}

func TestWorkerProjectionCommittedResultsAndDuplicates(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		for _, missing := range []bool{false, true} {
			name := category + "/result"
			if missing {
				name = category + "/missing"
			}
			t.Run(name, func(t *testing.T) {
				status := map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category]
				s, authority, _, input := workerResultFixture(t, category, status)
				record, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
				before, err := s.ControlSnapshotContext(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if missing {
					if _, err := s.FinalizeWorkerExecutions(t.Context(), record.Intent.Deadline, 1); err != nil {
						t.Fatal(err)
					}
				} else if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
					t.Fatal(err)
				}
				cursor := requireWorkerProjection(t, s, before.Cursor, record.Intent.MonitorID)
				requireWorkerProjectionView(t, s, record.Intent.MonitorID)
				if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
					t.Fatal(err)
				}
				if missing {
					if _, err := s.FinalizeWorkerExecutions(t.Context(), record.Intent.Deadline.Add(time.Second), 1); err != nil {
						t.Fatal(err)
					}
				}
				changes, err := s.ControlsChangedSinceContext(t.Context(), cursor, 100)
				if err != nil || len(changes.Changes) != 0 {
					t.Fatal("duplicate outcome published a new projection", err)
				}
			})
		}
	}
}

func TestWorkerProjectionStartedAndUnstartedActions(t *testing.T) {
	for _, category := range []string{"recovery", "notification"} {
		for _, start := range []bool{false, true} {
			t.Run(category+map[bool]string{false: "/queued", true: "/started"}[start], func(t *testing.T) {
				s, authority, poll, intent, _ := workerOfferFixture(t, category)
				view, err := s.ControlSnapshotContext(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				cursor := view.Cursor
				if start {
					offer, err := s.CommitWorkerPoll(t.Context(), authority, poll)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.CommitWorkerStart(t.Context(), authority, workerStartFromOffer(offer)); err != nil {
						t.Fatal(err)
					}
					cursor = requireWorkerProjection(t, s, cursor, intent.MonitorID)
				}
				if _, err := s.FinalizeWorkerExecutions(t.Context(), intent.Deadline, 1); err != nil {
					t.Fatal(err)
				}
				requireWorkerProjection(t, s, cursor, intent.MonitorID)
				want := Cancelled
				if start {
					want = Unknown
				}
				if retainedAction(t, s, intent.ActionID).State != want {
					t.Fatal("unexpected action disposition")
				}
			})
		}
	}
}

func TestWorkerProjectionRebuildAndImmutableView(t *testing.T) {
	s, authority, _, input := workerResultFixture(t, "check", "success")
	record, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	before, err := s.ControlSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
		t.Fatal(err)
	}
	rows, _, err := before.Page("", 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("published outcome mutated an earlier immutable view", err)
	}
	m, _ := s.Get(record.Intent.MonitorID)
	if m.Incident || m.ControlRevision != identity("control/"+m.ID+"/"+m.CatalogUID) {
		t.Fatal("fixture must remain healthy with initial controls")
	}
	s.fsm.mu.Lock()
	s.fsm.controlChanges, s.fsm.controlSequence = nil, 0
	s.fsm.controlEpoch++
	err = s.fsm.rebuildControlIndexesContext(t.Context())
	s.fsm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlsChangedSinceContext(t.Context(), before.Cursor, 100); !errors.Is(err, ErrControlChangeGap) {
		t.Fatal("old projection cursor survived index replacement", err)
	}
	requireWorkerProjectionView(t, s, m.ID)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.fsm.mu.Lock()
	err = s.fsm.rebuildControlIndexesContext(ctx)
	cleared := s.fsm.controls == nil
	s.fsm.mu.Unlock()
	if !errors.Is(err, context.Canceled) || !cleared {
		t.Fatal("canceled rebuild published partial index", err)
	}
	requireWorkerProjectionView(t, s, m.ID)
}

func TestWorkerProjectionDoesNotDependOnCheckReceiptRetention(t *testing.T) {
	for _, status := range []string{"success", "noData"} {
		t.Run(status, func(t *testing.T) {
			s, authority, _, input := workerResultFixture(t, "check", status)
			if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
				t.Fatal(err)
			}
			record, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			m, _ := s.Get(record.Intent.MonitorID)
			if m.Generation != record.Intent.Generation || m.Incident || m.IncidentID != "" || m.ControlRevision != identity("control/"+m.ID+"/"+m.CatalogUID) {
				t.Fatal("fixture must have a finalized generation and initial controls")
			}
			// Exercise derived-index reconstruction without any retained receipt.
			// This does not simulate or qualify the pending retirement command.
			key := (CatalogKey{Kind: "Monitor", ID: m.ID}).indexKey()
			source := s.fsm.image.Catalog[key].Clone()
			for _, variant := range []string{"current", "missing", "foreign-incarnation", "removed", "builtin", "unobserved"} {
				t.Run(variant, func(t *testing.T) {
					candidate, monitor := source.Clone(), m.Clone()
					switch variant {
					case "foreign-incarnation":
						candidate.UID = "replacement-incarnation"
					case "removed":
						candidate.Removed = true
					case "builtin":
						candidate.JobTypeReferences = nil
					case "unobserved":
						monitor.Generation = 0
					}
					view := &machine{image: image{Monitors: map[string]Monitor{m.ID: monitor}, Catalog: map[string]CatalogRecord{key: candidate}}}
					if variant == "missing" {
						delete(view.image.Catalog, key)
					}
					if err := view.rebuildControlIndexesContext(t.Context()); err != nil {
						t.Fatal(err)
					}
					change, found := view.controls.Get(ControlChange{MonitorID: m.ID})
					if found != (variant == "current") || found && change != changeOf(m) {
						t.Fatal("receipt-independent monitor projection", variant, found)
					}
				})
			}
		})
	}
}
