package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validationApplyInput(t *testing.T, s *Store, n int) (CollectionState, OperatorAuthority) {
	t.Helper()
	if s.fsm.image.Authentication == nil {
		if _, err := s.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
			t.Fatal(err)
		}
	}
	c := collectionCreateFixture(t, s, uint64(n))
	c.Create.Actor = "oncall"
	owner, err := s.ObserveCollectionOwner(context.Background(), c.Create.Actor, c.Create.ActivityAt)
	if err != nil || owner == nil {
		t.Fatal("owner", err)
	}
	c.Create.Owner = owner
	r := collectionCommand(t, s, c, c.Create.ActivityAt)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	head := r.Collection.Clone()
	for j := 1; j <= n; j++ {
		item := collectionItemFixture(t, s, head, uint64(j), fmt.Sprintf("item-%05d", j))
		r = uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Millisecond))
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		head = r.Collection.Clone()
	}
	authority, err := s.ObserveOperatorAuthority(context.Background(), head.Actor, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	return head, authority
}

func validationApplyIntent(t *testing.T, s *Store, head CollectionState, authority OperatorAuthority, valid bool) (CollectionState, CollectionValidationBegin, []CollectionValidationItem) {
	t.Helper()
	if valid {
		begin, parts := planApplyArtifact(t, s, head)
		head = planApplyAll(t, s, planApplyBegin(t, s, head, begin), parts)
		proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
		if err != nil {
			t.Fatal(err)
		}
		r := collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &proof}, head.ActivityAt.Add(time.Millisecond))
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		head = r.Collection.Clone()
	}
	begin := CollectionValidationBegin{Header: CollectionValidationHeader{ResultID: uuid.NewString(), OperationID: head.ID, UploadID: head.UploadID,
		InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount, Authority: authority, CapabilitiesDigest: strings.Repeat("c", 64), Valid: valid},
		Descriptor: CollectionValidationDescriptor{Digest: CollectionValidationInitialDigest()}}
	if valid {
		begin.Header.PlanID, begin.Header.PlanDigest = head.Plan.Header.PlanID, head.Plan.Descriptor.Digest
	} else {
		begin.Header.Issue = "invalidResource"
	}
	var items []CollectionValidationItem
	for after := uint64(0); after < head.ItemCount; {
		page, err := s.fsm.collections.Page(head.ID, after, 256)
		if err != nil || len(page) == 0 {
			t.Fatal("input page", err)
		}
		for _, input := range page {
			item := CollectionValidationItem{Ordinal: input.Ordinal, Key: input.Key, Source: input.Source, Document: input.SourceDocument, Item: input.SourceItem, Change: "create"}
			if !valid && input.Ordinal == head.ItemCount {
				item.Change, item.Issue = "", "invalidResource"
			}
			items = append(items, item)
			digest, cost, err := CollectionValidationNextDigest(begin.Descriptor.Digest, item)
			if err != nil {
				t.Fatal(err)
			}
			begin.Descriptor.Count++
			begin.Descriptor.Bytes += cost
			begin.Descriptor.Digest = digest
			after = input.Ordinal
		}
	}
	return head, begin, items
}

func validationApplyCommand(t *testing.T, s *Store, head CollectionState, action string, begin *CollectionValidationBegin, items []CollectionValidationItem) Result {
	t.Helper()
	c := CollectionCommand{Action: action, OperationID: head.ID, UploadID: head.UploadID, ValidationBegin: begin, ValidationItems: items}
	if head.Validation != nil && begin == nil {
		c.ValidationID = head.Validation.Header.ResultID
	}
	return collectionCommand(t, s, c, head.ActivityAt.Add(time.Millisecond))
}

func validationApplyAllowed(t *testing.T, r Result) CollectionState {
	t.Helper()
	if r.Err != nil || !r.Allowed || r.Collection == nil {
		t.Fatal("validation command", r.Err)
	}
	return r.Collection.Clone()
}

func TestCollectionValidationApplyExactVerdictAndFrozenRestore(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			s := openCatalogMemory(t)
			head, authority := validationApplyInput(t, s, 3)
			head, begin, items := validationApplyIntent(t, s, head, authority, valid)
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
			retry := validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
			if !reflect.DeepEqual(head, retry) {
				t.Fatal("begin replay changed deadline or intent")
			}
			if r := validationApplyCommand(t, s, head, "validation_finalize", nil, nil); !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("partial finalized", r.Err)
			}
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[:1]))
			frozen, err := s.fsm.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			defer frozen.Release()
			old := &collectionTestSink{}
			if err := frozen.Persist(old); err != nil {
				t.Fatal(err)
			}
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items)) // Prefix retry plus new suffix.
			again := &collectionTestSink{}
			if err := frozen.Persist(again); err != nil || !bytes.Equal(old.Bytes(), again.Bytes()) {
				t.Fatal("frozen snapshot changed", err)
			}
			// Real FSM Restore removes derived indexes; the exact original input
			// and result bytes must be sufficient to recover the suffix.
			if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(old.Bytes()))); err != nil {
				t.Fatal(err)
			}
			restored, ok, err := s.CollectionGet(head.ID)
			if err != nil || !ok || restored.Validation.Uploaded != 1 {
				t.Fatal("prefix restore", err)
			}
			head = validationApplyAllowed(t, validationApplyCommand(t, s, restored, "validation_append", nil, items))
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
			retry = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
			if !reflect.DeepEqual(head, retry) || head.Validation.FinalizedAt.IsZero() || head.Validation.Header.Valid != valid {
				t.Fatal("changed final verdict")
			}
			if valid && head.Phase != "validated" || !valid && head.Phase != "rejected" {
				t.Fatal("wrong phase", head.Phase)
			}
			if r := validationApplyCommand(t, s, head, "validation_append", nil, items); !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("final result writable", r.Err)
			}
			fresh := captureSnapshotBytes(t, s.fsm)
			if bytes.Contains(fresh, []byte(authenticationToken)) || bytes.Contains(fresh, []byte("never-write-this-collection-plaintext")) {
				t.Fatal("plaintext in snapshot")
			}
			image, ledger, err := decodeSnapshot(bytes.NewReader(fresh), "")
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			if image.Collections[head.ID].Validation.Descriptor != begin.Descriptor || len(image.Catalog) != 0 || len(image.Monitors) != 0 || len(image.Operations) != 0 || len(s.fsm.validationPlanItems) != 0 {
				t.Fatal("result changed or staging activated work")
			}
		})
	}
}

func TestCollectionValidationApplyRejectsChangedInputAndPlanAtomically(t *testing.T) {
	for _, field := range []string{"range", "source", "document", "item", "key", "change", "version", "last-digest"} {
		t.Run(field, func(t *testing.T) {
			s := openCatalogMemory(t)
			head, authority := validationApplyInput(t, s, 2)
			head, begin, items := validationApplyIntent(t, s, head, authority, true)
			if field == "last-digest" {
				begin.Descriptor.Digest = strings.Repeat("f", 64)
			}
			head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
			bad := append([]CollectionValidationItem(nil), items...)
			switch field {
			case "range":
				bad = bad[1:]
				bad[0].Ordinal = 3
			case "source":
				bad[1].Source = "source.00000000000000000002"
			case "document":
				bad[1].Document++
			case "item":
				bad[1].Item++
			case "key":
				bad[1].Key.ID = "other"
			case "change":
				bad[1].Change = "unchanged"
			case "version":
				bad[1].Change = "update"
				bad[1].UID = "uid"
				bad[1].ResourceVersion = "revision"
			}
			r := validationApplyCommand(t, s, head, "validation_append", nil, bad)
			if !errors.Is(r.Err, ErrCollectionConflict) || s.fsm.err != nil {
				t.Fatal("proposal corruption or store failure", r.Err, s.fsm.err)
			}
			planApplyStateUnchanged(t, s, head)
			if n, b, err := s.fsm.collections.ValidationStats(head.ID); err != nil || n != 0 || b != 0 {
				t.Fatal("rejected batch committed prefix", n, b, err)
			}
			if field != "last-digest" {
				_ = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items))
			}
		})
	}
}

func TestCollectionValidationApplyAuthorityChangesStopPrefix(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 2)
	head, begin, items := validationApplyIntent(t, s, head, authority, false)
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[:1]))
	policy := s.fsm.image.Authentication.Clone()
	rotation := lifecycleReplacement(policy)
	rotation.At = head.ActivityAt.Add(time.Millisecond)
	rotation.Principals[0].TokenSHA256 = authenticationVerifier("rotated")
	if r := lifecycleCommand(t, s, CollectionValidationFormatVersion, rotation); r.Err != nil {
		t.Fatal(r.Err)
	}
	if r := validationApplyCommand(t, s, head, "validation_append", nil, items[1:]); !errors.Is(r.Err, ErrAuthenticationConflict) {
		t.Fatal("stale policy admitted continuation", r.Err)
	}
	planApplyStateUnchanged(t, s, head)
	if s.fsm.err != nil {
		t.Fatal("policy change stopped store")
	}
	changed := begin
	changed.Header.Authority.Revision = rotation.Revision
	if r := validationApplyCommand(t, s, head, "validation_begin", &changed, nil); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("refreshed authority silently rebound intent", r.Err)
	}
}

func TestCollectionValidationApplyCleanupBoundedAndSnapshots(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 257)
	head, begin, items := validationApplyIntent(t, s, head, authority, false)
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	for start := 0; start < len(items); start += 256 {
		head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, items[start:min(start+256, len(items))]))
	}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	// This test owns each cleanup page. Keep its cancellation observation ahead
	// of wall-clock maintenance while remaining inside the upload lifetime.
	cancel := CollectionCancellation{ID: uuid.NewString(), Actor: head.Actor, At: head.ActivityAt.Add(time.Hour)}
	if !cancel.At.Before(head.ExpiresAt) || !cancel.At.After(time.Now()) {
		t.Fatal("manual cleanup fixture must precede expiry and wall-clock maintenance")
	}
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "validation_publish", OperationID: head.ID, UploadID: head.UploadID,
			ValidationID: head.Validation.Header.ResultID, ValidationPublished: head.Validation.Published}, head.ActivityAt.Add(time.Millisecond)))
	}
	head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cancel", OperationID: head.ID, UploadID: head.UploadID, Cancel: &cancel}, cancel.At))
	for step := 0; step < 4; step++ {
		fence := collectionCleanupFor(head)
		fence.ActivityAt = fence.ActivityAt.In(time.FixedZone("offset", 19800))
		fence.Validation.FinalizedAt = fence.Validation.FinalizedAt.In(time.FixedZone("offset", 19800))
		head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "cleanup", OperationID: head.ID, UploadID: head.UploadID, Cleanup: &fence}, cancel.At))
		if step == 0 && (head.Validation.RemovedRows != 256 || head.RemovedRows != 0) || step == 1 && (head.Validation.RemovedRows != 257 || head.RemovedRows != 0) {
			t.Fatal("cleanup skipped namespace or page bound")
		}
		raw := captureSnapshotBytes(t, s.fsm)
		_, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
		if err != nil {
			t.Fatal("partial cleanup snapshot", err)
		}
		_ = ledger.Close()
	}
	if _, ok, err := s.CollectionGet(head.ID); !errors.Is(err, ErrOperationExpired) || ok {
		t.Fatal("completed cleanup retained header", err)
	}
}

func TestCollectionValidationApplyMissingCommittedInputFailsClosed(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 2)
	head, begin, items := validationApplyIntent(t, s, head, authority, false)
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	delete(s.fsm.collections.rows[head.ID], 2)
	_, err := s.Submit(context.Background(), []Command{{Kind: "collection", At: head.ActivityAt.Add(time.Millisecond), Collection: &CollectionCommand{
		Action: "validation_append", OperationID: head.ID, UploadID: head.UploadID, ValidationID: head.Validation.Header.ResultID, ValidationItems: items}}})
	if !errors.Is(err, ErrCollectionUnavailable) || !errors.Is(err, ErrCommitUnconfirmed) || s.fsm.err == nil {
		t.Fatal("missing committed input not fatal", err)
	}
}

func TestCollectionValidationApplyRejectedPhaseRequiresFinalRejection(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := validationApplyInput(t, s, 1)
	head, _, _ = validationApplyIntent(t, s, head, authority, true)
	for _, version := range []int{CollectionPlanFormatVersion, CollectionValidationFormatVersion} {
		bad := s.fsm.image
		bad.Collections = make(map[string]CollectionState)
		bad.Version = version
		// Auth2 belongs only to6; drop it here to isolate the phase invariant.
		bad.Authentication = nil
		modified := head.Clone()
		modified.Owner = nil
		modified.Phase = "rejected"
		bad.Collections[head.ID] = modified
		if modified.validate() == nil || validateCollectionHeaders(bad) == nil {
			t.Fatal("snapshot invented rejection without a result", version)
		}
	}
}
