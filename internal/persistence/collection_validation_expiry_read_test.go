package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validationExpiryDiskStore(t *testing.T) *Store {
	t.Helper()
	config := testConfig(t)
	admin := openAuthenticationAdmin(t, config)
	if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func validationExpiryCancel(t *testing.T, s *Store, head CollectionState, at time.Time) CollectionState {
	t.Helper()
	c := CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}}
	return validationApplyAllowed(t, collectionCommand(t, s, c, at))
}

func TestCollectionValidationExpiryReadPersistedMarkerWinsBackwardTime(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, valid := range []bool{false, true} {
			t.Run(fmt.Sprintf("disk=%t/valid=%t", disk, valid), func(t *testing.T) {
				var s *Store
				if disk {
					s = validationExpiryDiskStore(t)
				} else {
					s = openCatalogMemory(t)
				}
				head, _ := validationPublishFixture(t, s, 1, valid)
				backwards := head.Validation.FinalizedAt.Add(time.Second)
				deadline := head.Validation.FinalizedAt.AddDate(0, 0, 30)
				result := validationPublishStep(t, s, head, deadline)
				head = validationApplyAllowed(t, result)
				if !head.Validation.HistoryExpiredAt.Equal(deadline) || head.Validation.HistorySealed || len(result.Events) != 0 {
					t.Fatal("fixture did not commit explicit unpublished expiry")
				}
				// Make the explicit marker independently observable: these protected
				// reads must not fall through to a history query or provisional phase.
				s.History().mu.Lock()
				s.History().err = ErrHistoryUnavailable
				s.History().mu.Unlock()
				if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, backwards); !errors.Is(err, ErrOperationExpired) {
					t.Fatal("backward clock lost persisted validation expiry", err)
				}
				if r, err := s.CollectionReceipt(context.Background(), head.ID, backwards); !errors.Is(err, ErrOperationExpired) || r.ID != "" {
					t.Fatal("expired validation returned provisional status", r.Phase, err)
				}
			})
		}
	}
}

func TestCollectionValidationExpiryReadPreservesCanceledReceipt(t *testing.T) {
	// Use real disk: memory append also advances its retention cutoff, which
	// could independently mask the missing persisted-header check.
	s := validationExpiryDiskStore(t)
	head, _ := validationPublishFixture(t, s, 1, false)
	at := head.Validation.FinalizedAt.Add(time.Second)
	head = validationExpiryCancel(t, s, head, at)
	original, err := s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil || original.Phase != "canceled" {
		t.Fatal(err)
	}
	deadline := head.Validation.FinalizedAt.AddDate(0, 0, 30)
	head = validationApplyAllowed(t, validationPublishStep(t, s, head, deadline))
	if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("nested validation revived", err)
	}
	result, err := s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil || !collectionReceiptsEqual(result, original) {
		t.Fatal("validation expiry erased independent cancellation", err)
	}
}

// withAuthenticationRead checks Err twice per lock and once after its callback.
// Pause at the next (sixth) check, after the first protected read has released
// both locks and before the second begins. This test-only synchronization does
// not insert a production hook or race mutable ECS/store state.
type validationExpiryBetweenReadsContext struct {
	context.Context
	calls            atomic.Uint32
	between, proceed chan struct{}
}

func (c *validationExpiryBetweenReadsContext) Err() error {
	if c.calls.Add(1) == 6 {
		close(c.between)
		<-c.proceed
	}
	return c.Context.Err()
}

func TestCollectionValidationExpiryReadRechecksSecondProtectedRead(t *testing.T) {
	s := validationExpiryDiskStore(t)
	head, _ := validationPublishFixture(t, s, 1, false)
	at := head.Validation.FinalizedAt.Add(time.Second)
	ctx := &validationExpiryBetweenReadsContext{Context: context.Background(), between: make(chan struct{}), proceed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := s.CollectionValidationPage(ctx, head.ID, 0, 100, at); done <- err }()
	select {
	case <-ctx.between:
	case <-time.After(5 * time.Second):
		close(ctx.proceed)
		t.Fatal("read did not reach second protected observation")
	}
	// The first observation did not contain an expiry marker. Commit it through
	// the real Raft path before permitting the second read to observe the header.
	head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.Validation.FinalizedAt.AddDate(0, 0, 30)))
	// A retired result needs no history read. Make that boundary observable even
	// if a future history implementation also advances its cutoff on append.
	s.History().mu.Lock()
	s.History().err = ErrHistoryUnavailable
	s.History().mu.Unlock()
	close(ctx.proceed)
	select {
	case err := <-done:
		if !errors.Is(err, ErrOperationExpired) {
			t.Fatal("second read queried history after committed expiry", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second protected read did not return")
	}
	if head.Validation.HistoryExpiredAt.IsZero() {
		t.Fatal("missing committed expiry")
	}
}

func TestCollectionValidationExpiryReadAfterCleanupDiskBoundary(t *testing.T) {
	s := validationExpiryDiskStore(t)
	head, _ := validationPublishFixture(t, s, 1, false)
	at := head.Validation.FinalizedAt.Add(time.Second)
	head = validationExpiryCancel(t, s, head, at)
	original, err := s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	deadline := head.Validation.FinalizedAt.AddDate(0, 0, 30)
	head = validationApplyAllowed(t, validationPublishStep(t, s, head, deadline))
	for step := 0; step < 4; step++ {
		if _, ok, err := s.CollectionGet(head.ID); err != nil && !(errors.Is(err, ErrOperationExpired) && !ok) {
			t.Fatal(err)
		} else if !ok {
			break
		}
		fence := collectionCleanupFor(head)
		head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &fence}, deadline))
	}
	if _, ok, _ := s.CollectionGet(head.ID); ok {
		t.Fatal("fixture retained authoritative header")
	}
	if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("cleanup lost committed expiry", err)
	}
	receipt, err := s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil || !collectionReceiptsEqual(receipt, original) {
		t.Fatal("cleanup erased retained cancellation", err)
	}
	// Neither reopen nor a backward supplied clock can undo the synced cutoff.
	config := s.config
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	s = reopened
	if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("reopened cutoff did not preserve expiry", err)
	}
	receipt, err = s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil || !collectionReceiptsEqual(receipt, original) {
		t.Fatal("validation cutoff erased newer cancellation", err)
	}
}

func TestCollectionValidationExpiryReadCutoffWriteFailure(t *testing.T) {
	s := validationExpiryDiskStore(t)
	head, _ := validationPublishFixture(t, s, 1, false)
	original := head.Clone()
	// A directory at the catalog destination causes a real OS replacement error,
	// including when tests run as root. All paths belong to this test's tempdir.
	path := filepath.Join(s.config.Storage.Directory, "history", "catalog.json")
	backup := path + ".test-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(path)
		_ = os.Rename(backup, path)
	})
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	at := head.Validation.FinalizedAt.AddDate(0, 0, 30)
	_, err := s.Submit(context.Background(), []Command{{Kind: "collection", At: at, Collection: &CollectionCommand{
		Action: "validation_publish", OperationID: head.ID, UploadID: head.UploadID,
		ValidationID: head.Validation.Header.ResultID, ValidationPublished: head.Validation.Published,
	}}})
	if err == nil {
		t.Fatal("catalog replacement failure was acknowledged")
	}
	s.fsm.mu.RLock()
	retained := s.fsm.image.Collections[head.ID].Clone()
	failed := s.fsm.err
	s.fsm.mu.RUnlock()
	if !reflect.DeepEqual(retained, original) || failed == nil {
		t.Fatal("failed cutoff write installed expiry or left storage available")
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatal("fixture did not preserve failed replacement boundary", err)
	}
	if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, head.ActivityAt.Add(time.Second)); !errors.Is(err, ErrAuthenticationUnavailable) {
		t.Fatal("failed persistence remained readable", err)
	}
}
