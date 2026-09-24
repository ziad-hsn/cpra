package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These fixtures exercise the codec's broader ordering contract. Credential
// touches are structural test metadata, not a claim that the current compiler
// emits such graphs or that any provider/catalog operation has been validated.
func executionIndexFixture(t *testing.T, disk bool, mode string) (*Store, CollectionState) {
	t.Helper()
	var s *Store
	if disk {
		cfg := testConfig(t)
		admin := openAuthenticationAdmin(t, cfg)
		if _, err := admin.CommitAuthentication(context.Background(), authenticationBootstrap()); err != nil {
			t.Fatal(err)
		}
		if err := admin.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		s, err = Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
	} else {
		s = openCatalogMemory(t)
	}
	count := 3
	if mode == "large" || mode == "over-limit" {
		count = 1
	}
	head, authority := validationApplyInput(t, s, count)
	inputs, err := s.fsm.collections.Page(head.ID, 0, 256)
	if err != nil || len(inputs) != count {
		t.Fatal("fixture input", err)
	}
	header := planCodecHeader(head.ItemCount)
	header.OperationID, header.UploadID, header.Actor = head.ID, head.UploadID, head.Actor
	header.IdentityFormat, header.ContentDigest, header.InputProgressDigest = head.IdentityFormat, head.ContentDigest, head.ProgressDigest
	s.fsm.mu.RLock()
	header.ObservedIndex = s.fsm.image.Index
	s.fsm.mu.RUnlock()
	var artifact bytes.Buffer
	rows := make(map[uint64]CollectionPlanRow)
	emit := func(e *CollectionPlanEncoder) error {
		if mode == "large" || mode == "over-limit" {
			row := planCodecRow(1, 1, inputs[0].Key, "create")
			guards := 7000
			if mode == "over-limit" {
				guards = 10001
			}
			row.GuardCount = uint64(guards)
			rows[row.InputOrdinal] = row
			if err := e.BeginRow(row); err != nil {
				return err
			}
			for at := 0; at < guards; {
				chunk := make([]CollectionPlanGuard, 0, 256)
				for len(chunk) < cap(chunk) && at < guards {
					chunk = append(chunk, CollectionPlanGuard{Key: CatalogKey{"Credential", fmt.Sprintf("unselected-%08d", at)},
						OriginalUID: strings.Repeat("u", 256), OriginalRevision: strings.Repeat("v", 256), OriginalGeneration: 1})
					if mode == "over-limit" {
						token := uint64(0)
						chunk[len(chunk)-1].ReverseVersion = &token
					}
					at++
				}
				if err := e.WriteGuards(chunk); err != nil {
					return err
				}
			}
			return e.EndRow()
		}
		a := planCodecRow(1, 2, inputs[1].Key, "update")
		b := planCodecRow(2, 3, inputs[2].Key, "update")
		c := planCodecRow(3, 1, inputs[0].Key, "update")
		*a.Target.ReverseVersion, *b.Target.ReverseVersion, *c.Target.ReverseVersion = 3, 7, 11
		if mode == "zero-token" {
			*c.Target.ReverseVersion = 0
		}
		future := c.Target
		if mode == "contradictory-token" {
			wrong := uint64(22)
			future.ReverseVersion = &wrong
		} else if mode == "contradictory-tuple" {
			future.OriginalUID = "different-original-uid"
		}
		a.GuardCount, a.TouchesCount = 1, 1
		rows[a.InputOrdinal] = a
		if err := e.BeginRow(a); err != nil {
			return err
		}
		if err := e.WriteGuards([]CollectionPlanGuard{future}); err != nil {
			return err
		}
		if err := e.WriteTouches([]CatalogKey{c.Key}); err != nil {
			return err
		}
		if err := e.EndRow(); err != nil {
			return err
		}
		b.GuardCount, b.RequiresCount, b.TouchesCount = 1, 1, 1
		rows[b.InputOrdinal] = b
		if err := e.BeginRow(b); err != nil {
			return err
		}
		prior := a.Target
		prior.FromOrdinal = 1
		if err := e.WriteGuards([]CollectionPlanGuard{prior}); err != nil {
			return err
		}
		if err := e.WriteRequires([]uint64{1}); err != nil {
			return err
		}
		if err := e.WriteTouches([]CatalogKey{c.Key}); err != nil {
			return err
		}
		if err := e.EndRow(); err != nil {
			return err
		}
		// Row3 deliberately has no Requires. A future executor must still
		// certify earlier touches1 and2 before substituting its reverse token.
		rows[c.InputOrdinal] = c
		if err := e.BeginRow(c); err != nil {
			return err
		}
		return e.EndRow()
	}
	descriptor, err := EncodeCollectionPlan(context.Background(), &artifact, header, emit)
	if err != nil {
		t.Fatal("fixture codec", err)
	}
	var parts []CollectionPlanLedgerFragment
	if _, err := DecodeCollectionPlan(context.Background(), bytes.NewReader(artifact.Bytes()), func(f CollectionPlanFragment) error {
		parts = append(parts, CollectionPlanLedgerFragment{Ordinal: uint64(len(parts) + 1), Fragment: f})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	head = planApplyAll(t, s, planApplyBegin(t, s, head, CollectionPlanBegin{Header: header, Descriptor: descriptor}), parts)
	proof, err := s.VerifyCollectionPlan(context.Background(), head.ID, head.ActivityAt)
	if err != nil {
		t.Fatal(err)
	}
	head = validationApplyAllowed(t, collectionCommand(t, s, CollectionCommand{Action: "plan_finalize", OperationID: head.ID, UploadID: head.UploadID, PlanFinalize: &proof}, head.ActivityAt.Add(time.Millisecond)))
	begin := CollectionValidationBegin{Header: CollectionValidationHeader{ResultID: uuid.NewString(), OperationID: head.ID, UploadID: head.UploadID,
		InputProgressDigest: head.ProgressDigest, ItemCount: head.ItemCount, Authority: authority, CapabilitiesDigest: strings.Repeat("c", 64),
		Valid: true, PlanID: header.PlanID, PlanDigest: descriptor.Digest}, Descriptor: CollectionValidationDescriptor{Digest: CollectionValidationInitialDigest()}}
	var results []CollectionValidationItem
	for _, input := range inputs {
		row := rows[input.Ordinal]
		item := CollectionValidationItem{Ordinal: input.Ordinal, Key: input.Key, Source: input.Source, Document: input.SourceDocument,
			Item: input.SourceItem, Change: row.Change, UID: row.Target.OriginalUID, ResourceVersion: row.Target.OriginalRevision}
		digest, cost, err := CollectionValidationNextDigest(begin.Descriptor.Digest, item)
		if err != nil {
			t.Fatal(err)
		}
		begin.Descriptor.Count++
		begin.Descriptor.Bytes += cost
		begin.Descriptor.Digest = digest
		results = append(results, item)
	}
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_begin", &begin, nil))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_append", nil, results))
	head = validationApplyAllowed(t, validationApplyCommand(t, s, head, "validation_finalize", nil, nil))
	for !head.Validation.HistorySealed {
		head = validationApplyAllowed(t, validationPublishStep(t, s, head, head.ActivityAt.Add(time.Millisecond)))
	}
	return s, head
}

func executionIndexView(t *testing.T, s *Store) *collectionLedgerView {
	t.Helper()
	v, err := s.fsm.collections.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func TestCollectionExecutionIndexOriginalBindingsAndTouchObligations(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			s, head := executionIndexFixture(t, disk, "normal")
			v := executionIndexView(t, s)
			before, _ := json.Marshal(s.fsm.image)
			tx := 0
			if disk {
				tx = ledgerTestTransactionID(t, s.fsm.collections)
			}
			x, err := buildCollectionExecutionIndex(context.Background(), head, v, defaultCollectionExecutionIndexLimits())
			if err != nil {
				t.Fatal(err)
			}
			if !x.matches(head) || len(x.rows) != 3 || len(x.reverse) != 3 || x.touches != 2 {
				t.Fatal("missing bounded original metadata")
			}
			third, ok := x.row(3)
			if !ok || third.Row.InputOrdinal != 1 || third.Row.RequiresCount != 0 || third.Row.Source != "source.00000000000000000001" || third.Row.Item != 1 {
				t.Fatal("execution order lost original source position")
			}
			original, last, ok := x.reverseBaseline(third.Row.Key)
			if !ok || last != 3 || original.FromOrdinal != 0 || *original.ReverseVersion != 11 || original.OriginalUID != third.Row.Target.OriginalUID {
				t.Fatal("reverse baseline replaced target identity")
			}
			var prior []uint64
			if err := x.walkPriorTouches(context.Background(), third.Row.Key, 3, func(n uint64) error { prior = append(prior, n); return nil }); err != nil || !reflect.DeepEqual(prior, []uint64{1, 2}) {
				t.Fatal("touch predecessors absent from Requires were lost", prior, err)
			}
			*original.ReverseVersion = 999
			*third.Row.Target.ReverseVersion = 999
			again, _ := x.row(3)
			fresh, _, _ := x.reverseBaseline(again.Row.Key)
			if *again.Row.Target.ReverseVersion != 11 || *fresh.ReverseVersion != 11 {
				t.Fatal("mutable alias")
			}
			changed := head.Clone()
			changed.Validation.Header.ResultID = uuid.NewString()
			if x.matches(changed) {
				t.Fatal("index accepted another original result")
			}
			after, _ := json.Marshal(s.fsm.image)
			if !bytes.Equal(before, after) || disk && tx != ledgerTestTransactionID(t, s.fsm.collections) {
				t.Fatal("index construction wrote durable state")
			}
		})
	}
}

func TestCollectionExecutionIndexRejectsContradictoryOriginals(t *testing.T) {
	for _, mode := range []string{"contradictory-token", "contradictory-tuple"} {
		t.Run(mode, func(t *testing.T) {
			s, head := executionIndexFixture(t, false, mode)
			if x, err := buildCollectionExecutionIndex(context.Background(), head, executionIndexView(t, s), defaultCollectionExecutionIndexLimits()); x != nil || !errors.Is(err, ErrCollectionPlanInvalid) {
				t.Fatal("contradictory baseline accepted", err)
			}
		})
	}
}

func TestCollectionExecutionIndexRejectsIncompleteCorruptOrUnsealed(t *testing.T) {
	for _, mode := range []string{"unsealed", "unfinalized", "removed", "wrong-descriptor", "truncated", "row-digest", "missing-input", "input-index", "input-ciphertext"} {
		t.Run(mode, func(t *testing.T) {
			s, head := executionIndexFixture(t, false, "normal")
			v := executionIndexView(t, s)
			switch mode {
			case "unsealed":
				head.Validation.HistorySealed = false
			case "unfinalized":
				head.Plan.FinalizedAt = time.Time{}
			case "removed":
				head.Plan.RemovedFragments, head.Plan.RemovedBytes = 1, 1
			case "wrong-descriptor":
				head.Plan.Descriptor.Digest = strings.Repeat("d", 64)
			case "truncated":
				delete(v.planRows[head.ID], head.Plan.UploadedFragments)
			case "missing-input":
				delete(v.rows[head.ID], 1)
			case "input-index":
				v.keys[head.ID][CatalogKey{"Credential", "item-00001"}] = 2
			case "input-ciphertext":
				row, err := decodeCollectionLedgerRow(v.rows[head.ID][1])
				if err != nil {
					t.Fatal(err)
				}
				row.Item.Payload.Ciphertext[0] ^= 1
				v.rows[head.ID][1], err = collectionItemEncoding(head.ID, row.Item)
				if err != nil {
					t.Fatal(err)
				}
			case "row-digest":
				for ordinal, raw := range v.planRows[head.ID] {
					row, err := decodeCollectionPlanLedgerRow(raw)
					if err != nil {
						t.Fatal(err)
					}
					if row.Part.Fragment.End != nil {
						row.Part.Fragment.End.Digest = strings.Repeat("f", 64)
						v.planRows[head.ID][ordinal], err = collectionPlanLedgerEncoding(head.ID, row.Part)
						if err != nil {
							t.Fatal(err)
						}
						break
					}
				}
			}
			if x, err := buildCollectionExecutionIndex(context.Background(), head, v, defaultCollectionExecutionIndexLimits()); x != nil || err == nil {
				t.Fatal("invalid original produced index")
			}
		})
	}
}

func TestCollectionExecutionIndexLimitsCancellationAndRangeRecheck(t *testing.T) {
	s, head := executionIndexFixture(t, false, "normal")
	v := executionIndexView(t, s)
	for _, mutate := range []func(*collectionExecutionIndexLimits){
		func(l *collectionExecutionIndexLimits) { l.Rows = 2 },
		func(l *collectionExecutionIndexLimits) { l.ReverseKeys = 1 },
		func(l *collectionExecutionIndexLimits) { l.Touches = 1 },
		func(l *collectionExecutionIndexLimits) { l.Bytes = 2048 },
		func(l *collectionExecutionIndexLimits) { l.Work = 3 },
	} {
		limits := defaultCollectionExecutionIndexLimits()
		mutate(&limits)
		if x, err := buildCollectionExecutionIndex(context.Background(), head, v, limits); x != nil || !errors.Is(err, errCollectionExecutionIndexLimit) {
			t.Fatal("quota not explicit", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if x, err := buildCollectionExecutionIndex(ctx, head, v, defaultCollectionExecutionIndexLimits()); x != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		base, stop := context.WithCancel(context.Background())
		defer stop()
		waiting := &planVerificationWaitingContext{Context: base, waiting: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			_, err := buildCollectionExecutionIndex(waiting, head, v, defaultCollectionExecutionIndexLimits())
			done <- err
		}()
		// Done is reached only after the held view's TryLock fails.
		select {
		case <-waiting.waiting:
		case <-time.After(time.Second):
			t.Fatal("constructor did not reach contended view lock")
		}
		stop()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("view lock wait ignored cancellation", err)
			}
		case <-time.After(time.Second):
			t.Fatal("canceled view lock wait did not return")
		}
	}()
	x, err := buildCollectionExecutionIndex(context.Background(), head, v, defaultCollectionExecutionIndexLimits())
	if err != nil {
		t.Fatal(err)
	}
	callbacks := 0
	ctx, cancel = context.WithCancel(context.Background())
	if err := x.walkRow(ctx, v, 1, func(CollectionPlanFragment) error { callbacks++; cancel(); return nil }); !errors.Is(err, context.Canceled) || callbacks != 1 {
		t.Fatal("row stream ignored cancellation", err)
	}
	first, _ := x.row(1)
	part, err := decodeCollectionPlanLedgerRow(v.planRows[head.ID][first.FirstFragment])
	if err != nil {
		t.Fatal(err)
	}
	part.Part.Fragment.Row.Source = "source.00000000000000000002"
	v.planRows[head.ID][first.FirstFragment], err = collectionPlanLedgerEncoding(head.ID, part.Part)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.walkRow(context.Background(), v, 1, func(CollectionPlanFragment) error { return nil }); !errors.Is(err, ErrCollectionPlanInvalid) {
		t.Fatal("range digest failed to fence substituted row", err)
	}
}

func TestCollectionExecutionIndexStreamsLargeLogicalRow(t *testing.T) {
	s, head := executionIndexFixture(t, false, "large")
	if head.Plan.Descriptor.Bytes <= 4<<20 {
		t.Fatal("fixture did not exceed command budget")
	}
	v := executionIndexView(t, s)
	x, err := buildCollectionExecutionIndex(context.Background(), head, v, defaultCollectionExecutionIndexLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(x.rows) != 1 || len(x.reverse) != 0 || x.bytes > 8<<10 {
		t.Fatal("guards retained in derived metadata")
	}
	var guards int
	if err := x.walkRow(context.Background(), v, 1, func(f CollectionPlanFragment) error {
		if len(f.Guards) > 256 {
			t.Fatal("unbounded guard callback")
		}
		guards += len(f.Guards)
		return nil
	}); err != nil || guards != 7000 {
		t.Fatal("large row lost original fragments", guards, err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := x.walkRow(context.Background(), v, 1, func(CollectionPlanFragment) error { return nil }); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal("index retained another view", err)
	}
}

func TestCollectionExecutionIndexDistinguishesZeroFromAbsent(t *testing.T) {
	s, head := executionIndexFixture(t, false, "zero-token")
	x, err := buildCollectionExecutionIndex(context.Background(), head, executionIndexView(t, s), defaultCollectionExecutionIndexLimits())
	if err != nil {
		t.Fatal(err)
	}
	row, _ := x.row(3)
	original, last, found := x.reverseBaseline(row.Row.Key)
	if !found || last != 3 || original.Absent || original.ReverseVersion == nil || *original.ReverseVersion != 0 || row.Row.Target.ReverseVersion == nil || *row.Row.Target.ReverseVersion != 0 {
		t.Fatal("present zero reverse token became absent")
	}
	createdStore := openCatalogMemory(t)
	created, _ := activationFixture(t, createdStore)
	creates, err := buildCollectionExecutionIndex(context.Background(), created, executionIndexView(t, createdStore), defaultCollectionExecutionIndexLimits())
	if err != nil {
		t.Fatal(err)
	}
	first, _ := creates.row(1)
	if !first.Row.Target.Absent || first.Row.Target.ReverseVersion != nil || len(creates.reverse) != 0 {
		t.Fatal("absent target invented a zero token baseline")
	}
	if _, _, found := creates.reverseBaseline(first.Row.Key); found {
		t.Fatal("absent target claimed original reverse guard")
	}
}
