package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validationPublishStep(t *testing.T, s *Store, head CollectionState, at time.Time) Result {
	t.Helper()
	return collectionCommand(t, s, CollectionCommand{Action: "validation_publish", OperationID: head.ID, UploadID: head.UploadID,
		ValidationID: head.Validation.Header.ResultID, ValidationPublished: head.Validation.Published}, at)
}

func validationPublishFixture(t *testing.T, s *Store, n int, valid bool) (CollectionState, []CollectionValidationItem) {
	t.Helper()
	head := validationHistoryCrashInput(t, s, n)
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	head, begin, items := validationApplyIntent(t, s, head, authority, valid)
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	for start := 0; start < len(items); start += 256 {
		head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[start:min(start+256, len(items))]))
	}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	return head, items
}

func TestCollectionValidationPublicationRetainsItemsAfterCleanup(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			var s *Store
			if disk {
				config := testConfig(t)
				admin := openAuthenticationAdmin(t, config)
				if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
					t.Fatal(err)
				}
				if err := admin.Close(); err != nil {
					t.Fatal(err)
				}
				opened, err := Open(context.Background(), config)
				if err != nil {
					t.Fatal(err)
				}
				s = opened
				t.Cleanup(func() { _ = s.Close() })
			} else {
				s = openCatalogMemory(t)
			}
			head, items := validationPublishFixture(t, s, 257, false)
			at := head.ActivityAt.Add(time.Second)
			receipt, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || receipt.Phase != "validating" || receipt.Validation == nil {
				t.Fatal("unpublished verdict claimed completion", err)
			}
			if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("partial history called complete/expired", err)
			}
			original := head.Clone()
			r := validationPublishStep(t, s, head, at)
			head = validationApplyAllowed(t, r)
			if len(r.Events) != 256 || head.Validation.Published != 256 || head.Validation.HistorySealed {
				t.Fatal("publication exceeded page boundary")
			}
			if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at); !errors.Is(err, ErrHistoryUnavailable) {
				t.Fatal("partial summary visible", err)
			}
			retry := validationPublishStep(t, s, original, at)
			if retry.Err != nil || len(retry.Events) != 0 || !reflect.DeepEqual(*retry.Collection, head) {
				t.Fatal("lost publication reply duplicated rows", retry.Err)
			}
			cancel := CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}
			head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &cancel}, at))
			fence := collectionCleanupFor(head)
			if r := collectionCommand(t, s, CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &fence}, at); !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("cleanup discarded unpublished history", r.Err)
			}
			// The retained terminal expectation exists before the final history
			// page; cancellation cannot erase a finalized validation result.
			terminal, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || terminal.Validation == nil {
				t.Fatal("cancellation lost expected result", err)
			}
			terminal.Validation.Header.Issue = "changed-by-caller"
			fresh, err := s.CollectionReceipt(context.Background(), head.ID, at)
			if err != nil || fresh.Validation.Header.Issue != "invalidResource" {
				t.Fatal("receipt leaked mutable expectation", err)
			}
			r = validationPublishStep(t, s, head, at)
			head = validationApplyAllowed(t, r)
			if len(r.Events) != 2 || !head.Validation.HistorySealed || head.Validation.Published != 257 {
				t.Fatal("missing final row/seal")
			}
			for _, event := range r.Events {
				if !event.At.Equal(head.Validation.FinalizedAt) {
					t.Fatal("publication renewed retention cohort")
				}
			}
			var got []CollectionValidationItem
			for after := uint64(0); ; {
				page, err := s.CollectionValidationPage(context.Background(), head.ID, after, 100, at)
				if err != nil || len(page.Items) > 100 {
					t.Fatal("bounded results", err)
				}
				got = append(got, page.Items...)
				if page.NextAfter == 0 {
					break
				}
				after = page.NextAfter
			}
			if !reflect.DeepEqual(got, items) {
				t.Fatal("original results changed")
			}
			for step := 0; step < 4; step++ {
				fence := collectionCleanupFor(head)
				head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &fence}, at))
			}
			if _, ok, _ := s.CollectionGet(head.ID); ok {
				t.Fatal("input header not reclaimed")
			}
			page, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 500, head.Validation.FinalizedAt.AddDate(0, 0, 29))
			if err != nil || !reflect.DeepEqual(page.Items, items) {
				t.Fatal("result did not outlive input cleanup", err)
			}
			if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, head.Validation.FinalizedAt.AddDate(0, 0, 30)); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("result exceeded fixed retention deadline", err)
			}
			raw := captureSnapshotBytes(t, s.fsm)
			if !bytes.Contains(raw, []byte(collectionValidationSnapshotMagic)) {
				t.Fatal("snapshot format changed")
			}
		})
	}
}

func TestCollectionValidationPublicationTruthfulProjectionAndExpiration(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := validationPublishFixture(t, s, 2, true)
	at := head.ActivityAt.Add(time.Second)
	r, err := s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil || r.Phase != "validating" {
		t.Fatal("unsealed success became public", err)
	}
	head = validationApplyAllowed(t, validationPublishStep(t, s, head, at))
	r, err = s.CollectionReceipt(context.Background(), head.ID, at)
	if err != nil || r.Phase != "validated" || r.Validation == nil || !r.Validation.Header.Valid {
		t.Fatal("sealed success not observed", err)
	}
	// Twenty-four-hour upload expiry does not erase thirty-day result access,
	// even before maintenance has retired the inactive header.
	if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, head.ExpiresAt.Add(time.Hour)); err != nil {
		t.Fatal("upload TTL shortened result retention", err)
	}
	if err := s.History().Expire(head.Validation.FinalizedAt.AddDate(0, 0, 31)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CollectionReceipt(context.Background(), head.ID, at); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("backward clock fabricated retained success", err)
	}
	if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("backward clock revived expired results", err)
	}

	other := openCatalogMemory(t)
	late, _ := validationPublishFixture(t, other, 1, false)
	deadline := late.Validation.FinalizedAt.AddDate(0, 0, 30)
	result := validationPublishStep(t, other, late, deadline)
	late = validationApplyAllowed(t, result)
	if late.Validation.HistorySealed || !late.Validation.HistoryExpiredAt.Equal(deadline) || len(result.Events) != 0 {
		t.Fatal("expired publication claimed a visible seal")
	}
	if _, err := other.CollectionValidationPage(context.Background(), late.ID, 0, 100, deadline); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("explicit expiration not observed", err)
	}
}

func TestCollectionValidationPublicationMaintenanceAndMissingEvidence(t *testing.T) {
	s := openCatalogMemory(t)
	head, _ := validationPublishFixture(t, s, 1, false)
	at := head.ActivityAt.Add(time.Second)
	if err := s.maintainCollections(at); err != nil {
		t.Fatal(err)
	}
	head, _, _ = s.CollectionGet(head.ID)
	if !head.Validation.HistorySealed {
		t.Fatal("maintenance skipped finalized history")
	}
	s.History().mu.Lock()
	delete(s.History().memoryValidationResults, head.ID)
	s.History().mu.Unlock()
	if _, err := s.CollectionValidationPage(context.Background(), head.ID, 0, 100, at); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("missing known unexpired result called expired", err)
	}
	if _, err := s.CollectionReceipt(context.Background(), head.ID, at); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatal("phase exposed without retained evidence", err)
	}
}
