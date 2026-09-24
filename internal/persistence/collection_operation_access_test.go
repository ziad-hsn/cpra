package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCollectionOperationAsOwnerBeforeHistoryAndElapsedReadOnly(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 2)
	ctx := context.Background()
	original := head.Clone()
	index := s.Status().CommittedIndex
	got, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, head.ExpiresAt)
	if err != nil || got.Phase != "expired" || !got.TerminalAt.IsZero() || got.ContentDigest != head.ContentDigest || got.ItemCount != 2 {
		t.Fatal("elapsed observation", got, err)
	}
	now, _, _ := s.CollectionGet(head.ID)
	if !reflect.DeepEqual(now, original) || s.Status().CommittedIndex != index {
		t.Fatal("read committed expiry")
	}
	if _, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, head.ActivityAt.Add(-time.Nanosecond)); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("backdated observation", err)
	}
	for _, terminal := range []bool{false, true} {
		if terminal {
			r := collectionCommand(t, s, collectionCancelFixture(head, head.ActivityAt.Add(time.Second)), head.ActivityAt.Add(time.Second))
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		}
		h := s.fsm.history
		h.mu.Lock()
		h.err = errors.New("injected unreadable history")
		h.mu.Unlock()
		denied, err := s.CollectionOperationAs(ctx, head.ID, "foreign", head.ActivityAt.Add(time.Second))
		h.mu.Lock()
		h.err = nil
		h.mu.Unlock()
		if !errors.Is(err, ErrOperationNotFound) || denied.ID != "" {
			t.Fatal("foreign owner reached unavailable history", terminal, err)
		}
	}
}

func TestCollectionOperationAsSealedSummaryDoesNotReadResultItems(t *testing.T) {
	s := openCatalogMemory(t)
	ctx := context.Background()
	head, _ := validationPublishFixture(t, s, 2, false)
	head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Second)))
	at := head.ActivityAt.Add(time.Second)
	index := s.Status().CommittedIndex
	h := s.fsm.history
	h.mu.Lock()
	result := h.memoryValidationResults[head.ID]
	first := result.items[1]
	delete(result.items, 1)
	h.mu.Unlock()
	got, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at)
	if err != nil || got.Phase != "rejected" || got.Validation == nil {
		t.Fatal("summary-only read required item rows", err)
	}
	if _, err := s.CollectionValidationPage(ctx, head.ID, 0, 1, at); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("fixture missing item not exercised", err)
	}
	h.mu.Lock()
	result.items[1] = first
	summary := result.summary
	result.summary = nil
	h.mu.Unlock()
	if denied, err := s.CollectionOperationAs(ctx, head.ID, "foreign", at); !errors.Is(err, ErrOperationNotFound) || denied.ID != "" {
		t.Fatal("foreign owner reached summary lookup", err)
	}
	if _, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("missing summary claimed verdict", err)
	}
	expired, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, head.ExpiresAt)
	if err != nil || expired.Phase != "expired" {
		t.Fatal("elapsed staging requires no verdict claim", err)
	}
	h.mu.Lock()
	result.summary = summary
	h.mu.Unlock()
	got, err = s.CollectionOperationAs(ctx, head.ID, head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	got.Validation.Header.ResultID = uuid.NewString()
	fresh, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at)
	if err != nil || fresh.Validation.Header.ResultID != head.Validation.Header.ResultID || s.Status().CommittedIndex != index {
		t.Fatal("mutable metadata alias/write", err)
	}
}

func TestCollectionOperationAsRetiredReceiptMissingHistoryAndRestart(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(snapshot), func(t *testing.T) {
			ctx := context.Background()
			config := testConfig(t)
			s, err := Open(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			head := collectionFill(t, s, 2)
			at := head.ActivityAt.Add(time.Second)
			r := collectionCommand(t, s, collectionCancelFixture(head, at), at)
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			expected, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.maintainCollections(at); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			index := s.Status().CommittedIndex
			got, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at.Add(29*24*time.Hour))
			if err != nil || !collectionReceiptsEqual(got, expected) {
				t.Fatal("retired receipt lost across restart", err)
			}
			if _, err := s.CollectionOperationAs(ctx, head.ID, "foreign", at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("retired receipt shared", err)
			}
			if _, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at.Add(31*24*time.Hour)); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("expired receipt revived", err)
			}
			if _, err := s.CollectionOperationAs(ctx, operationHandle(s.fsm.image.OperationEpoch, s.fsm.image.OperationHighWater+1), head.Actor, at); !errors.Is(err, ErrOperationNotFound) {
				t.Fatal("never-issued handle", err)
			}
			if _, err := s.CollectionOperationAs(ctx, operationHandle(uuid.NewString(), 1), head.Actor, at); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("old-epoch handle", err)
			}
			s.fsm.history.mu.Lock()
			s.fsm.history.err = errors.New("injected missing terminal history")
			s.fsm.history.mu.Unlock()
			_, err = s.CollectionOperationAs(ctx, head.ID, head.Actor, at)
			s.fsm.history.mu.Lock()
			s.fsm.history.err = nil
			s.fsm.history.mu.Unlock()
			if !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("unavailable history called expired", err)
			}
			if s.Status().CommittedIndex != index || len(s.fsm.image.Collections) != 0 || len(s.fsm.image.Catalog) != 0 {
				t.Fatal("point read allocated or activated work")
			}
		})
	}
}

func TestCollectionOperationAsCancellationHealthAndCorruptMetadata(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 1)
	for _, held := range []string{"store", "fsm"} {
		t.Run(held, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if held == "store" {
				s.mu.Lock()
			} else {
				s.fsm.mu.Lock()
			}
			done := make(chan error, 1)
			go func() { _, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, head.ActivityAt); done <- err }()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Error("lock wait ignored cancellation", err)
				}
			case <-time.After(time.Second):
				t.Error("canceled lock wait did not return")
			}
			if held == "store" {
				s.mu.Unlock()
			} else {
				s.fsm.mu.Unlock()
			}
		})
	}
	s.fsm.mu.Lock()
	corrupt := head.Clone()
	corrupt.ID = "invalid"
	s.fsm.image.Collections[head.ID] = corrupt
	s.fsm.mu.Unlock()
	if _, err := s.CollectionOperationAs(context.Background(), head.ID, head.Actor, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("corrupt header exposed", err)
	}
	s.fsm.mu.Lock()
	s.fsm.image.Collections[head.ID] = head
	s.fsm.mu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CollectionOperationAs(context.Background(), head.ID, head.Actor, head.ActivityAt); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("closed store returned receipt", err)
	}
}

// Done is first reached by the deliberately contended history lock. Metadata
// locks are uncontended in this fixture, making this an exact I/O-wait handshake.
type collectionOperationHistoryWait struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *collectionOperationHistoryWait) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestCollectionOperationAsHistoryWaitReleasesOwnerLocksAndRechecksEpoch(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		for _, changed := range []string{"none", "epoch", "index", "health"} {
			t.Run(fmt.Sprintf("terminal=%t/%s", terminal, changed), func(t *testing.T) {
				s := openCatalogMemory(t)
				head, _ := validationPublishFixture(t, s, 1, false)
				head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Second)))
				at := head.ActivityAt.Add(time.Second)
				if terminal {
					head = validationApplyAllowed(t, collectionCommand(t, s, collectionCancelFixture(head, at), at))
					if err := s.maintainCollections(at); err != nil {
						t.Fatal(err)
					}
				}
				base, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				ctx := &collectionOperationHistoryWait{Context: base, entered: make(chan struct{})}
				h := s.fsm.history
				h.mu.Lock()
				locked := true
				defer func() {
					if locked {
						h.mu.Unlock()
					}
				}()
				done := make(chan error, 1)
				go func() { _, err := s.CollectionOperationAs(ctx, head.ID, head.Actor, at); done <- err }()
				select {
				case <-ctx.entered:
				case <-base.Done():
					t.Fatal("lookup did not reach history lock")
				}
				// This is the same lock order used for protected owner changes. It must
				// complete while history remains blocked. An actual commit also appends its
				// history watermark; this test asserts owner-lock progress, not independent
				// completion of durable commits while history itself is unavailable.
				progressed := make(chan struct{})
				originalEpoch, originalIndex := s.fsm.image.OperationEpoch, s.fsm.image.Index
				go func() {
					s.mu.Lock()
					s.fsm.mu.Lock()
					s.fsm.image.Index++
					switch changed {
					case "epoch":
						s.fsm.image.OperationEpoch = uuid.NewString()
					case "index":
						s.fsm.image.Index = originalIndex - 1
					case "health":
						s.fsm.err = errors.New("injected unavailable owner")
					}
					s.fsm.mu.Unlock()
					s.mu.Unlock()
					close(progressed)
				}()
				select {
				case <-progressed:
				case <-time.After(time.Second):
					h.mu.Unlock()
					locked = false
					cancel()
					<-progressed
					<-done
					t.Fatal("history wait pinned Store/FSM mutation locks")
				}
				if changed == "none" {
					if _, err := s.ObserveOperatorAuthority(base, head.Actor, at); err != nil {
						t.Fatal("independent authority observation blocked", err)
					}
				}
				h.mu.Unlock()
				locked = false
				err := <-done
				want := error(nil)
				switch changed {
				case "epoch":
					want = ErrOperationExpired
				case "index", "health":
					want = ErrCollectionUnavailable
				}
				if !errors.Is(err, want) {
					t.Fatal("post-I/O owner fence", err, "want", want)
				}
				s.mu.Lock()
				s.fsm.mu.Lock()
				s.fsm.image.OperationEpoch = originalEpoch
				s.fsm.image.Index = originalIndex
				s.fsm.err = nil
				s.fsm.mu.Unlock()
				s.mu.Unlock()
			})
		}
	}
}
