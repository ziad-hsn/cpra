package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func retainedListSealed(t *testing.T, s *Store) CollectionState {
	t.Helper()
	head, _ := executionResultInput(t, s, "create")
	head, _ = activationSnapshotCancel(t, s, head, head.Activation.At.Add(time.Second))
	return executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
}

func retainedListIDs(rows []OperationObservation) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Operation != nil {
			ids = append(ids, row.Operation.ID)
		} else {
			ids = append(ids, row.Collection.ID)
		}
	}
	return ids
}

func TestManagementRetainedExecutionListOrderSnapshotAndOwnership(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 2)
			head = executionResultViewPublish(t, s, head)
			reserved, _ := allocatedCommand(t, s, reservationCommand(t, s, "shared-monitor", time.Now().UTC()))
			terminal := managementCollectionTerminal(t, s, head.Actor)
			foreign := managementCollectionTerminal(t, s, "foreign")
			live := managementCollectionCreate(t, s, head.Actor)
			second := retainedListSealed(t, s)
			at := max(head.ExecutionResult.Summary.FinalizedAt.UnixNano(), second.ExecutionResult.Summary.FinalizedAt.UnixNano())
			now := time.Unix(0, at).UTC().Add(time.Hour)
			s.fsm.mu.Lock()
			r := s.fsm.image.OperationReservations[reserved.ID]
			r.ExpiresAt = now.Add(time.Hour)
			s.fsm.image.OperationReservations[reserved.ID] = r
			s.fsm.mu.Unlock()
			old, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, now, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			before := managementOperationAll(t, old, "", 1)
			executionReadRemoveHeader(t, s, head)
			executionReadRemoveHeader(t, s, second)
			if got := managementOperationAll(t, old, "", 2); !reflect.DeepEqual(got, before) {
				t.Fatal("source retirement changed frozen phases or original metadata")
			}
			fresh, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, now, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{reserved.ID, live.ID, head.ID, terminal.ID, second.ID}
			rows := managementOperationAll(t, fresh, "", 1)
			if got := retainedListIDs(rows); !reflect.DeepEqual(got, want) {
				t.Fatal("ordinary/live/retained ordering or duplicate cancellation", got, want, foreign.ID)
			}
			for _, row := range rows {
				if row.Collection == nil || row.Collection.ExecutionObservation == nil {
					continue
				}
				point, err := s.CollectionOperationAs(t.Context(), row.Collection.ID, head.Actor, now)
				if err != nil || !reflect.DeepEqual(*row.Collection, point) {
					t.Fatal("list/point mismatch", err)
				}
			}
			later := retainedListSealed(t, s)
			executionReadRemoveHeader(t, s, later)
			index := s.Status().CommittedIndex
			if got := retainedListIDs(managementOperationAll(t, fresh, "", 3)); !reflect.DeepEqual(got, want) {
				t.Fatal("new allocation crossed watermark", got)
			}
			plain, err := s.ManagementOperationSnapshot(t.Context(), "", now, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			if got := retainedListIDs(managementOperationAll(t, plain, "", 100)); !reflect.DeepEqual(got, []string{reserved.ID}) {
				t.Fatal("shared reader received collections", got)
			}
			if got := retainedListIDs(managementOperationAll(t, fresh, "shared-monitor", 100)); !reflect.DeepEqual(got, []string{reserved.ID}) {
				t.Fatal("monitor filter included collections", got)
			}
			if s.Status().CommittedIndex != index {
				t.Fatal("listing submitted work")
			}
		})
	}
}

func TestManagementRetainedExecutionListExpiredCancellation(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s := openCatalogMemory(t)
			if disk {
				s = validationExpiryDiskStore(t)
			}
			head, begin := executionResultInput(t, s, "create")
			head, child := executionResultDecide(t, s, head, begin, "accepted")
			head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
			stopped := head.TerminalAt
			update := OperationUpdate{ID: child.ID, Key: child.Key, UID: child.UID, Revision: child.NewVersion}
			results, err := s.Submit(t.Context(), []Command{{Kind: "operation", Operation: &update, At: stopped.AddDate(0, 0, 35)}})
			if err != nil || len(results) != 1 || results[0].Err != nil {
				t.Fatal(results, err)
			}
			s.fsm.mu.RLock()
			head = s.fsm.image.Collections[head.ID].Clone()
			s.fsm.mu.RUnlock()
			head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
			executionReadRemoveHeader(t, s, head)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			if _, err := s.History().collectionReceipt(t.Context(), head.ID, s.Status().CommittedIndex, at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("old cancellation still retained", err)
			}
			got := executionObservationListed(t, s, head, at)
			if got.Phase != "canceled" || !got.TerminalAt.Equal(stopped) || got.ExecutionObservation.State != "ready" ||
				got.ExecutionObservation.Counts.Accepted != 1 || got.ExecutionObservation.Counts.ChildFailed != 1 || got.ExecutionObservation.Counts.ChildApplied != 0 {
				t.Fatal("late result lost original cancellation/child facts", got)
			}
		})
	}
}

func TestManagementRetainedExecutionListAfterSourceRetirement(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := sourceRetirementFixture(t, disk, 3)
			original := head.Clone()
			at := head.ExecutionRetirement.UpdatedAt.Add(time.Second)
			frozen, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			before := managementOperationAll(t, frozen, "", 1)
			if len(before) != 1 || before[0].Collection == nil || before[0].Collection.ID != head.ID {
				t.Fatal("original source missing from captured list")
			}
			partialStages := map[string]bool{}
			for step := 0; sourceHeaderPresent(t, s, head.ID); step++ {
				if step >= 20 {
					t.Fatal("source retirement did not finish")
				}
				previous := head.Clone()
				head = validationApplyAllowed(t, executeStoreCommand(t, s, sourceRetirementCommand(head), at))
				if sourceHeaderPresent(t, s, head.ID) {
					page, err := frozen.ReadPage(t.Context(), "", "", 100)
					if err != nil || page.Next != "" || !reflect.DeepEqual(page.Rows, before) {
						t.Fatal("partial source retirement invalidated captured metadata", err)
					}
					if err := page.Recheck(t.Context(), at); err != nil {
						t.Fatal("partial cleanup prevented final list admission", err)
					}
					if head.Validation.RemovedRows > previous.Validation.RemovedRows {
						partialStages["validation"] = true
					}
					if head.Plan.RemovedFragments > previous.Plan.RemovedFragments {
						partialStages["plan"] = true
					}
				}
				at = at.Add(time.Second)
			}
			if !partialStages["validation"] || !partialStages["plan"] {
				t.Fatal("fixture omitted partial validation/plan stages")
			}
			// Advancing cleanup times also advances committed history retention.
			// The old cursor retains its existing expiry contract.
			if got, next, err := frozen.Page(t.Context(), "", "", 100); !errors.Is(err, ErrOperationCursorExpired) || got != nil || next != "" {
				t.Fatal("cleanup retention did not invalidate the old cursor", len(got), next, err)
			}
			if disk {
				configuration := s.config
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(t.Context(), configuration)
				if err != nil {
					t.Fatal("reopen retired source", err)
				}
				t.Cleanup(func() { _ = s.Close() })
			}
			index := s.Status().CommittedIndex
			fresh, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			page, err := fresh.ReadPage(t.Context(), "", "", 100)
			if err != nil || page.Next != "" || len(page.Rows) != 1 || page.Rows[0].Collection == nil {
				t.Fatal("retired original operation absent from list", len(page.Rows), page.Next, err)
			}
			row := page.Rows[0].Collection
			if row.ID != original.ID || row.Actor != original.Actor || row.Phase != original.Phase || !row.TerminalAt.Equal(original.TerminalAt) ||
				row.ExecutionObservation == nil || row.ExecutionObservation.State != "ready" || row.ExecutionObservation.Descriptor == nil ||
				!collectionExecutionSummariesEqual(row.ExecutionObservation.Summary, &original.ExecutionResult.Summary) ||
				*row.ExecutionObservation.Descriptor != collectionExecutionReceiptFor(*original.ExecutionResult).Descriptor {
				t.Fatal("retired list changed original owner/outcome/result")
			}
			if err := page.Recheck(t.Context(), at); err != nil {
				t.Fatal("retired list failed final admission", err)
			}
			foreign, err := s.ManagementOperationSnapshot(t.Context(), "foreign", at, 1<<20)
			if err != nil || len(managementOperationAll(t, foreign, "", 100)) != 0 {
				t.Fatal("retired list lost original ownership", err)
			}
			if s.Status().CommittedIndex != index {
				t.Fatal("retired list submitted work")
			}
		})
	}
}

func TestManagementRetainedExecutionListMetadataCorruption(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, fault := range []string{"anchor-index", "anchor-primary", "anchor-both", "seal", "progress", "items"} {
			t.Run(fmt.Sprint(disk)+"/"+fault, func(t *testing.T) {
				s, head := executionPublicationFixture(t, disk, 2)
				head = executionResultViewPublish(t, s, head)
				executionReadRemoveHeader(t, s, head)
				view, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, head.ExecutionResult.Summary.FinalizedAt.Add(time.Second), 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				h := s.History()
				h.mu.Lock()
				if disk {
					day := head.ExecutionResult.Summary.FinalizedAt.UTC().Format("2006-01-02")
					err = h.databases[day].Update(func(tx *bolt.Tx) error {
						a := tx.Bucket(collectionExecutionAnchorBucket)
						p := tx.Bucket(collectionExecutionPublicationBucket).Bucket([]byte(head.ID))
						switch fault {
						case "anchor-index":
							return a.Delete([]byte(head.ID))
						case "anchor-primary", "anchor-both":
							e, _ := decodeCollectionExecutionResultEvent(a.Get([]byte(head.ID)))
							if fault == "anchor-both" {
								if err := a.Delete([]byte(head.ID)); err != nil {
									return err
								}
							}
							return tx.Bucket([]byte("events")).Delete([]byte(e.MonitorID + "\x00" + e.ID))
						case "seal":
							return p.Delete(collectionExecutionPublicationSummaryKey)
						case "progress":
							return p.Put(collectionExecutionPublicationProgressKey, []byte(`{}`))
						case "items":
							return p.DeleteBucket(collectionExecutionPublicationItemsKey)
						}
						return nil
					})
				} else {
					switch fault {
					case "anchor-index":
						delete(h.memoryExecutionAnchors, head.ID)
					case "anchor-primary":
						h.memoryExecutionTree.Delete(operationMemoryItem{key: head.ID})
					case "anchor-both":
						delete(h.memoryExecutionAnchors, head.ID)
						delete(h.memoryExecutionResults, head.ID)
					case "seal":
						h.memoryExecutionResults[head.ID].summary = nil
					case "progress":
						h.memoryExecutionResults[head.ID].progress.Count++
					case "items":
						h.memoryExecutionResults[head.ID].items = nil
					}
				}
				h.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				rows, next, err := view.Page(t.Context(), "", "c:", 100)
				if fault == "items" {
					if err != nil || next != "" || len(rows) != 1 || rows[0].Collection.ExecutionObservation.State != "ready" {
						t.Fatal("list read item bodies", next, err)
					}
				} else if !errors.Is(err, ErrHistoryUnavailable) || rows != nil || next != "" {
					t.Fatal("corruption became empty/stale success", len(rows), next, err)
				}
			})
		}
	}
}

func TestManagementRetainedExecutionListBoundedForeignPrefix(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s := openCatalogMemory(t)
			if disk {
				s = validationExpiryDiskStore(t)
			}
			head := retainedListSealed(t, s)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			s.fsm.mu.Lock()
			base, index := s.fsm.image.OperationHighWater, s.fsm.image.Index+1
			events := make([]Event, 3001)
			for i := range events {
				summary := head.ExecutionResult.Summary.Clone()
				summary.Binding.OperationID = operationHandle(s.fsm.image.OperationEpoch, base+uint64(i)+1)
				summary.Actor = "foreign"
				events[i] = collectionExecutionResultEvent(summary)
				events[i].ID = fmt.Sprintf("%020d:%08d", index, i)
			}
			err := s.History().append(index, events, at)
			s.fsm.image.Index, s.fsm.image.OperationHighWater = index, base+uint64(len(events))
			s.fsm.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			view, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			after, pages := "c:"+head.ID, 0
			for ; pages < 10; pages++ {
				rows, next, err := view.Page(t.Context(), "", after, 100)
				if err != nil || len(rows) != 0 {
					t.Fatal("foreign anchor exposed rows or inspected absent foreign seal", len(rows), err)
				}
				if next == "" {
					break
				}
				if next == after || !strings.HasPrefix(next, "c:") {
					t.Fatal("budget did not advance", next)
				}
				after = next
			}
			if pages < 2 || pages == 10 {
				t.Fatal("foreign work was not bounded", pages)
			}
		})
	}
}

func TestManagementRetainedExecutionListReadAndAdmissionFences(t *testing.T) {
	for _, fence := range []string{"epoch", "highwater", "health", "cutoff", "cancel", "header"} {
		t.Run(fence, func(t *testing.T) {
			s, head := executionPublicationFixture(t, false, 1)
			head = executionResultViewPublish(t, s, head)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			view, err := s.ManagementOperationSnapshot(t.Context(), head.Actor, at, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if fence != "header" {
				executionReadRemoveHeader(t, s, head)
			}
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
			h := s.History()
			h.mu.Lock()
			locked := true
			defer func() {
				if locked {
					h.mu.Unlock()
				}
			}()
			type result struct {
				page ManagementOperationPage
				err  error
			}
			done := make(chan result, 1)
			go func() { p, err := view.ReadPage(ctx, "", "", 100); done <- result{p, err} }()
			select {
			case <-ctx.waiting:
			case <-time.After(time.Second):
				t.Fatal("list did not reach history wait")
			}
			probe, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
			if err := s.ControllerHealthContext(probe); err != nil {
				stop()
				t.Fatal("list held owner locks", err)
			}
			stop()
			var want error
			switch fence {
			case "epoch":
				s.fsm.mu.Lock()
				s.fsm.image.OperationEpoch = uuid.NewString()
				s.fsm.mu.Unlock()
				want = ErrOperationCursorExpired
			case "highwater":
				s.fsm.mu.Lock()
				s.fsm.image.OperationHighWater = 0
				s.fsm.mu.Unlock()
				want = ErrOperationCursorExpired
			case "health":
				s.MarkUnavailable(errors.New("list storage failure"))
				want = ErrHistoryUnavailable
			case "cutoff":
				h.catalog.Cutoff = head.ExecutionResult.Summary.FinalizedAt
				h.publishRetentionCutoff(h.catalog.Cutoff)
			case "cancel":
				cancel()
				want = context.Canceled
			case "header":
				executionReadRemoveHeader(t, s, head)
			}
			h.mu.Unlock()
			locked = false
			select {
			case got := <-done:
				if !errors.Is(got.err, want) {
					t.Fatal("read fence", got.err, want)
				}
				if want != nil {
					if len(got.page.Rows) != 0 {
						t.Fatal("failed read retained rows")
					}
					return
				}
				if len(got.page.Rows) != 1 {
					t.Fatal("lost retained row")
				}
				if fence == "cutoff" && got.page.Rows[0].Collection.ExecutionObservation.State != "expired" {
					t.Fatal("cutoff ignored")
				}
				h.mu.Lock()
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				err := got.page.Recheck(ctx, at)
				cancel()
				h.mu.Unlock()
				if err != nil {
					t.Fatal("final recheck acquired history", err)
				}
				if fence == "header" {
					h.publishRetentionCutoff(head.ExecutionResult.Summary.FinalizedAt)
					if err := got.page.Recheck(t.Context(), at); !errors.Is(err, ErrOperationCursorExpired) {
						t.Fatal("final admission ignored cutoff", err)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("list remained blocked")
			}
		})
	}
}
