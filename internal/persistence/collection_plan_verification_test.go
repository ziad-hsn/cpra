package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This fixture constructs structural staging state, not a graph-validity or
// execution certificate. Store/FSM admission itself is covered separately.
func planVerificationFixture(t *testing.T, disk bool, count int) (*Store, CollectionState, []CollectionPlanLedgerFragment) {
	t.Helper()
	s := collectionReadStore(t, disk)
	head := createCollectionFixture(t, s, 3)
	for n, key := range []CatalogKey{{Kind: "NotificationEndpoint", ID: "endpoint"}, {Kind: "Monitor", ID: "monitor"}, {Kind: "Credential", ID: "credential"}} {
		item := collectionItemFixture(t, s, head, uint64(n+1), key.ID)
		item.Key = key
		// Payload remains opaque: this fixture verifies structural binding of
		// original metadata, not provider configuration or graph semantics.
		result := uploadCollectionFixture(t, s, head, item, head.ActivityAt.Add(time.Second))
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		head = result.Collection.Clone()
	}
	header := planCodecHeader(3)
	header.OperationID, header.UploadID, header.Actor = head.ID, head.UploadID, head.Actor
	header.IdentityFormat, header.ContentDigest, header.InputProgressDigest = head.IdentityFormat, head.ContentDigest, head.ProgressDigest
	var artifact bytes.Buffer
	descriptor, err := EncodeCollectionPlan(context.Background(), &artifact, header, planCodecFixture)
	if err != nil {
		t.Fatal(err)
	}
	var parts []CollectionPlanLedgerFragment
	if _, err := DecodeCollectionPlan(context.Background(), &artifact, func(fragment CollectionPlanFragment) error {
		parts = append(parts, CollectionPlanLedgerFragment{Ordinal: uint64(len(parts) + 1), Fragment: fragment})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count < 0 {
		count = len(parts)
	}
	head.Phase = "validating"
	head.ActivityAt = head.ActivityAt.Add(time.Second)
	head.ExpiresAt = head.ActivityAt.Add(CollectionInactivityLifetime)
	head.Plan = &CollectionPlanState{Header: header, Descriptor: descriptor, BegunAt: head.ActivityAt,
		ArtifactBytes: uint64(len(collectionPlanMagic)), ProgressDigest: collectionPlanInitialDigest()}
	for _, part := range parts[:count] {
		if err := s.fsm.collections.AppendPlanFragments(head.ID, []CollectionPlanLedgerFragment{part}); err != nil {
			t.Fatal(err)
		}
		cost, _ := collectionPlanLedgerCost(head.ID, part)
		raw, _ := json.Marshal(part.Fragment)
		head.Plan.UploadedFragments++
		head.Plan.EncodedBytes += cost
		head.Plan.ArtifactBytes += uint64(len(raw)) + 4
		head.Plan.ProgressDigest, err = collectionPlanNextDigest(head.Plan.ProgressDigest, part.Fragment)
		if err != nil {
			t.Fatal(err)
		}
	}
	s.fsm.mu.Lock()
	s.fsm.image.Version = CollectionPlanFormatVersion
	s.fsm.image.Collections[head.ID] = head.Clone()
	s.fsm.mu.Unlock()
	if err := head.validate(); err != nil {
		t.Fatal("fixture header", err)
	}
	return s, head, parts
}

func TestCollectionPlanVerificationCompleteDetachedAndReadOnly(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%v", disk), func(t *testing.T) {
			s, head, _ := planVerificationFixture(t, disk, -1)
			before, _ := json.Marshal(s.fsm.image)
			tx := 0
			if disk {
				tx = ledgerTestTransactionID(t, s.fsm.collections)
			}
			proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
			if err != nil || proof != collectionPlanFence(head) {
				t.Fatal("complete artifact not verified", proof, err)
			}
			after, _ := json.Marshal(s.fsm.image)
			if !bytes.Equal(before, after) || disk && tx != ledgerTestTransactionID(t, s.fsm.collections) {
				t.Fatal("verification mutated or renewed durable state")
			}
			if disk && s.fsm.collections.db.Stats().OpenTxN != 0 {
				t.Fatal("verification retained a read transaction")
			}
			proof.Descriptor.Digest = strings.Repeat("f", 64)
			again, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
			if err != nil || again != collectionPlanFence(head) {
				t.Fatal("proof aliases stored header", err)
			}
			// A finalized retry verifies the same immutable descriptor without
			// rewriting timestamps or recording another outcome.
			s.fsm.mu.Lock()
			head.Phase, head.Plan.FinalizedAt = "validated", head.ActivityAt
			s.fsm.image.Collections[head.ID] = head.Clone()
			s.fsm.mu.Unlock()
			if got, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt); err != nil || got != again {
				t.Fatal("finalized identity changed", err)
			}
		})
	}
}

func TestCollectionPlanVerificationRejectsPartialAndChangedArtifacts(t *testing.T) {
	for _, count := range []int{0, 1, 5, 12} {
		s, head, _ := planVerificationFixture(t, false, count)
		proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
		if !errors.Is(err, ErrCollectionConflict) || proof != (CollectionPlanVerification{}) {
			t.Fatal("partial artifact produced proof", count, err)
		}
		view, err := s.fsm.collections.Freeze()
		if err != nil {
			t.Fatal(err)
		}
		err = validateCollectionPlanRows(s.fsm.image, view)
		_ = view.Close()
		if err != nil {
			t.Fatal("valid inactive prefix rejected", count, err)
		}
	}
	for _, change := range []string{"descriptor", "progress", "artifact-bytes", "encoded-bytes", "missing", "oversized", "cross-frame"} {
		t.Run(change, func(t *testing.T) {
			s, head, parts := planVerificationFixture(t, false, -1)
			s.fsm.mu.Lock()
			current := s.fsm.image.Collections[head.ID]
			switch change {
			case "descriptor":
				current.Plan.Descriptor.Digest = strings.Repeat("e", 64)
			case "progress":
				current.Plan.ProgressDigest = strings.Repeat("e", 64)
			case "artifact-bytes":
				current.Plan.ArtifactBytes--
			case "encoded-bytes":
				current.Plan.EncodedBytes++
			case "missing":
				delete(s.fsm.collections.planRows[head.ID], 4)
			case "oversized":
				s.fsm.collections.planRows[head.ID][1] = bytes.Repeat([]byte("x"), collectionPlanLedgerMaxFrame+1)
			case "cross-frame":
				// Individually valid typed guard, but its referenced predecessor
				// does not match the earlier committed key.
				part := parts[4]
				part.Fragment.Guards[0].Key.ID = "different-credential"
				raw, err := collectionPlanLedgerEncoding(head.ID, part)
				if err != nil {
					t.Fatal(err)
				}
				s.fsm.collections.planRows[head.ID][part.Ordinal] = raw
			}
			s.fsm.image.Collections[head.ID] = current
			s.fsm.mu.Unlock()
			proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
			if err == nil || proof != (CollectionPlanVerification{}) {
				t.Fatal("changed committed artifact produced proof")
			}
		})
	}
}

func TestCollectionPlanVerificationSnapshotNamespaceAndTerminalPrefix(t *testing.T) {
	for _, scenario := range []string{"full", "orphan", "missing-all", "extra-tail", "terminal-tail", "active-tail", "terminal-empty"} {
		t.Run(scenario, func(t *testing.T) {
			s, head, parts := planVerificationFixture(t, false, -1)
			wantError := false
			switch scenario {
			case "orphan":
				delete(s.fsm.image.Collections, head.ID)
				wantError = true
			case "missing-all":
				s.fsm.collections.planRows, s.fsm.collections.planOperationBytes = nil, nil
				s.fsm.collections.bytes -= s.fsm.collections.planBytes
				s.fsm.collections.planBytes = 0
				wantError = true
			case "extra-tail":
				head.Plan.UploadedFragments--
				wantError = true
			case "terminal-tail", "active-tail", "terminal-empty":
				keep := 5
				if scenario == "terminal-empty" {
					keep = 0
				}
				for _, part := range parts[keep:] {
					cost, _ := collectionPlanLedgerCost(head.ID, part)
					delete(s.fsm.collections.planRows[head.ID], part.Ordinal)
					s.fsm.collections.planOperationBytes[head.ID] -= cost
					s.fsm.collections.planBytes -= cost
					s.fsm.collections.bytes -= cost
					head.Plan.RemovedFragments++
					head.Plan.RemovedBytes += cost
				}
				if keep == 0 {
					delete(s.fsm.collections.planRows, head.ID)
					delete(s.fsm.collections.planOperationBytes, head.ID)
				}
				if scenario == "active-tail" {
					wantError = true
				} else {
					head.Phase = "expired"
				}
			}
			if scenario != "orphan" {
				s.fsm.image.Collections[head.ID] = head.Clone()
			}
			view, err := s.fsm.collections.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			err = validateCollectionPlanRows(s.fsm.image, view)
			_ = view.Close()
			if (err != nil) != wantError {
				t.Fatal("snapshot correspondence", err)
			}
		})
	}
}

type planVerificationCancelContext struct {
	context.Context
	checks atomic.Int32
	stopAt int32
}

func (c *planVerificationCancelContext) Err() error {
	if c.checks.Add(1) >= c.stopAt {
		return context.Canceled
	}
	return nil
}

func TestCollectionPlanVerificationFencesAndCancellation(t *testing.T) {
	s, head, _ := planVerificationFixture(t, true, -1)
	ctx := &planVerificationCancelContext{Context: context.Background(), stopAt: 40}
	if proof, err := s.VerifyCollectionPlan(ctx, head.ID, head.ActivityAt); !errors.Is(err, context.Canceled) || proof != (CollectionPlanVerification{}) {
		t.Fatal("in-progress cancellation not preserved", err)
	}
	if s.fsm.collections.db.Stats().OpenTxN != 0 {
		t.Fatal("cancellation retained a transaction")
	}
	for _, scenario := range []string{"expired", "canceled", "different-plan", "different-progress", "different-epoch", "storage-failed"} {
		t.Run(scenario, func(t *testing.T) {
			s, head, _ := planVerificationFixture(t, false, -1)
			observed := head.ActivityAt
			s.fsm.mu.Lock()
			current := s.fsm.image.Collections[head.ID]
			switch scenario {
			case "expired":
				observed = head.ExpiresAt
			case "canceled":
				current.Phase = "canceled"
			case "different-plan":
				current.Plan.Header.PlanID = "644f84f1-89e5-4bd2-9ce6-fc1c9665da49"
			case "different-progress":
				current.Plan.ProgressDigest = strings.Repeat("e", 64)
			case "different-epoch":
				s.fsm.image.OperationEpoch = "644f84f1-89e5-4bd2-9ce6-fc1c9665da49"
			case "storage-failed":
				s.fsm.err = ErrCollectionUnavailable
			}
			s.fsm.image.Collections[head.ID] = current
			s.fsm.mu.Unlock()
			if err := s.readCollectionPlan(context.Background(), head.ID, observed, &head, nil); err == nil {
				t.Fatal("captured verification fence accepted after change")
			}
		})
	}
}

func TestCollectionPlanVerificationPrefixChainUsesExactFraming(t *testing.T) {
	initial := sha256.Sum256([]byte(collectionPlanProgressDomain + collectionPlanMagic))
	if collectionPlanInitialDigest() != hex.EncodeToString(initial[:]) {
		t.Fatal("initial chain domain mismatch")
	}
	header := planCodecHeader(1)
	fragment := CollectionPlanFragment{Kind: "header", Header: &header}
	raw, _ := json.Marshal(fragment)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
	input := append([]byte(collectionPlanProgressDomain), initial[:]...)
	input = append(input, size[:]...)
	input = append(input, raw...)
	want := sha256.Sum256(input)
	got, err := collectionPlanNextDigest(collectionPlanInitialDigest(), fragment)
	if err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatal("chain did not commit exact length-framed fragment", err)
	}
	if got == collectionPlanInitialDigest() || reflect.DeepEqual(want, initial) {
		t.Fatal("fragment did not advance chain")
	}
	for _, previous := range []string{"", "secret", strings.Repeat("z", 64)} {
		if digest, err := collectionPlanNextDigest(previous, fragment); err == nil || digest != "" || strings.Contains(err.Error(), previous) && previous != "" {
			t.Fatal("invalid previous digest accepted or reflected")
		}
	}
}

func TestCollectionPlanVerificationRejectsMalformedPrefixAtOffendingFragment(t *testing.T) {
	_, head, parts := planVerificationFixture(t, false, 0)
	for _, scenario := range []string{"wrong-predecessor", "wrong-order", "wrong-header", "skipped-ordinal"} {
		t.Run(scenario, func(t *testing.T) {
			prefix := newCollectionPlanPrefix(head.Plan.Header)
			defer prefix.close()
			var offending CollectionPlanLedgerFragment
			if scenario == "wrong-header" {
				offending = parts[0]
				copy := *offending.Fragment.Header
				copy.PlanID = "644f84f1-89e5-4bd2-9ce6-fc1c9665da49"
				offending.Fragment.Header = &copy
			} else {
				for _, part := range parts[:4] {
					if err := prefix.add(context.Background(), head.ID, part); err != nil {
						t.Fatal("valid earlier prefix", err)
					}
				}
				offending = parts[4]
				switch scenario {
				case "wrong-predecessor":
					offending.Fragment.Guards = append([]CollectionPlanGuard(nil), offending.Fragment.Guards...)
					offending.Fragment.Guards[0].Key.ID = "different-credential"
				case "wrong-order":
					offending.Fragment = parts[5].Fragment
				case "skipped-ordinal":
					offending.Ordinal++
				}
			}
			if err := prefix.add(context.Background(), head.ID, offending); !errors.Is(err, ErrCollectionPlanInvalid) {
				t.Fatal("invalid prefix accepted before any footer/digest check", err)
			}
		})
	}
}

func TestCollectionPlanVerificationBindsEveryRowToOriginalInput(t *testing.T) {
	for _, scenario := range []string{"key", "source", "document", "item", "input-ordinal"} {
		t.Run(scenario, func(t *testing.T) {
			s, head, originalParts := planVerificationFixture(t, false, 0)
			var artifact bytes.Buffer
			descriptor, err := EncodeCollectionPlan(context.Background(), &artifact, head.Plan.Header, func(e *CollectionPlanEncoder) error {
				for _, part := range originalParts {
					var err error
					switch f := part.Fragment; f.Kind {
					case "row":
						row := *f.Row
						if scenario == "input-ordinal" && row.Ordinal == 2 {
							row.InputOrdinal = 2
						}
						if row.Ordinal == 3 {
							switch scenario {
							case "key":
								row.Key.ID, row.Target.Key.ID = "other-monitor", "other-monitor"
							case "source":
								row.Source = "source.00000000000000000002"
							case "document":
								row.Document++
							case "item":
								row.Item++
							case "input-ordinal":
								row.InputOrdinal = 1
							}
						}
						err = e.BeginRow(row)
					case "guards":
						err = e.WriteGuards(f.Guards)
					case "requires":
						err = e.WriteRequires(f.Requires)
					case "touches":
						err = e.WriteTouches(f.Touches)
					case "end":
						err = e.EndRow()
					}
					if err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal("mutated artifact must be otherwise structurally valid", err)
			}
			head.Plan.Descriptor = descriptor
			_, err = DecodeCollectionPlan(context.Background(), &artifact, func(fragment CollectionPlanFragment) error {
				part := CollectionPlanLedgerFragment{Ordinal: head.Plan.UploadedFragments + 1, Fragment: fragment}
				if err := s.fsm.collections.AppendPlanFragments(head.ID, []CollectionPlanLedgerFragment{part}); err != nil {
					return err
				}
				cost, _ := collectionPlanLedgerCost(head.ID, part)
				raw, _ := json.Marshal(fragment)
				head.Plan.UploadedFragments++
				head.Plan.EncodedBytes += cost
				head.Plan.ArtifactBytes += 4 + uint64(len(raw))
				head.Plan.ProgressDigest, _ = collectionPlanNextDigest(head.Plan.ProgressDigest, fragment)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s.fsm.image.Collections[head.ID] = head.Clone()
			if proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt); !errors.Is(err, ErrCollectionPlanInvalid) || proof != (CollectionPlanVerification{}) {
				t.Fatal("rehashed artifact accepted different original input metadata", err)
			}
			view, err := s.fsm.collections.Freeze()
			if err != nil {
				t.Fatal(err)
			}
			err = validateCollectionPlanRows(s.fsm.image, view)
			_ = view.Close()
			if !errors.Is(err, ErrCollectionPlanInvalid) {
				t.Fatal("snapshot accepted different original input metadata", err)
			}
		})
	}
}

type planVerificationWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *planVerificationWaitingContext) Done() <-chan struct{} {
	// collectionReadLock consults Done only after TryRLock has failed.
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestCollectionPlanVerificationCancellationWhileLedgerLockHeld(t *testing.T) {
	s, head, _ := planVerificationFixture(t, false, -1)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
	s.fsm.collections.mu.Lock()
	defer s.fsm.collections.mu.Unlock()
	completed := make(chan error, 1)
	go func() {
		_, err := s.VerifyCollectionPlan(ctx, head.ID, head.ActivityAt)
		completed <- err
	}()
	// Synchronize with the failed TryRLock wait, so cancellation cannot happen
	// before the goroutine reaches the actual lock boundary under test.
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		t.Fatal("verification did not reach its cancellable ledger-lock wait")
	}
	cancel()
	select {
	case err := <-completed:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("ledger wait lost cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("verification blocked on ledger lock after cancellation")
	}
}
