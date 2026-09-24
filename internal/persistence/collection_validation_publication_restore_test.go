package persistence

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCollectionValidationPublicationRejectsImpossibleSnapshotProgress(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := validationPublishFixture(t, s, 257, false)
	for _, published := range []uint64{1, 255} {
		bad := head.Clone()
		bad.Validation.Published = published
		if bad.validate() == nil || collectionValidationCleanupFor(bad.Validation).validate() == nil {
			t.Fatal("impossible partial publication accepted", published)
		}
		frozen, err := s.fsm.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		frozen.(*frozenSnapshot).image.Collections[head.ID] = bad
		sink := &collectionTestSink{}
		err = frozen.Persist(sink)
		frozen.Release()
		if err == nil {
			_, materialized, decodeErr := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
			if materialized != nil {
				_ = materialized.Close()
			}
			if decodeErr == nil {
				t.Fatal("snapshot restored an unrecoverable publication prefix", published)
			}
		}
	}
}

func TestCollectionValidationPublicationCanceledBeforeExplicitRestore(t *testing.T) {
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
	head, items := validationPublishFixture(t, s, 257, false)
	at := head.ActivityAt.Add(time.Second)
	cancel := CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}
	head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &cancel}, at))
	if head.Validation.HistorySealed || head.Validation.Published != 0 {
		t.Fatal("restore fixture was already published")
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MarkRestored(config.Storage.Directory, at); err != nil {
		t.Fatal(err)
	}
	admin = openAuthenticationAdmin(t, config)
	reset, err := admin.Authentication()
	if err != nil || !reset.ResetRequired {
		t.Fatal("restore did not reset authentication", err)
	}
	provision := lifecycleReplacement(reset)
	provision.Mode, provision.Principals = "provision", authenticationBootstrap().Principals
	if _, err := admin.CommitAuthentication(context.Background(), provision); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 6; step++ { // Two history pages, two result and two input cleanup pages.
		if err := s.maintainCollections(provision.At); err != nil {
			t.Fatal("restored terminal evidence made storage unavailable", err)
		}
	}
	s.fsm.mu.RLock()
	_, exists := s.fsm.image.Collections[head.ID]
	index := s.fsm.image.Index
	s.fsm.mu.RUnlock()
	if exists || s.Status().Error != "" {
		t.Fatal("restored terminal staging was not reclaimed")
	}
	expected := collectionValidationReceiptFor(head.Validation)
	page, err := s.History().collectionValidationPage(context.Background(), *expected, index, 0, 500, provision.At)
	if err != nil || !reflect.DeepEqual(page.Items, items) || !page.Receipt.FinalizedAt.Equal(head.Validation.FinalizedAt) {
		t.Fatal("restore lost or changed original retained result", err)
	}
	if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, provision.At); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("old operation epoch became publicly resumable", err)
	}
}
