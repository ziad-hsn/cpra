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

	"github.com/google/uuid"
)

func TestCollectionValidationAttemptViewClaimBoundaryAndDetachedReads(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			s := collectionReadStore(t, disk)
			head, authority := validationApplyInput(t, s, 3)
			upload, _, err := s.CollectionUploadView(context.Background(), head.ID, head.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			legacy, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			head = requestTestBegin(t, s, head, authority)
			uploadViewAssertions(t, upload, head.ActivityAt, ErrOperationExpired)
			collectionReadAssertions(t, legacy, head.ActivityAt, ErrOperationExpired)
			if _, _, err := s.CollectionUploadView(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("request reopened ordinary upload access", err)
			}
			if _, _, err := s.CollectionValidationView(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("request reopened legacy validation access", err)
			}
			if _, _, err := s.CollectionValidationAttemptView(context.Background(), head.ID, *CollectionValidationRequestFenceFor(head), head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("request without a claim exposed input", err)
			}
			fabricated := CollectionValidationRequestFence{RequestID: head.ValidationRequest.ID, ClaimID: uuid.NewString(), RunID: uuid.NewString()}
			if _, _, err := s.CollectionValidationAttemptView(context.Background(), head.ID, fabricated, head.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
				t.Fatal("uncommitted claim exposed input", err)
			}
			head = requestTestClaim(t, s, head)
			correct := *CollectionValidationRequestFenceFor(head)
			for _, mutate := range []func(*CollectionValidationRequestFence){
				func(p *CollectionValidationRequestFence) { p.RequestID = uuid.NewString() },
				func(p *CollectionValidationRequestFence) { p.ClaimID = uuid.NewString() },
				func(p *CollectionValidationRequestFence) { p.RunID = uuid.NewString() },
			} {
				bad := correct
				mutate(&bad)
				if _, _, err := s.CollectionValidationAttemptView(context.Background(), head.ID, bad, head.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
					t.Fatal("foreign attempt exposed input", err)
				}
			}
			before, err := json.Marshal(s.fsm.image)
			if err != nil {
				t.Fatal(err)
			}
			var transaction uint64
			if disk {
				transaction = uint64(ledgerTestTransactionID(t, s.fsm.collections))
			}
			view, protected, err := s.CollectionValidationAttemptView(context.Background(), head.ID, correct, head.ActivityAt)
			if err != nil || !reflect.DeepEqual(protected, head) {
				t.Fatal("claimed input view", err)
			}
			// Both the protected header and the caller's fence are detached from
			// the captured view and the live immutable request.
			correct.RunID = uuid.NewString()
			protected.Secret.Ciphertext[0] ^= 1
			protected.Owner.Actor = "mutated-owner"
			protected.ValidationRequest.Claim.ID = uuid.NewString()
			protected.ValidationRequest.Authority.Actor = "mutated-authority"
			protected.ValidationRequest.CapabilitiesDigest = "mutated-profile"
			if !reflect.DeepEqual(view.header, head) || *view.attempt != *CollectionValidationRequestFenceFor(head) {
				t.Fatal("caller-owned metadata aliases captured view")
			}
			page, err := view.Page(context.Background(), 0, 2, head.ActivityAt)
			if err != nil || len(page) != 2 || page[0].Ordinal != 1 || page[1].Ordinal != 2 {
				t.Fatal("bounded original page", err)
			}
			original := page[0].Clone()
			page[0].Payload.Ciphertext[0] ^= 1
			item, err := view.Item(context.Background(), 1, head.ActivityAt)
			if err != nil || !reflect.DeepEqual(item, original) {
				t.Fatal("caller-owned ciphertext aliases retained input", err)
			}
			found, exists, err := view.Find(context.Background(), original.Key, head.ActivityAt)
			if err != nil || !exists || !reflect.DeepEqual(found, original) {
				t.Fatal("indexed input read", err)
			}
			last, err := view.Page(context.Background(), 2, 256, head.ActivityAt)
			if err != nil || len(last) != 1 || last[0].Ordinal != 3 {
				t.Fatal("original page boundary", err)
			}
			if _, err := view.Page(context.Background(), 0, 257, head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("attempt relaxed page limit", err)
			}
			if _, found, err := view.Find(context.Background(), CatalogKey{Kind: "Credential", ID: "absent"}, head.ActivityAt); err != nil || found {
				t.Fatal("missing input identity was not ordinary absence", err)
			}
			if err := view.Check(context.Background(), head.ActivityAt); err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(s.fsm.image)
			if err != nil || !bytes.Equal(before, after) || disk && transaction != uint64(ledgerTestTransactionID(t, s.fsm.collections)) {
				t.Fatal("attempt reads modified durable metadata or ledger")
			}
			if len(s.fsm.image.Catalog) != 0 || len(s.fsm.image.Monitors) != 0 {
				t.Fatal("protected reads activated resources")
			}
		})
	}
}

func TestCollectionValidationAttemptViewEndsAtCommittedBoundary(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, boundary := range []string{"cancel", "revoke", "principal-expiry", "input-expiry", "plan-begin", "result-begin", "interrupt"} {
			t.Run(fmt.Sprintf("disk=%t/%s", disk, boundary), func(t *testing.T) {
				s := collectionReadStore(t, disk)
				head, authority := validationApplyInput(t, s, 2)
				head = requestTestClaim(t, s, requestTestBegin(t, s, head, authority))
				fence := *CollectionValidationRequestFenceFor(head)
				view, _, err := s.CollectionValidationAttemptView(context.Background(), head.ID, fence, head.ActivityAt)
				if err != nil {
					t.Fatal(err)
				}
				at := head.ActivityAt.Add(time.Millisecond)
				want := error(ErrCollectionConflict)
				switch boundary {
				case "cancel":
					cancel := CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: at}
					_ = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &cancel}, at))
				case "revoke":
					state, err := s.Authentication()
					if err != nil {
						t.Fatal(err)
					}
					replacement := lifecycleReplacement(state)
					replacement.At = at
					replacement.Principals[0].Revoked = true
					if result := lifecycleCommand(t, s, CollectionValidationFormatVersion, replacement); result.Err != nil {
						t.Fatal(result.Err)
					}
					want = ErrAuthenticationConflict // Captured revision is now stale too.
				case "principal-expiry":
					state, err := s.Authentication()
					if err != nil {
						t.Fatal(err)
					}
					at, want = state.Principals[0].ExpiresAt, ErrOperatorAuthorityDenied
				case "input-expiry":
					at, want = head.ExpiresAt, ErrOperationExpired
				case "plan-begin":
					begin, _ := planApplyArtifact(t, s, head)
					_ = requestTestArtifact(t, s, head, CollectionCommand{Action: "plan_begin", PlanBegin: &begin})
				case "result-begin":
					_, begin, _ := validationApplyIntent(t, s, head, authority, false)
					_ = requestTestArtifact(t, s, head, CollectionCommand{Action: "validation_begin", ValidationBegin: &begin})
				case "interrupt":
					command := requestTestInterrupt(head, "validationInterrupted")
					_ = validationApplyAllowed(t, collectionCommand(t, s, command, command.ValidationInterruption.At))
				}
				collectionReadAssertions(t, view, at, want)
				if _, _, err := s.CollectionValidationAttemptView(context.Background(), head.ID, fence, at); !errors.Is(err, want) {
					t.Fatal("closed attempt constructed a fresh input view", err, "want", want)
				}
			})
		}
	}
}

func TestCollectionValidationAttemptViewCancellationAndBackdatedObservation(t *testing.T) {
	s := collectionReadStore(t, false)
	head, authority := validationApplyInput(t, s, 1)
	head = requestTestClaim(t, s, requestTestBegin(t, s, head, authority))
	fence := *CollectionValidationRequestFenceFor(head)
	view, _, err := s.CollectionValidationAttemptView(context.Background(), head.ID, fence, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	collectionReadAssertions(t, view, head.ActivityAt.Add(-time.Nanosecond), ErrCollectionConflict)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.CollectionValidationAttemptView(canceled, head.ID, fence, head.ActivityAt); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled constructor", err)
	}
	checks := []func() error{
		func() error { return view.Check(canceled, head.ActivityAt) },
		func() error { _, err := view.Page(canceled, 0, 1, head.ActivityAt); return err },
		func() error { _, err := view.Item(canceled, 1, head.ActivityAt); return err },
		func() error {
			_, _, err := view.Find(canceled, CatalogKey{Kind: "Credential", ID: "item-00001"}, head.ActivityAt)
			return err
		},
	}
	for _, check := range checks {
		if err := check(); !errors.Is(err, context.Canceled) {
			t.Fatal("canceled read", err)
		}
	}
	planApplyStateUnchanged(t, s, head)
}
