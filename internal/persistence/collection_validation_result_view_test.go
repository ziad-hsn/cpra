package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func resultViewSealed(t *testing.T, s *Store, count int) (CollectionState, []CollectionValidationItem) {
	t.Helper()
	head, items := validationPublishFixture(t, s, count, false)
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Second)))
	}
	return head, items
}

func resultViewCleanup(t *testing.T, s *Store, head CollectionState, at time.Time) {
	t.Helper()
	for step := 0; step < 20; step++ {
		current, ok, err := s.CollectionGet(head.ID)
		if !ok && errors.Is(err, ErrOperationExpired) {
			return
		}
		if err != nil || !ok {
			t.Fatal("cleanup original header", err)
		}
		validationApplyAllowed(t, collectionCommand(t, s, cleanupCommand(current), at))
	}
	t.Fatal("bounded cleanup did not finish")
}

func TestCollectionValidationResultViewDayTwoCleanupAndPages(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s := openCatalogMemory(t)
			if disk {
				s = validationExpiryDiskStore(t)
			}
			head, items := resultViewSealed(t, s, 503)
			at := head.Validation.FinalizedAt.Add(48 * time.Hour)
			if _, err := s.CollectionReceipt(context.Background(), head.ID, at); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("fixture did not cross staging expiry", err)
			}
			view, status, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, at)
			if err != nil || view == nil || status.State != "ready" || status.IdentityFormat != head.IdentityFormat || status.ContentDigest != head.ContentDigest || status.ItemCount != head.ItemCount || status.ResultID != head.Validation.Header.ResultID {
				t.Fatal("retained result incorrectly follows staging deadline", status.State, err)
			}
			if !collectionValidationReceiptsEqual(view.Receipt(), *collectionValidationReceiptFor(head.Validation)) || view.Identity() != status {
				t.Fatal("view changed original summary or input commitment")
			}
			copy := view.Receipt()
			copy.Header.Issue = "changed"
			identity := view.Identity()
			identity.ContentDigest = "changed"
			if view.Receipt().Header.Issue == "changed" || view.Identity().ContentDigest == "changed" {
				t.Fatal("scalar getters alias original metadata")
			}
			page, err := view.Page(context.Background(), 0, 0, at)
			if err != nil || len(page.Items) != 100 || page.NextAfter != 100 || !reflect.DeepEqual(page.Items, items[:100]) {
				t.Fatal("default bounded page", err)
			}
			page, err = view.Page(context.Background(), 0, 500, at)
			if err != nil || len(page.Items) != 500 || page.NextAfter != 500 {
				t.Fatal("maximum bounded page", err)
			}
			for _, limit := range []int{-1, 501} {
				if _, err := view.Page(context.Background(), 0, limit, at); !errors.Is(err, ErrCollectionInvalid) {
					t.Fatal("invalid page limit accepted", limit, err)
				}
			}
			frozenIndex := view.index
			// Later committed results cannot change this view's watermark or rows.
			resultViewSealed(t, s, 1)
			if s.Status().CommittedIndex <= frozenIndex {
				t.Fatal("fixture did not advance committed history")
			}
			resultViewCleanup(t, s, head, at)
			page, err = view.Page(context.Background(), 500, 100, at)
			if err != nil || len(page.Items) != 3 || page.NextAfter != 0 || !reflect.DeepEqual(page.Items, items[500:]) || view.index != frozenIndex {
				t.Fatal("later commits/cleanup changed original view", err)
			}
			after, state, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, at)
			if err != nil || state.State != "ready" || state.TerminalPhase != "expired" || !collectionValidationReceiptsEqual(after.Receipt(), view.Receipt()) {
				t.Fatal("post-cleanup constructor lost retained original result", state.State, err)
			}
			if _, err := view.Page(context.Background(), 504, 100, at); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("out-of-range cursor accepted", err)
			}
			if disk {
				config := s.config
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(context.Background(), config)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				reopened, status, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, at)
				if err != nil || status.State != "ready" || !collectionValidationReceiptsEqual(reopened.Receipt(), after.Receipt()) {
					t.Fatal("restart lost retained result after cleanup", err)
				}
			}
		})
	}
}

func TestCollectionValidationResultViewPendingAndNoVerdict(t *testing.T) {
	s := openCatalogMemory(t)
	unrequested := validationHistoryCrashInput(t, s, 1)
	view, status, err := s.CollectionValidationResultView(context.Background(), unrequested.ID, unrequested.Actor, unrequested.ActivityAt)
	if err != nil || view != nil || status.State != "notRequested" {
		t.Fatal("unrequested upload presented as queued or rejected", status.State, err)
	}
	claimed := requestReceiptInput(t, s, 1)
	view, status, err = s.CollectionValidationResultView(context.Background(), claimed.ID, claimed.Actor, claimed.ActivityAt)
	if err != nil || view != nil || status.State != "pending" {
		t.Fatal("claimed request presented as completed", status.State, err)
	}
	command := requestReceiptInterrupt(claimed, claimed.ActivityAt.Add(time.Second))
	interrupted := validationApplyAllowed(t, collectionCommand(t, s, command, command.ValidationInterruption.At))
	view, status, err = s.CollectionValidationResultView(context.Background(), interrupted.ID, interrupted.Actor, interrupted.TerminalAt)
	if err != nil || view != nil || status.State != "noVerdict" || status.TerminalPhase != "interrupted" || status.InterruptionReason != "coordinatorRestarted" || status.ResultID != "" {
		t.Fatal("interruption manufactured a rejection verdict", status, err)
	}
	head, _ := validationPublishFixture(t, s, 257, false)
	if _, _, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, head.Validation.FinalizedAt.Add(-time.Nanosecond)); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("pending result accepted an observation before finalization", err)
	}
	view, status, err = s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, head.ActivityAt)
	if err != nil || view != nil || status.State != "pending" || status.ResultID != head.Validation.Header.ResultID {
		t.Fatal("unpublished finalization became available/invalid", status.State, err)
	}
	head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Second)))
	view, status, err = s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, head.ActivityAt)
	if err != nil || view != nil || status.State != "pending" || head.Validation.Published != 256 {
		t.Fatal("partial publication became completed/unavailable", status.State, err)
	}
	head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Second)))
	s.History().mu.Lock()
	delete(s.History().memoryValidationResults[head.ID].items, 1)
	s.History().mu.Unlock()
	if view, status, err := s.CollectionValidationResultView(context.Background(), head.ID, "different-owner", head.ActivityAt); !errors.Is(err, ErrOperationNotFound) || view != nil || status.OperationID != "" {
		t.Fatal("foreign owner reached corrupt result rows", err)
	}
	if _, _, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, head.ActivityAt); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("missing sealed row disguised as pending/expired", err)
	}
}

func TestCollectionValidationResultViewOwnerAfterCleanup(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := resultViewSealed(t, s, 1)
	at := head.Validation.FinalizedAt.Add(48 * time.Hour)
	resultViewCleanup(t, s, head, at)
	// The only owner evidence left is the retained operation receipt. Corrupt
	// result evidence must not be examined before that owner has been checked.
	s.History().mu.Lock()
	delete(s.History().memoryValidationResults[head.ID].items, 1)
	s.History().mu.Unlock()
	if view, status, err := s.CollectionValidationResultView(context.Background(), head.ID, "different-owner", at); !errors.Is(err, ErrOperationNotFound) || view != nil || status.OperationID != "" {
		t.Fatal("post-cleanup foreign owner reached result evidence", err)
	}
	if _, _, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, at); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("post-cleanup missing evidence became absent/expired", err)
	}
}

func TestCollectionValidationResultViewHistoryWaitFences(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRead), func(t *testing.T) {
			s := openCatalogMemory(t)
			head, _ := resultViewSealed(t, s, 1)
			view, _, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, head.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
			s.History().mu.Lock()
			locked := true
			defer func() {
				if locked {
					s.History().mu.Unlock()
				}
			}()
			completed := make(chan error, 1)
			go func() {
				_, err := view.Page(ctx, 0, 100, head.ActivityAt)
				completed <- err
			}()
			select {
			case <-ctx.waiting:
			case <-time.After(time.Second):
				t.Fatal("read did not wait on held history lock")
			}
			want := ErrOperationExpired
			if cancelRead {
				cancel()
				want = context.Canceled
			} else {
				// A history wait must hold neither owner lock. Changing the epoch
				// here exercises the second protected check after a successful read.
				s.mu.Lock()
				s.fsm.mu.Lock()
				s.fsm.image.OperationEpoch = uuid.NewString()
				s.fsm.mu.Unlock()
				s.mu.Unlock()
				s.History().mu.Unlock()
				locked = false
			}
			select {
			case err := <-completed:
				if !errors.Is(err, want) {
					t.Fatal("history wait ignored context or later epoch", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("result read remained blocked")
			}
		})
	}
}

func TestCollectionValidationResultViewExpiryRestoreAndHealth(t *testing.T) {
	for _, scenario := range []string{"retention", "persisted-expiry", "restore", "closed", "canceled-context", "policy-revision"} {
		t.Run(scenario, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, _ := resultViewSealed(t, s, 1)
			at := head.ActivityAt.Add(time.Second)
			view, _, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, at)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			want := ErrOperationExpired
			switch scenario {
			case "retention":
				if _, err := view.Page(ctx, 0, 100, head.Validation.FinalizedAt.AddDate(0, 0, 30)); !errors.Is(err, ErrOperationExpired) {
					t.Fatal("fixed retention deadline not applied", err)
				}
				if err := s.History().Expire(head.Validation.FinalizedAt.AddDate(0, 0, 31)); err != nil {
					t.Fatal(err)
				}
			case "persisted-expiry":
				// A separate unsealed result follows the real replicated late-expiry
				// path; a backward read must honor its marker before history access.
				late, _ := validationPublishFixture(t, s, 1, false)
				late = validationApplyAllowed(t, validationPublishStep(t, s, late, late.Validation.FinalizedAt.AddDate(0, 0, 30)))
				if _, _, err := s.CollectionValidationResultView(ctx, late.ID, late.Actor, late.ActivityAt); !errors.Is(err, ErrOperationExpired) {
					t.Fatal("backward clock ignored persisted marker", err)
				}
			case "restore":
				s.fsm.mu.Lock()
				s.fsm.image.OperationEpoch = uuid.NewString()
				s.fsm.mu.Unlock()
			case "closed":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				want = ErrCollectionUnavailable
			case "canceled-context":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, want = canceled, context.Canceled
			case "policy-revision":
				// This protected storage read is not execution authorization. The
				// HTTP adapter must authorize current reads; old execution revision
				// cannot make immutable facts unreadable after permitted rotation.
				s.fsm.mu.Lock()
				s.fsm.image.Authentication.Revision = uuid.NewString()
				s.fsm.mu.Unlock()
				if _, err := view.Page(ctx, 0, 100, at); err != nil {
					t.Fatal("result read required original execution revision", err)
				}
				return
			}
			if _, err := view.Page(ctx, 0, 100, at); !errors.Is(err, want) {
				t.Fatal("protected view ignored expiry/health/epoch", err, want)
			}
		})
	}
}

func TestCollectionValidationResultViewMissingSegment(t *testing.T) {
	s := validationExpiryDiskStore(t)
	head, _ := resultViewSealed(t, s, 2)
	view, _, err := s.CollectionValidationResultView(context.Background(), head.ID, head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.History().dir, head.Validation.FinalizedAt.UTC().Format("2006-01-02")+".db")
	if err := os.Rename(path, path+".missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := view.Page(context.Background(), 1, 1, head.ActivityAt); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("missing segment presented empty/successful result", err)
	}
}
