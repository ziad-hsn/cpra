package persistence

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func executionPreparationViewFixture(t *testing.T) (*Store, CollectionState, OperatorAuthority) {
	t.Helper()
	s := openCatalogMemory(t)
	head, auth := activationFixture(t, s)
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, auth, at), at))
	return s, head, auth
}
func openExecutionPreparationView(t *testing.T, s *Store, head CollectionState, auth OperatorAuthority, at time.Time) *CollectionExecutionPreparationView {
	t.Helper()
	v, err := s.CollectionExecutionPreparationView(context.Background(), head.ID, auth, head.Activation.CapabilitiesDigest, at)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}
func TestCollectionExecutionPreparationViewOriginalInputBeyondUploadExpiry(t *testing.T) {
	s, head, auth := executionPreparationViewFixture(t)
	policy, _ := s.Authentication()
	replacement := lifecycleReplacement(policy)
	replacement.At = head.Activation.At.Add(time.Second)
	for i := range replacement.Principals {
		if replacement.Principals[i].ID == head.Actor {
			replacement.Principals[i].ExpiresAt = head.Activation.At.AddDate(1, 0, 0)
		}
	}
	if r := lifecycleCommand(t, s, CollectionActivationFormatVersion, replacement); r.Err != nil {
		t.Fatal(r.Err)
	}
	at := head.ExpiresAt.Add(time.Hour)
	auth, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	v := openExecutionPreparationView(t, s, head, auth, at)
	row, digest, err := v.Row(context.Background(), 1, at)
	if err != nil || digest == "" {
		t.Fatal(err)
	}
	input, err := v.Input(context.Background(), 1, at)
	if err != nil || input.Ordinal != row.InputOrdinal || input.Key != row.Key || input.Source != row.Source || input.SourceDocument != row.Document || input.SourceItem != row.Item {
		t.Fatal("original input/plan mismatch", err)
	}
	if _, found, err := v.Target(context.Background(), 1, at); err != nil || found {
		t.Fatal("create target", found, err)
	}
	if _, found, err := v.ExistingPrepared(context.Background(), at); err != nil || found {
		t.Fatal("nil progress synthesized preparation", err)
	}
	copy := v.Header()
	copy.Secret.Ciphertext[0] ^= 1
	if !reflect.DeepEqual(v.Header(), head) {
		t.Fatal("header alias")
	}
	if _, _, err := v.Row(context.Background(), 2, at); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("skipped next row", err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := v.Check(context.Background(), at); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("closed view usable", err)
	}
	after, _, _ := s.CollectionGet(head.ID)
	if !reflect.DeepEqual(after, head) || after.Execution != nil {
		t.Fatal("read changed original admission")
	}
}
func TestCollectionExecutionPreparationViewFencesCurrentTargetAndParent(t *testing.T) {
	t.Run("target", func(t *testing.T) {
		s, head, auth := executionPreparationViewFixture(t)
		at := head.Activation.At
		v := openExecutionPreparationView(t, s, head, auth, at)
		row, _, err := v.Row(context.Background(), 1, at)
		if err != nil {
			t.Fatal(err)
		}
		createCatalog(t, s, catalogRecord(t, s, row.Key.Kind, row.Key.ID, "outside-uid", "outside-revision", "{}"))
		if _, _, err = v.Target(context.Background(), 1, at); !errors.Is(err, ErrCatalogConflict) {
			t.Fatal("outside create was rebased", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		s, head, auth := executionPreparationViewFixture(t)
		at := head.Activation.At
		v := openExecutionPreparationView(t, s, head, auth, at)
		validationApplyAllowed(t, collectionCommand(t, s, activationCancel(head, auth, at.Add(time.Second)), at.Add(time.Second)))
		if err := v.Check(context.Background(), at.Add(time.Second)); !errors.Is(err, ErrCollectionConflict) {
			t.Fatal("cancel fence", err)
		}
	})
	t.Run("authority", func(t *testing.T) {
		s, head, auth := executionPreparationViewFixture(t)
		at := head.Activation.At
		v := openExecutionPreparationView(t, s, head, auth, at)
		s.fsm.mu.Lock()
		s.fsm.image.Authentication.Revision = "changed-revision"
		s.fsm.mu.Unlock()
		if err := v.Check(context.Background(), at); !errors.Is(err, ErrAuthenticationConflict) {
			t.Fatal("authority fence", err)
		}
	})
	t.Run("profile", func(t *testing.T) {
		s, head, auth := executionPreparationViewFixture(t)
		if _, err := s.CollectionExecutionPreparationView(context.Background(), head.ID, auth, strings.Repeat("d", 64), head.Activation.At); !errors.Is(err, ErrCollectionConflict) {
			t.Fatal("capability mismatch", err)
		}
	})
}
func TestCollectionExecutionPreparationViewCancellationWhileLedgerLocked(t *testing.T) {
	s, head, auth := executionPreparationViewFixture(t)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
	s.fsm.collections.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := s.CollectionExecutionPreparationView(ctx, head.ID, auth, head.Activation.CapabilitiesDigest, head.Activation.At)
		done <- err
	}()
	select {
	case <-ctx.waiting:
	case <-time.After(2 * time.Second):
		s.fsm.collections.mu.Unlock()
		t.Fatal("constructor did not wait cancellably")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			s.fsm.collections.mu.Unlock()
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		s.fsm.collections.mu.Unlock()
		t.Fatal("canceled constructor stuck")
	}
	s.fsm.collections.mu.Unlock()
	// The canceled capture released FSM ownership, so a real command can progress.
	at := head.Activation.At.Add(time.Second)
	validationApplyAllowed(t, collectionCommand(t, s, activationCancel(head, auth, at), at))
}
func TestCollectionExecutionPreparationViewChecksExactPreparedCommitment(t *testing.T) {
	s, head, auth := executionPreparationViewFixture(t)
	at := head.Activation.At
	v := openExecutionPreparationView(t, s, head, auth, at)
	row, digest, err := v.Row(context.Background(), 1, at)
	if err != nil {
		t.Fatal(err)
	}
	_ = v.Close()
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	record := catalogRecord(t, s, row.Key.Kind, row.Key.ID, "prepared-uid", "prepared-revision", "opaque test-only encrypted candidate")
	record.CreatedAt, record.UpdatedAt = at, at
	candidate := CollectionPreparedItem{Binding: binding, ID: "22222222-2222-4222-8222-222222222222", Ordinal: 1, InputOrdinal: row.InputOrdinal, RowDigest: digest, At: at, Record: record}
	progress, err := NewCollectionExecutionProgress(binding, head.ItemCount, at)
	if err != nil {
		t.Fatal(err)
	}
	progress, err = progress.withPrepared(candidate)
	if err != nil {
		t.Fatal(err)
	}
	// No execution preparation command exists yet. This explicit test-only splice
	// supplies its future atomic image+materialization boundary inside a real Store.
	s.fsm.mu.Lock()
	if err = s.fsm.collections.ApplyExecutionBatch([]collectionExecutionRecord{{Version: 1, Prepared: &candidate}}); err == nil {
		head.Execution = &progress
		s.fsm.image.Collections[head.ID] = head
	}
	s.fsm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	v = openExecutionPreparationView(t, s, head, auth, at)
	got, found, err := v.ExistingPrepared(context.Background(), at)
	if err != nil || !found || !reflect.DeepEqual(got, candidate) {
		t.Fatal("original candidate changed", found, err)
	}
	got.Record.Payload.Ciphertext[0] ^= 1
	again, _, err := v.ExistingPrepared(context.Background(), at)
	if err != nil || !reflect.DeepEqual(again, candidate) {
		t.Fatal("candidate alias", err)
	}
	_ = v.Close()
	s.fsm.mu.Lock()
	bad := head.Clone()
	bad.Execution.Prepared.Digest = strings.Repeat("f", 64)
	s.fsm.image.Collections[head.ID] = bad
	s.fsm.mu.Unlock()
	if _, err := s.CollectionExecutionPreparationView(context.Background(), head.ID, auth, head.Activation.CapabilitiesDigest, at); err == nil {
		t.Fatal("wrong authoritative prepared commitment accepted")
	}
}

func TestCollectionExecutionPreparationViewDiskRestartAndConcurrentGrowth(t *testing.T) {
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
	head, auth := activationFixture(t, s)
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, auth, at), at))
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	// Reopen the actual materialized generation without the production mmap
	// headroom. A modest native write must then exercise the growth boundary.
	s.fsm.mu.Lock()
	l := s.fsm.collections
	l.mu.Lock()
	path := l.db.Path()
	if err := l.db.Close(); err != nil {
		l.mu.Unlock()
		s.fsm.mu.Unlock()
		t.Fatal(err)
	}
	l.db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	l.mu.Unlock()
	s.fsm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	v := openExecutionPreparationView(t, s, head, auth, at)
	if v.ledger != nil || v.index != nil || v.catalog.tree != nil || l.db.Stats().OpenTxN != 0 {
		t.Fatal("holder retained a read transaction or full fleet index")
	}
	// Hold FSM ownership just as Apply does. The returned holder stays live while
	// the native bbolt writer grows; a pinned reader would prevent this completion
	// and also block the context-aware current-state check below.
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		s.fsm.mu.Lock()
		defer s.fsm.mu.Unlock()
		l.mu.Lock()
		defer l.mu.Unlock()
		close(started)
		err := l.db.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucket([]byte("preparation-test-growth"))
			if err != nil {
				return err
			}
			return b.Put([]byte("value"), bytes.Repeat([]byte{23}, 8<<20))
		})
		if err == nil {
			err = l.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("preparation-test-growth")) })
		}
		done <- err
	}()
	<-started
	bounded, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	checkErr := v.Check(bounded, at)
	cancel()
	if checkErr != nil {
		_ = v.Close()
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		_ = v.Close()
		t.Fatal("native writer remained blocked by holder")
	}
	if checkErr != nil {
		t.Fatal("current-state check blocked behind pinned reader", checkErr)
	}
	if _, err := v.Input(context.Background(), 1, at); err != nil {
		t.Fatal(err)
	}
	if _, found, err := v.ExistingPrepared(context.Background(), at); err != nil || found {
		t.Fatal("restart invented prepared state", err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	// Closing releases owned ciphertext; no database transaction remains to block the command.
	after := validationApplyAllowed(t, collectionCommand(t, s, activationCancel(head, auth, at.Add(time.Second)), at.Add(time.Second)))
	if after.Phase != "canceled" {
		t.Fatal("write did not progress after closing disk view")
	}
}

func TestCollectionExecutionPreparationViewRejectsCorruptOriginalResult(t *testing.T) {
	s, head, auth := executionPreparationViewFixture(t)
	s.fsm.collections.mu.Lock()
	original := s.fsm.collections.validationRows[head.ID][1]
	row, err := decodeCollectionValidationLedgerRow(original)
	if err == nil {
		row.Item.Source = "source.00000000000000000002"
		var raw []byte
		raw, err = collectionValidationLedgerEncoding(head.ID, row.Item)
		if err == nil {
			s.fsm.collections.validationRows[head.ID][1] = raw
		}
	}
	s.fsm.collections.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CollectionExecutionPreparationView(context.Background(), head.ID, auth, head.Activation.CapabilitiesDigest, head.Activation.At); !errors.Is(err, ErrCollectionUnavailable) {
		t.Fatal("corrupted sealed result authorized preparation", err)
	}
}

func TestCollectionExecutionPreparationViewCancellationAtOwnerLocks(t *testing.T) {
	for _, where := range []string{"construct-store", "construct-fsm", "check-store", "check-fsm"} {
		t.Run(where, func(t *testing.T) {
			s, head, auth := executionPreparationViewFixture(t)
			var view *CollectionExecutionPreparationView
			if strings.HasPrefix(where, "check") {
				view = openExecutionPreparationView(t, s, head, auth, head.Activation.At)
			}
			mu := &s.mu
			if strings.HasSuffix(where, "fsm") {
				mu = &s.fsm.mu
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
			mu.Lock()
			done := make(chan error, 1)
			go func() {
				if view != nil {
					done <- view.Check(ctx, head.Activation.At)
					return
				}
				captured, err := s.CollectionExecutionPreparationView(ctx, head.ID, auth, head.Activation.CapabilitiesDigest, head.Activation.At)
				if captured != nil {
					_ = captured.Close()
				}
				done <- err
			}()
			select {
			case <-ctx.waiting:
			case <-time.After(2 * time.Second):
				mu.Unlock()
				t.Fatal("read did not enter cancellable owner wait")
			}
			cancel()
			select {
			case err := <-done:
				mu.Unlock()
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				mu.Unlock()
				t.Fatal("canceled owner read remained blocked")
			}
		})
	}
}
