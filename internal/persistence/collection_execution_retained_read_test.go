package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// Retirement is not enabled by these tests. Remove only the source header after
// committed finalization/publication to exercise independent retained authority.
func executionReadRemoveHeader(t *testing.T, s *Store, head CollectionState) {
	t.Helper()
	s.fsm.mu.Lock()
	delete(s.fsm.image.Collections, head.ID)
	s.fsm.mu.Unlock()
}

func TestCollectionExecutionRetainedReadOriginalPagesAndMetadata(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 3)
			head = executionResultViewPublish(t, s, head)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			before, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			first, err := before.Page(t.Context(), 0, 1, at)
			if err != nil || len(first.Items) != 1 || first.NextAfter != 1 {
				t.Fatal(first, err)
			}
			index := s.Status().CommittedIndex
			executionReadRemoveHeader(t, s, head)
			// Final HTTP admission must remain metadata-only after source removal.
			s.History().mu.Lock()
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
			err = before.Recheck(ctx, at)
			cancel()
			s.History().mu.Unlock()
			if err != nil {
				t.Fatal("final recheck acquired history or required source", err)
			}
			second, err := before.Page(t.Context(), first.NextAfter, 1, at)
			if err != nil || len(second.Items) != 1 || second.Items[0].InputOrdinal != 2 || second.NextAfter != 2 ||
				!collectionExecutionReceiptsEqual(first.Receipt, second.Receipt) {
				t.Fatal("cursor changed after source retirement", second, err)
			}
			fresh, status, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
			if err != nil || fresh == nil || status.State != "ready" || !reflect.DeepEqual(status, before.Identity()) ||
				!collectionExecutionReceiptsEqual(fresh.Receipt(), before.Receipt()) {
				t.Fatal("fresh retained lookup changed original identity", status, err)
			}
			point, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, at)
			if err != nil || point.ID != head.ID || point.UploadID != head.UploadID || point.Actor != head.Actor || point.Phase != "canceled" ||
				point.ItemCount != 3 || point.Uploaded != 3 || point.ContentDigest != head.ContentDigest || point.IdentityFormat != head.IdentityFormat ||
				point.CancellationID != head.Cancellation.ID || !point.TerminalAt.Equal(head.TerminalAt) || point.ExecutionObservation == nil ||
				point.ExecutionObservation.State != "ready" || point.ExecutionObservation.Counts.Unattempted != 3 ||
				!collectionExecutionSummariesEqual(point.ExecutionObservation.Summary, &head.ExecutionResult.Summary) {
				t.Fatal("retained operation projection", point, err)
			}
			if !point.CreatedAt.IsZero() || !point.ActivityAt.IsZero() || !point.ExpiresAt.IsZero() || point.Activation != nil || point.Validation != nil || point.validate() == nil {
				t.Fatal("read projection fabricated source or mutation authority")
			}
			if _, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, "foreign", at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("retained page owner", err)
			}
			if _, err := s.CollectionOperationAs(t.Context(), head.ID, "foreign", at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("retained metadata owner", err)
			}
			if s.Status().CommittedIndex != index {
				t.Fatal("retained read submitted work")
			}
		})
	}
}

func TestCollectionExecutionRetainedReadMissingEvidenceUnavailable(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, fault := range []string{"anchor", "anchor-primary", "seal", "seal-primary", "progress", "indexes", "watermark", "unsealed"} {
			t.Run(fmt.Sprint(disk)+"/"+fault, func(t *testing.T) {
				s, head := executionPublicationFixture(t, disk, 1)
				if fault != "unsealed" {
					head = executionResultViewPublish(t, s, head)
				}
				at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
				executionReadRemoveHeader(t, s, head)
				h := s.History()
				h.mu.Lock()
				if disk {
					day := head.ExecutionResult.Summary.FinalizedAt.UTC().Format("2006-01-02")
					err := h.databases[day].Update(func(tx *bolt.Tx) error {
						a := tx.Bucket(collectionExecutionAnchorBucket)
						var p *bolt.Bucket
						if root := tx.Bucket(collectionExecutionPublicationBucket); root != nil {
							p = root.Bucket([]byte(head.ID))
						}
						switch fault {
						case "anchor":
							return a.Delete([]byte(head.ID))
						case "anchor-primary":
							e, _ := decodeCollectionExecutionResultEvent(a.Get([]byte(head.ID)))
							return tx.Bucket([]byte("events")).Delete([]byte(e.MonitorID + "\x00" + e.ID))
						case "seal":
							return p.Delete(collectionExecutionPublicationSummaryKey)
						case "seal-primary":
							e, _ := decodeCollectionExecutionPublicationEvent(p.Get(collectionExecutionPublicationSummaryKey))
							return tx.Bucket([]byte("events")).Delete([]byte(e.MonitorID + "\x00" + e.ID))
						case "progress":
							return p.Put(collectionExecutionPublicationProgressKey, []byte(`{}`))
						case "indexes":
							if err := a.Delete([]byte(head.ID)); err != nil {
								return err
							}
							return tx.Bucket(collectionExecutionPublicationBucket).DeleteBucket([]byte(head.ID))
						}
						return nil
					})
					if err != nil {
						h.mu.Unlock()
						t.Fatal(err)
					}
				} else {
					switch fault {
					case "anchor":
						delete(h.memoryExecutionAnchors, head.ID)
					case "seal":
						h.memoryExecutionResults[head.ID].summary = nil
					case "progress":
						h.memoryExecutionResults[head.ID].progress.Count++
					case "anchor-primary":
						evidence := h.memoryExecutionEvidence[head.ID]
						evidence.anchor = Event{}
						h.memoryExecutionEvidence[head.ID] = evidence
					case "seal-primary":
						evidence := h.memoryExecutionEvidence[head.ID]
						evidence.seal = nil
						h.memoryExecutionEvidence[head.ID] = evidence
					case "indexes":
						delete(h.memoryExecutionAnchors, head.ID)
						delete(h.memoryExecutionResults, head.ID)
					}
				}
				if fault == "watermark" {
					h.catalog.Index--
				}
				h.mu.Unlock()
				if view, status, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at); !errors.Is(err, ErrHistoryUnavailable) || view != nil || status.State != "" {
					t.Fatal("missing evidence became available/pending/expired", status, err)
				}
				if point, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, at); !errors.Is(err, ErrHistoryUnavailable) || point.ID != "" {
					t.Fatal("missing execution evidence fell back to old cancellation", point, err)
				}
				if fault == "seal" || fault == "progress" || fault == "unsealed" {
					if _, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, "foreign", at); !errors.Is(err, ErrOperationNotFound) {
						t.Fatal("seal inspected before original owner", err)
					}
				}
			})
		}
	}
}

func TestCollectionExecutionRetainedReadOldCancellationLifetime(t *testing.T) {
	s := openCatalogMemory(t)
	head, command := executionResultInput(t, s, "create")
	head, child := executionResultDecide(t, s, head, command, "accepted")
	head, _ = activationSnapshotCancel(t, s, head, head.Execution.LastAt.Add(time.Second))
	originalStop := head.TerminalAt
	update := OperationUpdate{ID: child.ID, Key: child.Key, UID: child.UID, Revision: child.NewVersion, Applied: false}
	r, err := s.Submit(t.Context(), []Command{{Kind: "operation", Operation: &update, At: originalStop.AddDate(0, 0, 35)}})
	if err != nil || len(r) != 1 || r[0].Err != nil {
		t.Fatal(err, r)
	}
	s.fsm.mu.RLock()
	head = s.fsm.image.Collections[head.ID].Clone()
	s.fsm.mu.RUnlock()
	head = executionResultViewPublish(t, s, executionItemFinalize(t, s, head))
	executionReadRemoveHeader(t, s, head)
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Hour)
	if _, err := s.History().collectionReceipt(t.Context(), head.ID, s.Status().CommittedIndex, at); !errors.Is(err, ErrOperationNotFound) {
		t.Fatal("fixture still has old cancellation receipt", err)
	}
	point, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, at)
	if err != nil || point.Phase != "canceled" || !point.TerminalAt.Equal(originalStop) || point.ExecutionObservation.State != "ready" ||
		point.ExecutionObservation.Counts.Accepted != 1 || point.ExecutionObservation.Counts.ChildFailed != 1 {
		t.Fatal("old cancellation hid original execution", point, err)
	}
	view, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	page, err := view.Page(t.Context(), 0, 100, at)
	if err != nil || len(page.Items) != 1 || page.Items[0].Decision != "accepted" || page.Items[0].Child == nil || page.Items[0].Child.State != "failed" {
		t.Fatal("lost accepted decision or late child", page, err)
	}
}

func TestCollectionExecutionRetainedReadFencesAfterBlockedLookup(t *testing.T) {
	for _, operation := range []bool{false, true} {
		for _, fence := range []string{"cancel", "epoch", "highwater", "health", "reset", "cutoff"} {
			t.Run(fmt.Sprint(operation)+"/"+fence, func(t *testing.T) {
				s, head := executionPublicationFixture(t, false, 1)
				head = executionResultViewPublish(t, s, head)
				executionReadRemoveHeader(t, s, head)
				at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
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
				done := make(chan error, 1)
				go func() {
					if operation {
						r, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at)
						if fence == "cutoff" && err == nil && (r.ExecutionObservation == nil || r.ExecutionObservation.State != "expired") {
							err = errors.New("cutoff did not expire retained metadata")
						}
						done <- err
						return
					}
					_, _, err := s.CollectionExecutionResultView(ctx, head.ID, head.Actor, at)
					done <- err
				}()
				select {
				case <-ctx.waiting:
				case <-time.After(time.Second):
					t.Fatal("lookup did not reach bounded history wait")
				}
				probe, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
				err := s.ControllerHealthContext(probe)
				stop()
				if err != nil {
					t.Fatal("lookup retained owner locks", err)
				}
				want := ErrCollectionUnavailable
				switch fence {
				case "cancel":
					cancel()
					want = context.Canceled
				case "epoch":
					s.fsm.mu.Lock()
					s.fsm.image.OperationEpoch = uuid.NewString()
					s.fsm.mu.Unlock()
					want = ErrOperationExpired
				case "health":
					s.MarkUnavailable(errors.New("retained read storage failure"))
				case "highwater":
					s.fsm.mu.Lock()
					s.fsm.image.OperationHighWater = 0
					s.fsm.mu.Unlock()
					want = ErrOperationNotFound
				case "reset":
					s.fsm.mu.Lock()
					s.fsm.image.Authentication.ResetRequired = true
					s.fsm.mu.Unlock()
				case "cutoff":
					h.catalog.Cutoff = head.ExecutionResult.Summary.FinalizedAt
					h.publishRetentionCutoff(h.catalog.Cutoff)
					want = ErrOperationExpired
					if operation {
						want = nil
					}
				}
				h.mu.Unlock()
				locked = false
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatal("blocked lookup ignored current fence", err, want)
					}
				case <-time.After(time.Second):
					t.Fatal("lookup did not leave history wait")
				}
			})
		}
	}
}

func TestCollectionExecutionRetainedReadExpiryAndFinalRecheck(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionPublicationFixture(t, disk, 1)
			head = executionResultViewPublish(t, s, head)
			executionReadRemoveHeader(t, s, head)
			at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
			view, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			deadline := head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30)
			if _, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, head.Actor, deadline); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("known expired anchor became missing", err)
			}
			if _, _, err := s.CollectionExecutionResultView(t.Context(), head.ID, "foreign", deadline); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("expired metadata bypassed owner", err)
			}
			point, err := s.CollectionOperationAs(t.Context(), head.ID, head.Actor, deadline)
			if err != nil || point.ExecutionObservation.State != "expired" {
				t.Fatal(point, err)
			}
			if err := s.History().Expire(deadline.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if !disk {
				h := s.History()
				h.mu.RLock()
				_, retained := h.memoryExecutionEvidence[head.ID]
				h.mu.RUnlock()
				if retained {
					t.Fatal("expired primary execution evidence retained")
				}
			}
			if err := view.Recheck(t.Context(), at); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("final recheck missed monotonic cutoff", err)
			}
			if _, err := view.Page(t.Context(), 0, 100, at); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("backward read resurrected retained results", err)
			}
		})
	}
}

func TestCollectionExecutionRetainedReadHeaderRetiresDuringObservation(t *testing.T) {
	s, head := executionPublicationFixture(t, false, 1)
	head = executionResultViewPublish(t, s, head)
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
	ctx := &planVerificationWaitingContext{Context: t.Context(), waiting: make(chan struct{})}
	h := s.History()
	h.mu.Lock()
	locked := true
	defer func() {
		if locked {
			h.mu.Unlock()
		}
	}()
	type answer struct {
		receipt CollectionReceipt
		err     error
	}
	done := make(chan answer, 1)
	go func() {
		r, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at)
		done <- answer{r, err}
	}()
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		t.Fatal("point read did not reach history")
	}
	executionReadRemoveHeader(t, s, head)
	h.mu.Unlock()
	locked = false
	select {
	case got := <-done:
		if got.err != nil || got.receipt.ID != head.ID || got.receipt.ExecutionObservation == nil || got.receipt.ExecutionObservation.State != "ready" {
			t.Fatal("original sealed observation lost during source removal", got.receipt, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("point read did not finish")
	}
}

func TestCollectionExecutionRetainedReadNativeHistoryReopen(t *testing.T) {
	s, head := executionPublicationFixture(t, true, 2)
	head = executionResultViewPublish(t, s, head)
	at := head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
	index := s.Status().CommittedIndex
	dir := s.History().dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// This reopens only retained history. No source header/ledger is consulted.
	h, err := openHistoryWithCleanup(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	retained, err := h.collectionRetainedExecution(t.Context(), head.ID, head.Actor, index, at)
	if err != nil || retained.receipt == nil || !collectionExecutionReceiptsEqual(*retained.receipt, collectionExecutionReceiptFor(*head.ExecutionResult)) {
		t.Fatal("native retained authority did not recover", err)
	}
	page, err := h.collectionExecutionPage(t.Context(), *retained.receipt, index, 0, 1, at)
	if err != nil || len(page.Items) != 1 || page.NextAfter != 1 || page.Items[0].InputOrdinal != 1 {
		t.Fatal("native recovered result page", page, err)
	}
}
