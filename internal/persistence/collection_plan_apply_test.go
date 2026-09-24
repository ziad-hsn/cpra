package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Build an artifact from the real encrypted input ledger without decrypting it.
// These create-only rows exercise staging, not graph validity or activation.
func planApplyArtifact(t *testing.T, s *Store, head CollectionState) (CollectionPlanBegin, []CollectionPlanLedgerFragment) {
	t.Helper()
	header := planCodecHeader(head.ItemCount)
	header.OperationID, header.UploadID, header.Actor = head.ID, head.UploadID, head.Actor
	header.IdentityFormat, header.ContentDigest, header.InputProgressDigest = head.IdentityFormat, head.ContentDigest, head.ProgressDigest
	s.fsm.mu.RLock()
	header.ObservedIndex = s.fsm.image.Index
	s.fsm.mu.RUnlock()
	var raw bytes.Buffer
	descriptor, err := EncodeCollectionPlan(context.Background(), &raw, header, func(e *CollectionPlanEncoder) error {
		for after := uint64(0); after < head.ItemCount; {
			items, err := s.fsm.collections.Page(head.ID, after, 256)
			if err != nil || len(items) == 0 {
				return ErrCollectionUnavailable
			}
			for _, item := range items {
				row := planCodecRow(item.Ordinal, item.Ordinal, item.Key, "create")
				row.Source, row.Document, row.Item = item.Source, item.SourceDocument, item.SourceItem
				if err := e.BeginRow(row); err != nil {
					return err
				}
				if err := e.EndRow(); err != nil {
					return err
				}
				after = item.Ordinal
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var parts []CollectionPlanLedgerFragment
	got, err := DecodeCollectionPlan(context.Background(), &raw, func(part CollectionPlanFragment) error {
		parts = append(parts, CollectionPlanLedgerFragment{Ordinal: uint64(len(parts) + 1), Fragment: part})
		return nil
	})
	if err != nil || got != descriptor {
		t.Fatal("decode fixture", err)
	}
	return CollectionPlanBegin{Header: header, Descriptor: descriptor}, parts
}

func planApplyBegin(t *testing.T, s *Store, head CollectionState, begin CollectionPlanBegin) CollectionState {
	t.Helper()
	r := collectionCommand(t, s, CollectionCommand{Action: "plan_begin", OperationID: head.ID, UploadID: head.UploadID, PlanBegin: &begin}, head.ActivityAt.Add(time.Second))
	if r.Err != nil || !r.Allowed || r.Collection == nil {
		t.Fatal("begin plan", r.Err)
	}
	return r.Collection.Clone()
}

func planApplyAppend(t *testing.T, s *Store, head CollectionState, part CollectionPlanLedgerFragment) Result {
	t.Helper()
	return collectionCommand(t, s, CollectionCommand{Action: "plan_append", OperationID: head.ID, UploadID: head.UploadID,
		PlanID: head.Plan.Header.PlanID, PlanFragment: &part}, head.ActivityAt.Add(time.Second))
}

func planApplyAll(t *testing.T, s *Store, head CollectionState, parts []CollectionPlanLedgerFragment) CollectionState {
	t.Helper()
	for _, part := range parts {
		r := planApplyAppend(t, s, head, part)
		if r.Err != nil || r.Collection == nil {
			t.Fatal("append fragment", part.Ordinal, r.Err)
		}
		head = r.Collection.Clone()
	}
	return head
}

func planApplyStateUnchanged(t *testing.T, s *Store, want CollectionState) {
	t.Helper()
	got, exists, err := s.CollectionGet(want.ID)
	if err != nil || !exists || !reflect.DeepEqual(got, want) {
		t.Fatal("rejected proposal advanced committed state", err)
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if s.fsm.err != nil {
		t.Fatal("invalid proposal poisoned storage", s.fsm.err)
	}
}

func TestCollectionPlanApplyExactBeginAppendAndFinalizeStayInactive(t *testing.T) {
	s := openCatalogMemory(t)
	input := collectionFill(t, s, 3)
	begin, parts := planApplyArtifact(t, s, input)
	head := planApplyBegin(t, s, input, begin)
	if head.Phase != "validating" || head.Plan.UploadedFragments != 0 {
		t.Fatal("incorrect beginning")
	}
	retry := planApplyBegin(t, s, head, begin)
	if !reflect.DeepEqual(head, retry) {
		t.Fatal("begin retry renewed or replaced original intent")
	}
	changed := begin
	changed.Header.PlanID = uuid.NewString()
	r := collectionCommand(t, s, CollectionCommand{Action: "plan_begin", OperationID: head.ID, UploadID: head.UploadID, PlanBegin: &changed}, head.ActivityAt.Add(time.Second))
	if !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("second plan replaced original intent", r.Err)
	}
	head = planApplyAll(t, s, head, parts[:1])
	r = planApplyAppend(t, s, head, parts[0])
	if r.Err != nil || r.Collection.Plan.UploadedFragments != 1 || r.Collection.Plan.EncodedBytes != head.Plan.EncodedBytes ||
		!r.Collection.ExpiresAt.Equal(head.ExpiresAt.Add(time.Second)) {
		t.Fatal("exact fragment retry", r.Err)
	}
	head = planApplyAll(t, s, r.Collection.Clone(), parts[1:])
	proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	wrong := proof
	wrong.ProgressDigest = strings.Repeat("f", 64)
	c := CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &wrong}
	if r := collectionCommand(t, s, c, head.ActivityAt.Add(time.Second)); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("changed verification fence accepted", r.Err)
	}
	planApplyStateUnchanged(t, s, head)
	c.PlanFinalize = &proof
	r = collectionCommand(t, s, c, head.ActivityAt.Add(time.Second))
	if r.Err != nil || r.Collection.Phase != "validated" || r.Collection.Plan.FinalizedAt.IsZero() || len(r.Events) != 0 {
		t.Fatal("finalize", r.Err)
	}
	finalized := r.Collection.Clone()
	r = collectionCommand(t, s, c, finalized.ExpiresAt.Add(time.Hour))
	if r.Err != nil || !reflect.DeepEqual(*r.Collection, finalized) {
		t.Fatal("finalize retry rewrote verdict or deadline", r.Err)
	}
	if r := planApplyAppend(t, s, finalized, parts[0]); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("finalized plan remained writable", r.Err)
	}
	view, _ := s.CatalogSnapshot()
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if view.Len() != 0 || len(s.fsm.image.Monitors) != 0 || len(s.fsm.image.Operations) != 0 || len(s.fsm.planPrefixes) != 0 {
		t.Fatal("staging activated resources or retained a finished parser")
	}
}

func TestCollectionPlanApplyRejectsInputMismatchWithoutStoppingStorage(t *testing.T) {
	for _, field := range []string{"range", "key", "source", "document", "item", "row-order", "nested-header", "skipped-fragment"} {
		t.Run(field, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := collectionFill(t, s, 2)
			begin, parts := planApplyArtifact(t, s, head)
			head = planApplyBegin(t, s, head, begin)
			head = planApplyAll(t, s, head, parts[:1])
			bad := parts[1]
			row := *bad.Fragment.Row
			bad.Fragment.Row = &row
			switch field {
			case "range":
				row.InputOrdinal = head.ItemCount + 1
			case "key":
				row.Key.ID, row.Target.Key.ID = "other", "other"
			case "source":
				row.Source = "source.00000000000000000002"
			case "document":
				row.Document++
			case "item":
				row.Item++
			case "row-order":
				row.Ordinal++
			case "nested-header":
				bad.Fragment = parts[0].Fragment
			case "skipped-fragment":
				bad.Ordinal++
			}
			if err := (CollectionCommand{Action: "plan_append", OperationID: head.ID, UploadID: head.UploadID,
				PlanID: begin.Header.PlanID, PlanFragment: &bad}).validate(head.ActivityAt); err != nil {
				t.Fatal("fixture rejected before FSM under test", err)
			}
			r := planApplyAppend(t, s, head, bad)
			if r.Err == nil || r.Allowed {
				t.Fatal("accepted invalid fragment")
			}
			planApplyStateUnchanged(t, s, head)
			// A partially advanced parser must be rebuilt from committed bytes.
			head = planApplyAll(t, s, head, parts[1:])
			if _, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt); err != nil {
				t.Fatal("valid retry could not recover after rejection", err)
			}
		})
	}
}

func TestCollectionPlanApplyRejectsBeginIdentityAndIncompleteInput(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 2)
	begin, _ := planApplyArtifact(t, s, head)
	for _, field := range []string{"actor", "digest", "progress", "count", "future-index"} {
		bad := begin
		switch field {
		case "actor":
			bad.Header.Actor = "someone-else"
		case "digest":
			bad.Header.ContentDigest = strings.Repeat("c", 64)
		case "progress":
			bad.Header.InputProgressDigest = strings.Repeat("d", 64)
		case "count":
			bad.Header.ItemCount++
		case "future-index":
			bad.Header.ObservedIndex = ^uint64(0)
		}
		r := collectionCommand(t, s, CollectionCommand{Action: "plan_begin", OperationID: head.ID, UploadID: head.UploadID, PlanBegin: &bad}, head.ActivityAt.Add(time.Second))
		if !errors.Is(r.Err, ErrCollectionConflict) {
			t.Fatal(field, r.Err)
		}
		planApplyStateUnchanged(t, s, head)
	}
	incomplete := createCollectionFixture(t, s, 2)
	begin.Header.OperationID, begin.Header.UploadID = incomplete.ID, incomplete.UploadID
	begin.Header.Actor, begin.Header.ContentDigest, begin.Header.InputProgressDigest = incomplete.Actor, incomplete.ContentDigest, incomplete.ProgressDigest
	r := collectionCommand(t, s, CollectionCommand{Action: "plan_begin", OperationID: incomplete.ID, UploadID: incomplete.UploadID, PlanBegin: &begin}, incomplete.ActivityAt.Add(time.Second))
	if !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("plan began before complete input", r.Err)
	}
	planApplyStateUnchanged(t, s, incomplete)
}

func TestCollectionPlanApplyRejectsWrongFullDescriptorBeforeFooterCommit(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 1)
	begin, parts := planApplyArtifact(t, s, head)
	begin.Descriptor.Digest = strings.Repeat("e", 64)
	head = planApplyBegin(t, s, head, begin)
	head = planApplyAll(t, s, head, parts[:len(parts)-1])
	if r := planApplyAppend(t, s, head, parts[len(parts)-1]); !errors.Is(r.Err, ErrCollectionConflict) {
		t.Fatal("committed footer from a different intended artifact", r.Err)
	}
	planApplyStateUnchanged(t, s, head)
	if _, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("partial plan produced a finalize fence", err)
	}
}

func TestCollectionPlanApplyQuotaDoesNotAdvanceHeaderOrParser(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 1)
	begin, parts := planApplyArtifact(t, s, head)
	head = planApplyBegin(t, s, head, begin)
	s.fsm.collections.mu.Lock()
	s.fsm.collections.maxBytes = s.fsm.collections.bytes
	s.fsm.collections.mu.Unlock()
	if r := planApplyAppend(t, s, head, parts[0]); !errors.Is(r.Err, ErrCollectionQuota) {
		t.Fatal("quota exhaustion was not a nonfatal admission rejection", r.Err)
	}
	planApplyStateUnchanged(t, s, head)
	s.fsm.collections.mu.Lock()
	s.fsm.collections.maxBytes = maxCollectionLedgerBytes
	s.fsm.collections.mu.Unlock()
	head = planApplyAll(t, s, head, parts)
	if _, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt); err != nil {
		t.Fatal("valid plan could not resume after quota became available", err)
	}
}

func TestCollectionPlanApplyMissingCommittedInputStopsAdmission(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 1)
	begin, parts := planApplyArtifact(t, s, head)
	head = planApplyBegin(t, s, head, begin)
	head = planApplyAll(t, s, head, parts[:1])
	s.fsm.collections.mu.Lock()
	delete(s.fsm.collections.rows[head.ID], 1)
	s.fsm.collections.mu.Unlock()
	_, err := s.Submit(context.Background(), []Command{{Kind: "collection", At: head.ActivityAt.Add(time.Second),
		Collection: &CollectionCommand{Action: "plan_append", OperationID: head.ID, UploadID: head.UploadID,
			PlanID: begin.Header.PlanID, PlanFragment: &parts[1]}}})
	if !errors.Is(err, ErrCommitUnconfirmed) || s.Status().Ready {
		t.Fatal("missing committed input did not stop admission", err)
	}
}

func TestCollectionPlanApplyCleanupRetiresPlanBeforeInputs(t *testing.T) {
	for _, phase := range []string{"partial", "complete", "finalized"} {
		t.Run(phase, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := collectionFill(t, s, 140)
			begin, parts := planApplyArtifact(t, s, head)
			head = planApplyBegin(t, s, head, begin)
			if phase == "partial" {
				parts = parts[:270]
			}
			head = planApplyAll(t, s, head, parts)
			if phase == "finalized" {
				proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
				if err != nil {
					t.Fatal(err)
				}
				r := collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &proof}, head.ActivityAt.Add(time.Second))
				if r.Err != nil {
					t.Fatal(r.Err)
				}
				head = r.Collection.Clone()
			}
			original := head.Clone()
			oldFence := cleanupCommand(head)
			r := collectionCommand(t, s, oldFence, head.ExpiresAt)
			if r.Err != nil || r.Collection.Plan.RemovedFragments != 256 || r.Collection.RemovedRows != 0 || len(r.Events) != 1 {
				t.Fatal("first cleanup did not retain inputs", r.Err)
			}
			head = r.Collection.Clone()
			if r := collectionCommand(t, s, oldFence, head.ExpiresAt); !errors.Is(r.Err, ErrCollectionConflict) {
				t.Fatal("stale cleanup advanced a second page", r.Err)
			}
			// Restore the actual two-namespace snapshot after partial tail deletion.
			raw := captureSnapshotBytes(t, s.fsm)
			if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(raw))); err != nil {
				t.Fatal("partial plan cleanup snapshot", err)
			}
			r = collectionCommand(t, s, cleanupCommand(head), head.ExpiresAt)
			if r.Err != nil || r.Collection.Plan.RemovedFragments != uint64(len(parts)) || r.Collection.RemovedRows != 0 || len(r.Events) != 0 {
				t.Fatal("second cleanup changed input or duplicated event", r.Err)
			}
			head = r.Collection.Clone()
			if head.Plan.Descriptor != original.Plan.Descriptor || head.Plan.ProgressDigest != original.Plan.ProgressDigest {
				t.Fatal("cleanup rewrote intended plan identity")
			}
			r = collectionCommand(t, s, cleanupCommand(head), head.ExpiresAt)
			if r.Err != nil || r.Collection.RemovedRows != head.ItemCount || len(r.Events) != 0 {
				t.Fatal("input cleanup did not finish", r.Err)
			}
			if _, _, err := s.CollectionGet(head.ID); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("retired plan remained resumable", err)
			}
			if used, err := s.fsm.collections.Bytes(); err != nil || used != 0 {
				t.Fatal("combined quota not reclaimed", used, err)
			}
		})
	}
}

func TestCollectionPlanApplyCancelAndExpirationFenceFinalization(t *testing.T) {
	for _, terminal := range []string{"cancel", "expire"} {
		t.Run(terminal, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := collectionFill(t, s, 1)
			begin, parts := planApplyArtifact(t, s, head)
			head = planApplyAll(t, s, planApplyBegin(t, s, head, begin), parts)
			proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
			if err != nil {
				t.Fatal(err)
			}
			at := head.ExpiresAt
			if terminal == "cancel" {
				at = head.ActivityAt.Add(time.Second)
				c := collectionCancelFixture(head, at)
				if r := collectionCommand(t, s, c, at); r.Err != nil {
					t.Fatal(r.Err)
				}
			}
			result := collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &proof}, at)
			if result.Err == nil || result.Allowed {
				t.Fatal("stale proof finalized terminal/expired input")
			}
		})
	}
}

func TestCollectionPlanApplyCommandUnionAndFormatAreStrict(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 1)
	begin, parts := planApplyArtifact(t, s, head)
	good := CollectionCommand{Action: "plan_begin", OperationID: head.ID, UploadID: head.UploadID, PlanBegin: &begin}
	for n := range 4 {
		bad := good
		switch n {
		case 0:
			bad.PlanFragment = &parts[0]
		case 1:
			bad.PlanID = begin.Header.PlanID
		case 2:
			bad.Create = &head
		case 3:
			bad.Action = "upload"
		}
		if err := bad.validate(head.ActivityAt); !errors.Is(err, ErrCollectionInvalid) {
			t.Fatal("mixed command union accepted", n, err)
		}
	}
	command := Command{Kind: "collection", At: head.ActivityAt, Collection: &good}
	if err := validateCommand(command); err != nil {
		t.Fatal(fmt.Errorf("valid plan command: %w", err))
	}
	// A codec header/descriptor contains only identifiers and guard metadata.
	raw, err := json.Marshal(good)
	if err != nil || bytes.Contains(raw, []byte("never-write-this-collection-plaintext")) || bytes.Contains(raw, []byte("ciphertext")) {
		t.Fatal("plan contains resource/secret payload", err)
	}
}

func TestCollectionPlanApplyCleanupComparesFinalizationInstants(t *testing.T) {
	s := openCatalogMemory(t)
	head := collectionFill(t, s, 1)
	begin, parts := planApplyArtifact(t, s, head)
	head = planApplyAll(t, s, planApplyBegin(t, s, head, begin), parts)
	proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	at := head.ActivityAt.Add(time.Second).In(time.FixedZone("fixture", 5*3600+30*60))
	r := collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &proof}, at)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	head = r.Collection.Clone()
	c := cleanupCommand(head)
	c.Cleanup.Plan.FinalizedAt = c.Cleanup.Plan.FinalizedAt.UTC()
	if r := collectionCommand(t, s, c, head.ExpiresAt.UTC()); r.Err != nil || !r.Allowed {
		t.Fatal("equivalent encoded instant blocked terminal plan cleanup", r.Err)
	}
}
