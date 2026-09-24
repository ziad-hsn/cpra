package persistence

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func executionProgressFixture(t *testing.T, count uint64) (CollectionExecutionProgress, []CollectionPreparedItem, []CollectionItemOutcome, []CollectionChildObservation) {
	t.Helper()
	var candidates []CollectionPreparedItem
	var outcomes []CollectionItemOutcome
	var terminals []CollectionChildObservation
	for i := uint64(1); i <= count; i++ {
		p, o, z := executionStreamFixture(t, 1, i)
		candidates = append(candidates, *p.Prepared)
		outcomes = append(outcomes, *o.Outcome)
		terminals = append(terminals, *z.Terminal)
	}
	p, err := NewCollectionExecutionProgress(candidates[0].Binding, count, candidates[0].At)
	if err != nil {
		t.Fatal(err)
	}
	return p, candidates, outcomes, terminals
}

func TestCollectionExecutionProgressAccountingMatchesLedger(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "bbolt"}[disk], func(t *testing.T) {
			p, candidates, outcomes, terminals := executionProgressFixture(t, 4)
			ledger := newLedgerTest(t, disk, 16<<20)
			tree, _ := newCollectionExecutionTerminalTree(4)
			check := func() {
				t.Helper()
				stats, err := ledger.ExecutionStats(p.Binding.OperationID)
				if err != nil || stats.Outcomes != p.Processed || stats.Terminals != p.ChildTerminals || stats.Prepared != (p.Prepared != nil) ||
					stats.EncodedBytes != p.EncodedBytes || stats.TerminalBytes != p.TerminalBytes || stats.ChargedBytes != p.ChargedBytes || stats.TerminalCapacity != int64(p.Accepted)*4096 {
					t.Fatalf("progress differs from real ledger: %+v / %+v / %v", p, stats, err)
				}
			}
			initial := p
			var err error
			// Two mutations, a conditionally unchanged row, and a blocked dependency.
			for i := 0; i < 4; i++ {
				o := outcomes[i]
				if i < 2 {
					p, err = p.withPrepared(candidates[i])
					if err != nil {
						t.Fatal(err)
					}
					if err = ledger.ApplyExecutionBatch([]collectionExecutionRecord{{Version: 1, Prepared: &candidates[i]}}); err != nil {
						t.Fatal(err)
					}
					check()
				} else {
					o.PreparedID = ""
					o.MutationSequence = 0
					o.Receipt = nil
					if i == 2 {
						o.Decision = "unchanged"
						o.OldVersion = o.Revision
					} else {
						o.Decision = "dependencyBlocked"
						o.UID = ""
						o.Revision = ""
						o.Generation = 0
					}
				}
				p, err = p.withOutcome(o)
				if err != nil {
					t.Fatal(err)
				}
				if err = ledger.ApplyExecutionBatch([]collectionExecutionRecord{{Version: 1, Outcome: &o}}); err != nil {
					t.Fatal(err)
				}
				check()
			}
			if p.Processed != 4 || p.Accepted != 2 || p.Unchanged != 1 || p.DependencyBlocked != 1 || initial.Processed != 0 || initial.Prepared != nil {
				t.Fatal("decision counts or value ownership changed")
			}
			// Arrival order differs from plan order. Quota charge remains permanent.
			charged := p.ChargedBytes
			for _, i := range []int{1, 0} {
				proof, _ := tree.Proof(terminals[i].Ordinal)
				next, e := p.withTerminal(outcomes[i], terminals[i], proof)
				if e != nil {
					t.Fatal(e)
				}
				if e = ledger.ApplyExecutionBatch([]collectionExecutionRecord{{Version: 1, Terminal: &terminals[i]}}); e != nil {
					t.Fatal(e)
				}
				if e = tree.Insert(terminals[i]); e != nil {
					t.Fatal(e)
				}
				p = next
				check()
				if p.TerminalRoot != tree.Root() || p.ChargedBytes != charged {
					t.Fatal("terminal changed prepaid quota or root")
				}
			}
			if p.ChildApplied != 2 || p.ChildTerminals != 2 {
				t.Fatal("wrong completed-child counts")
			}
		})
	}
}

func TestCollectionExecutionProgressPreparedOwnershipAndCommitment(t *testing.T) {
	initial, candidates, outcomes, _ := executionProgressFixture(t, 2)
	p, err := initial.withPrepared(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	original := p.Clone()
	retry, err := p.withPrepared(candidates[0])
	if err != nil || !reflect.DeepEqual(retry, p) {
		t.Fatal("exact retry changed candidate", err)
	}
	clone := p.Clone()
	clone.Prepared.Digest = strings.Repeat("f", 64)
	if !reflect.DeepEqual(p, original) {
		t.Fatal("clone shares prepared commitment")
	}
	changed := candidates[0].Clone()
	changed.Record.Payload.Ciphertext[0] ^= 1
	if got, err := p.withPrepared(changed); !errors.Is(err, ErrCollectionConflict) || got != (CollectionExecutionProgress{}) {
		t.Fatal("different ciphertext replaced prepared candidate", err)
	}
	if p.Prepared.Digest == "" || p.Prepared.EncodedBytes == 0 {
		t.Fatal("prepared record not committed")
	}
	cases := map[string]func(*CollectionItemOutcome){
		"identity": func(o *CollectionItemOutcome) { o.PreparedID = "66666666-6666-4666-8666-666666666666" },
		"row":      func(o *CollectionItemOutcome) { o.RowDigest = strings.Repeat("c", 64) },
		"input":    func(o *CollectionItemOutcome) { o.InputOrdinal = 2 },
		"version":  func(o *CollectionItemOutcome) { o.Revision = "changed"; o.Receipt.NewVersion = o.Revision },
		"earlier": func(o *CollectionItemOutcome) {
			o.At = o.At.Add(-time.Second)
			o.Receipt.At = o.At
			o.Receipt.UpdatedAt = o.At
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			o := outcomes[0].Clone()
			change(&o)
			if got, err := p.withOutcome(o); err == nil || got != (CollectionExecutionProgress{}) {
				t.Fatal("invalid outcome accepted")
			}
			if !reflect.DeepEqual(p, original) {
				t.Fatal("rejected transition mutated original")
			}
		})
	}
	next, err := p.withOutcome(outcomes[0])
	if err != nil {
		t.Fatal(err)
	}
	if next.Prepared != nil || next.Processed != 1 || p.Prepared == nil || p.Processed != 0 {
		t.Fatal("candidate consumption mutated prior state")
	}
	if got, err := next.withOutcome(outcomes[0]); err == nil || got != (CollectionExecutionProgress{}) {
		t.Fatal("repeated outcome appended twice")
	}
	if _, err := next.withPrepared(candidates[0]); err == nil {
		t.Fatal("consumed preparation recreated")
	}
	if _, err := initial.withOutcome(outcomes[0]); err == nil {
		t.Fatal("accepted mutation without preparation")
	}
	raw, _ := json.Marshal(p)
	if bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(candidates[0].Record.Payload.Ciphertext))) {
		t.Fatal("progress retained ciphertext")
	}
}

func TestCollectionExecutionProgressInvalidCountersAndBounds(t *testing.T) {
	empty, candidates, outcomes, terminals := executionProgressFixture(t, 1)
	p, _ := empty.withPrepared(candidates[0])
	accepted, _ := p.withOutcome(outcomes[0])
	tree, _ := newCollectionExecutionTerminalTree(1)
	proof, _ := tree.Proof(1)
	complete, err := accepted.withTerminal(outcomes[0], terminals[0], proof)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*CollectionExecutionProgress){
		"version":       func(p *CollectionExecutionProgress) { p.Version++ },
		"binding":       func(p *CollectionExecutionProgress) { p.Binding.PlanDigest = "bad" },
		"count":         func(p *CollectionExecutionProgress) { p.ItemCount = CollectionValidationMaxItems + 1 },
		"overflow":      func(p *CollectionExecutionProgress) { p.Processed = math.MaxUint64; p.Accepted = math.MaxUint64 },
		"decisions":     func(p *CollectionExecutionProgress) { p.Unchanged++ },
		"terminals":     func(p *CollectionExecutionProgress) { p.ChildTerminals++ },
		"categories":    func(p *CollectionExecutionProgress) { p.ChildSuperseded++ },
		"encoded":       func(p *CollectionExecutionProgress) { p.EncodedBytes++ },
		"charged":       func(p *CollectionExecutionProgress) { p.ChargedBytes-- },
		"terminalBytes": func(p *CollectionExecutionProgress) { p.TerminalBytes = 4097 },
		"negative":      func(p *CollectionExecutionProgress) { p.OutcomeBytes = -1 },
		"quota":         func(p *CollectionExecutionProgress) { p.ChargedBytes = maxCollectionLedgerBytes + 1 },
		"digest":        func(p *CollectionExecutionProgress) { p.OutcomeDigest = "bad" },
		"root":          func(p *CollectionExecutionProgress) { p.TerminalRoot = "bad" },
		"time":          func(p *CollectionExecutionProgress) { p.LastAt = time.Time{} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			bad := complete.Clone()
			change(&bad)
			if bad.validate() == nil {
				t.Fatal("invalid progress accepted")
			}
		})
	}
	for _, change := range []func(*CollectionExecutionProgress){func(p *CollectionExecutionProgress) { p.OutcomeDigest = strings.Repeat("a", 64) }, func(p *CollectionExecutionProgress) { p.TerminalRoot = strings.Repeat("a", 64) }} {
		bad := empty
		change(&bad)
		if bad.validate() == nil {
			t.Fatal("forged empty commitment")
		}
	}
	for _, change := range []func(*CollectionExecutionProgress){func(p *CollectionExecutionProgress) { p.Prepared.Ordinal = 2 }, func(p *CollectionExecutionProgress) { p.Prepared.InputOrdinal = 2 }, func(p *CollectionExecutionProgress) { p.Prepared.At = p.LastAt.Add(time.Second) }, func(p *CollectionExecutionProgress) { p.Prepared.EncodedBytes = collectionExecutionMaxFrame + 1 }} {
		bad := p.Clone()
		change(&bad)
		if bad.validate() == nil {
			t.Fatal("invalid prepared header")
		}
	}
	if _, err := NewCollectionExecutionProgress(empty.Binding, 0, empty.LastAt); err == nil {
		t.Fatal("empty collection count")
	}
	if _, err := NewCollectionExecutionProgress(empty.Binding, CollectionValidationMaxItems+1, empty.LastAt); err == nil {
		t.Fatal("oversized collection count")
	}
}

func TestCollectionExecutionProgressTerminalCategoriesAndDelayedObservation(t *testing.T) {
	p, candidates, outcomes, _ := executionProgressFixture(t, 4)
	var err error
	for i := range candidates {
		p, err = p.withPrepared(candidates[i])
		if err != nil {
			t.Fatal(err)
		}
		p, err = p.withOutcome(outcomes[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	tree, _ := newCollectionExecutionTerminalTree(4)
	terminals := []CollectionChildObservation{
		executionRecordTerminal(t, outcomes[0], "completed", "applied", ""),
		executionRecordTerminal(t, outcomes[1], "failed", "projection_failed", ""),
		executionRecordTerminal(t, outcomes[2], "partial", "superseded", ""),
		executionRecordTerminal(t, outcomes[3], "partial", "superseded", "restore-id"),
	}
	// Arrival two is older than unrelated already-recorded terminal one. Preserve
	// that original observation while keeping the parent maximum timestamp.
	terminals[0].UpdatedAt = terminals[0].UpdatedAt.Add(time.Hour)
	for i, z := range terminals {
		proof, _ := tree.Proof(z.Ordinal)
		p, err = p.withTerminal(outcomes[i], z, proof)
		if err != nil {
			t.Fatal(err)
		}
		if err = tree.Insert(z); err != nil {
			t.Fatal(err)
		}
	}
	if p.ChildApplied != 1 || p.ChildFailed != 1 || p.ChildSuperseded != 1 || p.ChildInvalidated != 1 || !p.LastAt.Equal(terminals[0].UpdatedAt) {
		t.Fatal("terminal categories or chronology collapsed")
	}
	proof, _ := tree.Proof(1)
	before := p.Clone()
	if got, err := p.withTerminal(outcomes[0], terminals[0], proof); !errors.Is(err, ErrCollectionConflict) || got != (CollectionExecutionProgress{}) {
		t.Fatal("duplicate terminal counted", err)
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatal("failed terminal mutated progress")
	}
}

func TestCollectionExecutionProgressBindsOriginalActivationAndStagingTime(t *testing.T) {
	s := openCatalogMemory(t)
	head, authority := activationFixture(t, s)
	at := head.ActivityAt.Add(time.Second)
	head = validationApplyAllowed(t, collectionCommand(t, s, activationCommand(head, authority, at), at))
	binding, err := collectionExecutionBindingFor(head)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewCollectionExecutionProgress(binding, head.ItemCount, at)
	if err != nil || p.validateState(head) != nil {
		t.Fatal("original activation rejected", err)
	}
	saved := head.Clone()
	for _, phase := range []string{"applying", "canceled", "invalidated"} {
		candidate := head.Clone()
		candidate.Phase = phase
		if phase != "applying" {
			candidate.TerminalAt = at.Add(time.Second)
		}
		later := p.Clone()
		later.LastAt = at.Add(time.Hour)
		if later.validateState(candidate) != nil {
			t.Fatal("unresolved children lost parent binding", phase)
		}
	}
	for _, change := range []func(*CollectionExecutionProgress){func(p *CollectionExecutionProgress) { p.Binding.UploadID = "99999999-9999-4999-8999-999999999999" }, func(p *CollectionExecutionProgress) { p.ItemCount++ }, func(p *CollectionExecutionProgress) { p.LastAt = at.Add(-time.Second) }} {
		q := p.Clone()
		change(&q)
		if q.validateState(head) == nil {
			t.Fatal("stale activation accepted")
		}
	}
	if p.validateState(CollectionState{}) == nil {
		t.Fatal("ownerless progress accepted")
	}
	if !reflect.DeepEqual(saved, head) || !head.ActivityAt.Equal(saved.ActivityAt) {
		t.Fatal("pure validation altered staging time/header")
	}
	if s.fsm.image.Version != 8 {
		t.Fatal("model-only checks changed application format")
	}
}

// Independent whole-tree reference deliberately does not use the sparse tree,
// proof, leaf or node helpers. It reconstructs fixed ordered leaves bottom-up.
func executionTerminalReferenceRoot(t *testing.T, records []CollectionChildObservation) string {
	t.Helper()
	nodes := make([][32]byte, 1<<15)
	empty := sha256.Sum256([]byte("cpra/collection/execution-terminal-empty/v1\x00"))
	for i := 1 << 14; i < 1<<15; i++ {
		nodes[i] = empty
	}
	for _, terminal := range records {
		raw, err := json.Marshal(collectionExecutionRecord{Version: 1, Terminal: &terminal})
		if err != nil {
			t.Fatal(err)
		}
		var ord [8]byte
		binary.BigEndian.PutUint64(ord[:], terminal.Ordinal)
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
		data := append([]byte("cpra/collection/execution-terminal-leaf/v1\x00"), ord[:]...)
		data = append(data, size[:]...)
		data = append(data, raw...)
		nodes[(1<<14)+int(terminal.Ordinal)-1] = sha256.Sum256(data)
	}
	for i := (1 << 14) - 1; i >= 1; i-- {
		data := append([]byte("cpra/collection/execution-terminal-node/v1\x00"), nodes[i*2][:]...)
		data = append(data, nodes[i*2+1][:]...)
		nodes[i] = sha256.Sum256(data)
	}
	return hex.EncodeToString(nodes[1][:])
}

func TestCollectionExecutionTerminalTreeSparseOrderAndClone(t *testing.T) {
	var records []CollectionChildObservation
	for _, ordinal := range []uint64{1, 2, 511, 4096, 9999, 10000} {
		_, _, r := executionStreamFixture(t, 1, ordinal)
		records = append(records, *r.Terminal)
	}
	a, _ := newCollectionExecutionTerminalTree(10000)
	b, _ := newCollectionExecutionTerminalTree(10000)
	if a.Root() != executionTerminalReferenceRoot(t, nil) {
		t.Fatal("empty tree differs from full reference")
	}
	for i := range records {
		if err := a.Insert(records[i]); err != nil {
			t.Fatal(err)
		}
		if err := b.Insert(records[len(records)-1-i]); err != nil {
			t.Fatal(err)
		}
	}
	if a.Root() != b.Root() || a.Root() != executionTerminalReferenceRoot(t, records) {
		t.Fatal("sparse root depends on arrival order or differs from full reconstruction")
	}
	clone := a.Clone()
	if clone.root != a.root {
		t.Fatal("clone did not share immutable root")
	}
	_, _, extra := executionStreamFixture(t, 1, 7000)
	if err := clone.Insert(*extra.Terminal); err != nil {
		t.Fatal(err)
	}
	if clone.Root() == a.Root() || a.Root() != b.Root() {
		t.Fatal("speculative clone changed committed cache")
	}
	var walk func(*collectionExecutionTerminalNodeValue, map[*collectionExecutionTerminalNodeValue]bool)
	walk = func(node *collectionExecutionTerminalNodeValue, set map[*collectionExecutionTerminalNodeValue]bool) {
		if node == nil {
			return
		}
		set[node] = true
		walk(node.left, set)
		walk(node.right, set)
	}
	originalNodes := make(map[*collectionExecutionTerminalNodeValue]bool)
	cloneNodes := make(map[*collectionExecutionTerminalNodeValue]bool)
	walk(a.root, originalNodes)
	walk(clone.root, cloneNodes)
	freshNodes := 0
	for node := range cloneNodes {
		if !originalNodes[node] {
			freshNodes++
		}
	}
	if len(originalNodes) > 2*collectionExecutionTerminalLeaves-1 || freshNodes != 15 {
		t.Fatal("tree path-copy bound violated", freshNodes)
	}
	proof, _ := a.Proof(7000)
	proof[0][0] ^= 1
	fresh, _ := a.Proof(7000)
	if proof[0] == fresh[0] {
		t.Fatal("proof shared cache memory")
	}
	if err := a.Insert(records[0]); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("identical leaf replaced")
	}
	changed := records[0]
	changed.Outcome = "projection_failed"
	changed.State = "failed"
	if err := a.Insert(changed); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("different terminal replaced original")
	}
}

func TestCollectionExecutionTerminalTreeRejectsBadProofsAndSlots(t *testing.T) {
	tree, _ := newCollectionExecutionTerminalTree(10)
	_, _, first := executionStreamFixture(t, 1, 1)
	if err := tree.Insert(*first.Terminal); err != nil {
		t.Fatal(err)
	}
	_, _, second := executionStreamFixture(t, 1, 2)
	raw, _ := collectionExecutionEncoding(second)
	proof, _ := tree.Proof(2)
	for name, change := range map[string]func(collectionExecutionTerminalProof) collectionExecutionTerminalProof{
		"sibling": func(p collectionExecutionTerminalProof) collectionExecutionTerminalProof { p[0][0] ^= 1; return p },
		"order": func(p collectionExecutionTerminalProof) collectionExecutionTerminalProof {
			p[0], p[1] = p[1], p[0]
			return p
		},
		"truncated": func(p collectionExecutionTerminalProof) collectionExecutionTerminalProof { return p[:len(p)-1] },
		"extended": func(p collectionExecutionTerminalProof) collectionExecutionTerminalProof {
			return append(p, [32]byte{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := append(collectionExecutionTerminalProof(nil), proof...)
			if _, err := collectionExecutionTerminalInsert(tree.Root(), 10, 2, raw, change(p)); err == nil {
				t.Fatal("bad proof accepted")
			}
		})
	}
	for _, ordinal := range []uint64{0, 3, 11, math.MaxUint64} {
		if _, err := collectionExecutionTerminalInsert(tree.Root(), 10, ordinal, raw, proof); err == nil {
			t.Fatal("wrong ordinal accepted", ordinal)
		}
	}
	_, _, third := executionStreamFixture(t, 1, 3)
	thirdRaw, _ := collectionExecutionEncoding(third)
	if _, err := collectionExecutionTerminalInsert(tree.Root(), 10, 3, thirdRaw, proof); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("proof reused at another valid slot", err)
	}
	empty, _ := newCollectionExecutionTerminalTree(10)
	stale, _ := empty.Proof(2)
	if _, err := collectionExecutionTerminalInsert(tree.Root(), 10, 2, raw, stale); !errors.Is(err, ErrCollectionConflict) {
		t.Fatal("stale cache proof accepted", err)
	}
	if _, err := collectionExecutionTerminalInsert(tree.Root(), 10, 2, append(raw, ' '), proof); err == nil {
		t.Fatal("noncanonical terminal bytes accepted")
	}
	if _, err := collectionExecutionTerminalInsert(tree.Root(), 10, 2, raw[:len(raw)-1], proof); err == nil {
		t.Fatal("truncated record accepted")
	}
	var nilTree *collectionExecutionTerminalTree
	if nilTree.Root() != "" || nilTree.Clone() != nil {
		t.Fatal("nil cache behavior")
	}
	if _, err := nilTree.Proof(1); err == nil {
		t.Fatal("nil proof accepted")
	}
	if _, err := newCollectionExecutionTerminalTree(0); err == nil {
		t.Fatal("zero tree")
	}
	if _, err := newCollectionExecutionTerminalTree(10001); err == nil {
		t.Fatal("unbounded tree")
	}
}

func TestCollectionExecutionProgressDelayedCandidateUsesOriginalStartBoundary(t *testing.T) {
	p, candidates, outcomes, terminals := executionProgressFixture(t, 3)
	p, err := p.withPrepared(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	p, err = p.withOutcome(outcomes[0])
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := newCollectionExecutionTerminalTree(3)
	proof, _ := tree.Proof(1)
	terminals[0].UpdatedAt = p.StartedAt.Add(time.Hour)
	p, err = p.withTerminal(outcomes[0], terminals[0], proof)
	if err != nil {
		t.Fatal(err)
	}
	later := p.LastAt
	beforeStart := candidates[1].Clone()
	beforeStart.At = p.StartedAt.Add(-time.Second)
	beforeStart.Record.UpdatedAt = beforeStart.At
	beforeStart.Record.CreatedAt = beforeStart.At
	if beforeStart.validate() != nil {
		t.Fatal("before-start fixture is independently invalid")
	}
	if _, err := p.withPrepared(beforeStart); err == nil {
		t.Fatal("preparation predates activation")
	}
	// These original per-item observations are valid, although another child's
	// terminal update reached the FSM first. No timestamp is rewritten.
	candidates[1].At = p.StartedAt.Add(time.Second)
	p, err = p.withPrepared(candidates[1])
	if err != nil {
		t.Fatal(err)
	}
	outcomes[1].At = candidates[1].At.Add(time.Second)
	outcomes[1].Receipt.At = outcomes[1].At
	outcomes[1].Receipt.UpdatedAt = outcomes[1].At
	tooEarly := outcomes[1].Clone()
	tooEarly.At = p.StartedAt
	tooEarly.Receipt.At = tooEarly.At
	tooEarly.Receipt.UpdatedAt = tooEarly.At
	if _, err := p.withOutcome(tooEarly); err == nil {
		t.Fatal("outcome predates its exact preparation")
	}
	p, err = p.withOutcome(outcomes[1])
	if err != nil {
		t.Fatal(err)
	}
	conflict := outcomes[2].Clone()
	conflict.Decision = "conflict"
	conflict.PreparedID = ""
	conflict.UID = ""
	conflict.Revision = ""
	conflict.Generation = 0
	conflict.MutationSequence = 0
	conflict.Receipt = nil
	conflict.At = p.StartedAt.Add(-time.Nanosecond)
	if _, err := p.withOutcome(conflict); err == nil {
		t.Fatal("unprepared decision predates activation")
	}
	conflict.At = p.StartedAt
	p, err = p.withOutcome(conflict)
	if err != nil {
		t.Fatal(err)
	}
	if !p.LastAt.Equal(later) || !p.StartedAt.Equal(candidates[0].At) || p.Processed != 3 {
		t.Fatal("delayed observation changed immutable start/latest maximum")
	}
	bad := p.Clone()
	bad.StartedAt = time.Time{}
	if bad.validate() == nil {
		t.Fatal("missing immutable start")
	}
}

func TestCollectionExecutionProgressCommitmentsMatchFramedReference(t *testing.T) {
	p, candidates, outcomes, _ := executionProgressFixture(t, 2)
	q, err := p.withPrepared(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(collectionExecutionRecord{Version: 1, Prepared: &candidates[0]})
	if err != nil {
		t.Fatal(err)
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
	data := append([]byte("cpra/collection/execution-prepared/v1\x00"), length[:]...)
	data = append(data, raw...)
	preparedSum := sha256.Sum256(data)
	if q.Prepared.Digest != hex.EncodeToString(preparedSum[:]) || q.Prepared.EncodedBytes != int64(len(raw)) {
		t.Fatal("prepared commitment differs from independent framing")
	}
	p = q
	previous := sha256.Sum256([]byte("cpra/collection/execution-outcomes/v1\x00"))
	for i := range outcomes {
		o := outcomes[i]
		if i == 1 {
			o.Decision = "conflict"
			o.PreparedID = ""
			o.UID = ""
			o.Revision = ""
			o.Generation = 0
			o.MutationSequence = 0
			o.Receipt = nil
		}
		raw, err = json.Marshal(collectionExecutionRecord{Version: 1, Outcome: &o})
		if err != nil {
			t.Fatal(err)
		}
		binary.BigEndian.PutUint32(length[:], uint32(len(raw)))
		data = append([]byte("cpra/collection/execution-outcome-prefix/v1\x00"), previous[:]...)
		data = append(data, length[:]...)
		data = append(data, raw...)
		previous = sha256.Sum256(data)
		p, err = p.withOutcome(o)
		if err != nil {
			t.Fatal(err)
		}
		if p.OutcomeDigest != hex.EncodeToString(previous[:]) {
			t.Fatal("outcome commitment differs from independent framing", i)
		}
	}
	// A different immutable conditional outcome cannot retain the original seal.
	replay, err := NewCollectionExecutionProgress(p.Binding, 2, p.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	replay, err = replay.withPrepared(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	replay, err = replay.withOutcome(outcomes[0])
	if err != nil {
		t.Fatal(err)
	}
	changed := outcomes[1]
	changed.Decision = "dependencyBlocked"
	changed.PreparedID = ""
	changed.UID = ""
	changed.Revision = ""
	changed.Generation = 0
	changed.MutationSequence = 0
	changed.Receipt = nil
	replay, err = replay.withOutcome(changed)
	if err != nil {
		t.Fatal(err)
	}
	if replay.OutcomeDigest == p.OutcomeDigest {
		t.Fatal("different decision retained original commitment")
	}
}
