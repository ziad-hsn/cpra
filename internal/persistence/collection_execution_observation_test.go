package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func executionObservationListed(t *testing.T, s *Store, head CollectionState, at time.Time) CollectionReceipt {
	t.Helper()
	view, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	rows, next, err := view.Page(t.Context(), "", "", 100)
	if err != nil || next != "" {
		t.Fatal(next, err)
	}
	var found []CollectionReceipt
	for _, row := range rows {
		if row.Collection != nil && row.Collection.ID == head.ID {
			found = append(found, *row.Collection)
		}
	}
	if len(found) != 1 {
		t.Fatal("missing/duplicate collection observation", len(found))
	}
	return found[0]
}

func TestCollectionExecutionObservationPointListAndHistoricalBytes(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 3)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			for _, sealed := range []bool{false, true} {
				if sealed {
					head = executionResultViewPublish(t, s, head)
				}
				point, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, at)
				want := map[bool]string{false: "pending", true: "ready"}[sealed]
				if err != nil || point.ExecutionObservation == nil || point.ExecutionObservation.State != want ||
					point.Phase != "canceled" || point.ExecutionObservation.Counts.Unattempted != 3 ||
					!reflect.DeepEqual(point, executionObservationListed(t, s, head, at)) {
					t.Fatal(point, err)
				}
				original, err := json.Marshal(collectionReceiptFor(head))
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(point)
				if err != nil || !bytes.Equal(original, encoded) {
					t.Fatal("read observation changed historical bytes", err)
				}
				cloned := point.Clone()
				cloned.ExecutionObservation.Summary.Fence.Progress.Processed++
				if point.ExecutionObservation.Summary.Fence.Progress.Processed != 0 {
					t.Fatal("clone aliases summary")
				}
				if cloned.ExecutionObservation.Descriptor != nil {
					cloned.ExecutionObservation.Descriptor.Count++
					if point.ExecutionObservation.Descriptor.Count != 3 {
						t.Fatal("clone aliases descriptor")
					}
				}
			}
			if err := s.History().Expire(head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 31)); err != nil {
				t.Fatal(err)
			}
			point, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, at)
			if err != nil || point.ExecutionObservation.State != "expired" || point.Phase != "canceled" ||
				!reflect.DeepEqual(point, executionObservationListed(t, s, head, at)) {
				t.Fatal("cutoff resurrected result", point, err)
			}
		})
	}
}

func TestCollectionExecutionObservationMissingAnchorUnavailable(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 1)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			h := s.History()
			h.mu.Lock()
			if disk {
				day := head.ExecutionResult.Summary.FinalizedAt.UTC().Format("2006-01-02")
				err := h.databases[day].Update(func(tx *bolt.Tx) error { return tx.Bucket(collectionExecutionAnchorBucket).Delete([]byte(head.ID)) })
				if err != nil {
					h.mu.Unlock()
					t.Fatal(err)
				}
			} else {
				delete(h.memoryExecutionAnchors, head.ID)
			}
			h.mu.Unlock()
			if _, err := s.CollectionOperationAs(t.Context(), head.ID, "foreign", at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("foreign owner accessed missing anchor", err)
			}
			if _, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, at); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("missing anchor became pending/expired", err)
			}
			view, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if rows, _, err := view.Page(t.Context(), "", "", 100); !errors.Is(err, ErrHistoryUnavailable) || len(rows) != 0 {
				t.Fatal("list hid missing anchor", err)
			}
		})
	}
}

func TestCollectionExecutionObservationWaitRechecksFences(t *testing.T) {
	for _, list := range []bool{false, true} {
		for _, scenario := range []string{"expiry", "history", "cancel"} {
			t.Run(fmt.Sprintf("list=%t/%s", list, scenario), func(t *testing.T) {
				s, head := executionPublicationFixture(t, false, 1)
				head = executionResultViewPublish(t, s, head)
				at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
				view, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				base, cancel := context.WithCancel(t.Context())
				defer cancel()
				ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
				history := s.History()
				history.mu.Lock()
				locked := true
				defer func() {
					if locked {
						history.mu.Unlock()
					}
				}()
				type outcome struct {
					receipt CollectionReceipt
					err     error
				}
				done := make(chan outcome, 1)
				go func() {
					if list {
						rows, _, err := view.Page(ctx, "", "", 100)
						var receipt CollectionReceipt
						for _, row := range rows {
							if row.Collection != nil && row.Collection.ID == head.ID {
								receipt = *row.Collection
							}
						}
						done <- outcome{receipt, err}
					} else {
						receipt, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at)
						done <- outcome{receipt, err}
					}
				}()
				select {
				case <-ctx.waiting:
				case <-time.After(time.Second):
					t.Fatal("read did not reach history wait")
				}
				probe, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
				if err := s.ControllerHealthContext(probe); err != nil {
					stop()
					t.Fatal("history read held owner locks", err)
				}
				stop()
				var want error
				switch scenario {
				case "expiry":
					s.fsm.mu.Lock()
					changed := head.Clone()
					changed.ExecutionResult.HistoryExpiredAt = head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30)
					s.fsm.image.Collections[head.ID] = changed
					s.fsm.mu.Unlock()
				case "history":
					replacement, err := openHistory("")
					if err != nil {
						t.Fatal(err)
					}
					s.fsm.mu.Lock()
					s.fsm.history = replacement
					s.fsm.mu.Unlock()
					defer func() { s.fsm.mu.Lock(); s.fsm.history = history; s.fsm.mu.Unlock(); _ = replacement.Close() }()
					want = ErrCollectionUnavailable
					if list {
						want = ErrHistoryUnavailable
					}
				case "cancel":
					cancel()
					want = context.Canceled
				}
				if scenario != "cancel" {
					history.mu.Unlock()
					locked = false
				}
				select {
				case got := <-done:
					if !errors.Is(got.err, want) || want == nil && (got.receipt.ExecutionObservation == nil || got.receipt.ExecutionObservation.State != "expired") || want != nil && got.receipt.ID != "" {
						t.Fatal("read ignored current fence", got, want)
					}
				case <-time.After(time.Second):
					t.Fatal("blocked observation did not finish")
				}
			})
		}
	}
}

func TestCollectionExecutionObservationNonAcceptedTerminalCounts(t *testing.T) {
	for _, decision := range []string{"unchanged", "conflict"} {
		t.Run(decision, func(t *testing.T) {
			s := openCatalogMemory(t)
			change := "create"
			if decision == "unchanged" {
				change = "unchanged"
			}
			head, begin := executionResultInput(t, s, change)
			head, child := executionResultDecide(t, s, head, begin, decision)
			if child != nil {
				t.Fatal("nonaccepted decision created a child")
			}
			head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
			got, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, head.ExecutionResult.Summary.FinalizedAt.Add(time.Second))
			if err != nil || got.ExecutionObservation == nil {
				t.Fatal(got, err)
			}
			c := got.ExecutionObservation.Counts
			want := "failed"
			if decision == "unchanged" {
				want = "completed"
			}
			if got.Phase != want || c.Processed != 1 || c.Accepted != 0 || c.ChildApplied != 0 || c.ChildPending != 0 ||
				decision == "unchanged" && c.Unchanged != 1 || decision == "conflict" && c.Conflicts != 1 {
				t.Fatal("successful unchanged or rejected decision changed catalog/application counters", got)
			}
		})
	}
}

func TestCollectionExecutionResultRecheckUsesMetadataOnly(t *testing.T) {
	s, head := executionPublicationFixture(t, false, 1)
	head = executionResultViewPublish(t, s, head)
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
	v, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	s.History().mu.Lock()
	defer s.History().mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := v.Recheck(ctx, at); err != nil {
		t.Fatal("metadata recheck waited on history", err)
	}
	if err := (*CollectionExecutionResultView)(nil).Recheck(ctx, at); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("nil view accepted", err)
	}
	s.fsm.mu.Lock()
	changed := head.Clone()
	changed.ExecutionResult.HistoryExpiredAt = head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30)
	s.fsm.image.Collections[head.ID] = changed
	s.fsm.mu.Unlock()
	if err := v.Recheck(ctx, at); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("metadata recheck ignored committed expiry", err)
	}
}
