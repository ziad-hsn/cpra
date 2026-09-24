package persistence

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"reflect"
	"strings"
	"testing"
	"time"
)

func retirementCheckpointFixture(t *testing.T, count uint64) (CollectionExecutionProgress, []CollectionPreparedItem, []CollectionItemOutcome, []*CollectionChildObservation) {
	t.Helper()
	initial, candidates, outcomes, _ := executionProgressFixture(t, count)
	terminals := make([]*CollectionChildObservation, count)
	for n := range outcomes {
		o := &outcomes[n]
		o.At = initial.StartedAt.Add(time.Duration(n) * time.Second)
		o.Receipt.At, o.Receipt.UpdatedAt = o.At, o.At
		switch n % 7 {
		case 1, 3, 5:
			o.Decision, o.PreparedID, o.MutationSequence, o.Receipt = "unchanged", "", 0, nil
			o.OldVersion = o.Revision
			if n%7 != 1 {
				o.Decision, o.UID, o.Revision, o.Generation, o.OldVersion = "conflict", "", "", 0, ""
				if n%7 == 5 {
					o.Decision = "dependencyBlocked"
				}
			}
		default:
			state, outcome, restore := "completed", "applied", ""
			switch n % 7 {
			case 2:
				state, outcome = "failed", "projection_failed"
			case 4:
				state, outcome = "partial", "superseded"
			case 6:
				state, outcome, restore = "partial", "superseded", "restore-checkpoint"
			}
			terminal := executionRecordTerminal(t, *o, state, outcome, restore)
			terminal.UpdatedAt = initial.StartedAt.Add(time.Duration(count+uint64(n*17)%count+1) * time.Second)
			terminals[n] = &terminal
		}
	}
	return initial, candidates, outcomes, terminals
}

func retirementCheckpointFinal(t *testing.T, initial CollectionExecutionProgress, candidates []CollectionPreparedItem, outcomes []CollectionItemOutcome, terminals []*CollectionChildObservation) CollectionExecutionProgress {
	t.Helper()
	p := initial
	var err error
	for n, outcome := range outcomes {
		if outcome.Decision == "accepted" {
			p, err = p.withPrepared(candidates[n])
			if err != nil {
				t.Fatal(err)
			}
		}
		p, err = p.withOutcome(outcome)
		if err != nil {
			t.Fatal(err)
		}
	}
	tree, _ := newCollectionExecutionTerminalTree(initial.ItemCount)
	for n := len(terminals) - 1; n >= 0; n-- {
		if terminal := terminals[n]; terminal != nil {
			proof, _ := tree.Proof(terminal.Ordinal)
			p, err = p.withTerminal(outcomes[n], *terminal, proof)
			if err != nil {
				t.Fatal(err)
			}
			if err := tree.Insert(*terminal); err != nil {
				t.Fatal(err)
			}
		}
	}
	return p
}

func TestCollectionExecutionRetirementCheckpointEveryPrefixReconstructs(t *testing.T) {
	initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 65)
	final := retirementCheckpointFinal(t, initial, candidates, outcomes, terminals)
	checkpoint, err := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	for prefix := 0; prefix <= len(outcomes); prefix++ {
		raw, err := collectionExecutionRetirementCheckpointEncoding(checkpoint)
		if err != nil {
			t.Fatal(prefix, err)
		}
		restored, err := decodeCollectionExecutionRetirementCheckpoint(raw)
		if err != nil || !collectionExecutionProgressEqual(restored.Progress, checkpoint.Progress) || !reflect.DeepEqual(restored.TerminalFrontier, checkpoint.TerminalFrontier) {
			t.Fatal("checkpoint round trip", prefix, err)
		}
		if len(restored.TerminalFrontier) != bits.OnesCount(uint(prefix)) {
			t.Fatal("frontier is not the canonical prefix decomposition")
		}
		tree, err := restored.terminalTree()
		if err != nil {
			t.Fatal(err)
		}
		before := tree.Root()
		independent, _ := newCollectionExecutionTerminalTree(initial.ItemCount)
		for n := prefix - 1; n >= 0; n-- {
			if terminals[n] != nil {
				if err := independent.Insert(*terminals[n]); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tree.Proof(uint64(n + 1)); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("retired leaf still grants a proof", prefix, n)
			}
			_, _, fabricated := executionStreamFixture(t, 1, uint64(n+1))
			if err := tree.Insert(*fabricated.Terminal); !errors.Is(err, ErrCollectionInvalid) || tree.Root() != before {
				t.Fatal("retired leaf accepted insertion, including empty leaf", prefix, n, err)
			}
		}
		if tree.Root() != independent.Root() {
			t.Fatal("prefix frontier differs from independently populated sparse tree", prefix)
		}
		clone := tree.Clone()
		for n := len(terminals) - 1; n >= prefix; n-- {
			if terminals[n] == nil {
				continue
			}
			proof, err := clone.Proof(uint64(n + 1))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Terminal: terminals[n]})
			want, err := collectionExecutionTerminalInsert(clone.Root(), initial.ItemCount, uint64(n+1), raw, proof)
			if err != nil || clone.Insert(*terminals[n]) != nil || clone.Root() != want {
				t.Fatal("suffix proof does not bind opaque prefix", prefix, n, err)
			}
		}
		if clone.Root() != final.TerminalRoot || tree.Root() != before {
			t.Fatal("suffix changed original prefix or failed final root", prefix)
		}
		for n := prefix; n < len(outcomes); n++ {
			restored, err = restored.append(outcomes[n], terminals[n])
			if err != nil {
				t.Fatal(prefix, n, err)
			}
		}
		if !restored.matchesFinal(final) {
			t.Fatal("remaining suffix failed immutable counters, digest, root or LastAt", prefix)
		}
		if prefix < len(outcomes) {
			checkpoint, err = checkpoint.append(outcomes[prefix], terminals[prefix])
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCollectionExecutionRetirementCheckpointPairsAndQuota(t *testing.T) {
	initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 7)
	final := retirementCheckpointFinal(t, initial, candidates, outcomes, terminals)
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			ledger := newLedgerTest(t, disk, 16<<20)
			for n, outcome := range outcomes {
				if outcome.Decision == "accepted" {
					executionLedgerApply(t, ledger, collectionExecutionRecord{Version: 1, Prepared: &candidates[n]})
				}
				executionLedgerApply(t, ledger, collectionExecutionRecord{Version: 1, Outcome: &outcome})
				if terminals[n] != nil {
					executionLedgerApply(t, ledger, collectionExecutionRecord{Version: 1, Terminal: terminals[n]})
				}
			}
			stats, err := ledger.ExecutionStats(initial.Binding.OperationID)
			if err != nil || stats.ChargedBytes != final.ChargedBytes || stats.EncodedBytes != final.EncodedBytes {
				t.Fatal("live progress and physical ledger accounting differ", err)
			}
			checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
			var outcomeBytes, terminalBytes, accepted int64
			for n, outcome := range outcomes {
				before := checkpoint.Clone()
				if terminals[n] != nil {
					if got, err := checkpoint.append(outcome, nil); !errors.Is(err, ErrCollectionInvalid) || !reflect.DeepEqual(got, CollectionExecutionRetirementCheckpoint{}) || !reflect.DeepEqual(checkpoint, before) {
						t.Fatal("missing accepted terminal released a charge or changed prefix", err)
					}
					accepted++
					terminalRaw, _ := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Terminal: terminals[n]})
					terminalBytes += int64(len(terminalRaw))
				} else if _, err := checkpoint.append(outcome, terminals[0]); !errors.Is(err, ErrCollectionInvalid) {
					t.Fatal("nonaccepted decision consumed a terminal")
				}
				outcomeRaw, _ := collectionExecutionEncoding(collectionExecutionRecord{Version: 1, Outcome: &outcome})
				outcomeBytes += int64(len(outcomeRaw))
				checkpoint, err = checkpoint.append(outcome, terminals[n])
				if err != nil || checkpoint.Progress.OutcomeBytes != outcomeBytes || checkpoint.Progress.TerminalBytes != terminalBytes || checkpoint.Progress.EncodedBytes != outcomeBytes+terminalBytes || checkpoint.Progress.ChargedBytes != outcomeBytes+accepted*collectionChildTerminalReserve {
					t.Fatal("retired prefix did not preserve exact paired accounting", n, err)
				}
			}
			if !checkpoint.matchesFinal(final) {
				t.Fatal("full retirement changed original accounting")
			}
			after, err := ledger.ExecutionStats(initial.Binding.OperationID)
			if err != nil || after != stats {
				t.Fatal("checkpoint codec changed physical records", err)
			}
		})
	}
}

func TestCollectionExecutionRetirementCheckpointRejectsMalformedAndUnlinkedPrefixes(t *testing.T) {
	initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 7)
	final := retirementCheckpointFinal(t, initial, candidates, outcomes, terminals)
	empty, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
	checkpoint, _ := empty.append(outcomes[0], terminals[0])
	raw, _ := collectionExecutionRetirementCheckpointEncoding(checkpoint)
	for name, mutate := range map[string]func(*CollectionExecutionRetirementCheckpoint){
		"version": func(c *CollectionExecutionRetirementCheckpoint) { c.Version++ },
		"prepared": func(c *CollectionExecutionRetirementCheckpoint) {
			q, _ := collectionExecutionPreparedCommitment(candidates[1])
			c.Progress.Prepared = &q
		},
		"nil frontier": func(c *CollectionExecutionRetirementCheckpoint) { c.TerminalFrontier = nil },
		"extra frontier": func(c *CollectionExecutionRetirementCheckpoint) {
			c.TerminalFrontier = append(c.TerminalFrontier, strings.Repeat("a", 64))
		},
		"changed frontier":          func(c *CollectionExecutionRetirementCheckpoint) { c.TerminalFrontier[0] = strings.Repeat("b", 64) },
		"invalid hash":              func(c *CollectionExecutionRetirementCheckpoint) { c.TerminalFrontier[0] = strings.Repeat("A", 64) },
		"missing accepted terminal": func(c *CollectionExecutionRetirementCheckpoint) { c.Progress.ChildTerminals-- },
		"count limit": func(c *CollectionExecutionRetirementCheckpoint) {
			c.Progress.ItemCount = CollectionValidationMaxItems + 1
		},
		"frontier limit": func(c *CollectionExecutionRetirementCheckpoint) { c.TerminalFrontier = make([]string, 15) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := checkpoint.Clone()
			mutate(&bad)
			encoded, _ := json.Marshal(bad)
			if _, err := decodeCollectionExecutionRetirementCheckpoint(encoded); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("malformed checkpoint decoded", err)
			}
		})
	}
	for _, bad := range [][]byte{
		append(bytes.Clone(raw), '\n'), append(bytes.Clone(raw), []byte(`{}`)...),
		append([]byte(`{"version":1,`), raw[1:]...), append([]byte(`{"unknown":1,`), raw[1:]...),
		bytes.Repeat([]byte(" "), collectionExecutionRetirementCheckpointMaxBytes+1),
	} {
		if _, err := decodeCollectionExecutionRetirementCheckpoint(bad); !errors.Is(err, ErrCollectionInvalid) {
			t.Fatal("noncanonical checkpoint decoded", err)
		}
	}
	zeroRaw, _ := collectionExecutionRetirementCheckpointEncoding(empty)
	if !bytes.Contains(zeroRaw, []byte(`"terminal_frontier":[]`)) {
		t.Fatal("zero prefix has no deterministic empty frontier")
	}
	badZero := empty.Clone()
	badZero.Progress.LastAt = badZero.Progress.LastAt.Add(time.Second)
	if badZero.validate() == nil {
		t.Fatal("empty prefix fabricated activity")
	}
	for _, corrupt := range []string{"outcome digest", "coherent false frontier"} {
		bad := checkpoint.Clone()
		if corrupt == "outcome digest" {
			bad.Progress.OutcomeDigest = strings.Repeat("d", 64)
		} else {
			bad.TerminalFrontier[0] = strings.Repeat("c", 64)
			frontier, _ := bad.frontierHashes()
			emptyHashes := collectionExecutionTerminalEmptyHashes()
			bad.Progress.TerminalRoot = bad.prefixTree(frontier, &emptyHashes).Root()
		}
		if bad.validate() != nil {
			t.Fatal("fixture should be structurally valid; immutable linkage is the failing check")
		}
		for n := 1; n < len(outcomes); n++ {
			var err error
			bad, err = bad.append(outcomes[n], terminals[n])
			if err != nil {
				t.Fatal(err)
			}
		}
		if bad.matchesFinal(final) {
			t.Fatal("corrupted retired prefix plus genuine suffix matched immutable final", corrupt)
		}
	}
	for name, change := range map[string]func(*CollectionItemOutcome, *CollectionChildObservation){
		"gap": func(o *CollectionItemOutcome, z *CollectionChildObservation) { o.Ordinal++; z.Ordinal++ },
		"binding": func(o *CollectionItemOutcome, z *CollectionChildObservation) {
			o.Binding.PlanDigest = strings.Repeat("c", 64)
			z.Binding = o.Binding
		},
		"input bound":    func(o *CollectionItemOutcome, _ *CollectionChildObservation) { o.InputOrdinal = initial.ItemCount + 1 },
		"child mismatch": func(_ *CollectionItemOutcome, z *CollectionChildObservation) { z.ChildID = ledgerTestOperation(90) },
		"early terminal": func(o *CollectionItemOutcome, z *CollectionChildObservation) { z.UpdatedAt = o.At.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			o, z := outcomes[0].Clone(), *terminals[0]
			change(&o, &z)
			if _, err := empty.append(o, &z); !errors.Is(err, ErrCollectionInvalid) {
				t.Fatal("unlinked outcome/terminal accepted", err)
			}
		})
	}
	if _, err := checkpoint.append(outcomes[0], terminals[0]); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("repeated outcome advanced prefix")
	}
}

func TestCollectionExecutionRetirementCheckpointPreparedLastAt(t *testing.T) {
	initial, candidates, outcomes, terminals := retirementCheckpointFixture(t, 3)
	final := retirementCheckpointFinal(t, initial, candidates[:1], outcomes[:1], terminals[:1])
	prepared := candidates[1].Clone()
	prepared.At = final.LastAt.Add(time.Hour)
	var err error
	final, err = final.withPrepared(prepared)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(initial.Binding, initial.ItemCount, initial.StartedAt)
	checkpoint, err = checkpoint.append(outcomes[0], terminals[0])
	if err != nil || !checkpoint.matchesFinal(final) || checkpoint.Progress.Prepared != nil || !checkpoint.Progress.LastAt.Before(final.LastAt) || checkpoint.Progress.ChargedBytes+final.Prepared.EncodedBytes != final.ChargedBytes {
		t.Fatal("abandoned prepared slot lost its independent bytes or LastAt", err)
	}
	changed := final.Clone()
	changed.LastAt = changed.LastAt.Add(time.Second)
	if checkpoint.matchesFinal(changed) {
		t.Fatal("unexplained LastAt passed full reconstruction")
	}
}

func TestCollectionExecutionRetirementCheckpointAllEmptyTerminalLeaves(t *testing.T) {
	prepared, _, _ := executionStreamFixture(t, 1, 1)
	checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(prepared.Prepared.Binding, 65, prepared.Prepared.At)
	for ordinal := uint64(1); ordinal <= 65; ordinal++ {
		_, record, _ := executionStreamFixture(t, 1, ordinal)
		o := record.Outcome
		o.Decision, o.PreparedID, o.Receipt, o.UID, o.Revision, o.Generation, o.MutationSequence = "conflict", "", nil, "", "", 0, 0
		var err error
		checkpoint, err = checkpoint.append(*o, nil)
		if err != nil || checkpoint.Progress.TerminalRoot != collectionExecutionTerminalEmptyRoot() || checkpoint.Progress.Accepted != 0 {
			t.Fatal("empty decision leaf changed root or accepted count", ordinal, err)
		}
		tree, err := checkpoint.terminalTree()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tree.Proof(ordinal); !errors.Is(err, ErrCollectionInvalid) {
			t.Fatal("all-empty retired prefix granted proof", ordinal)
		}
	}
	bad := checkpoint.Clone()
	bad.TerminalFrontier[0] = strings.Repeat("a", 64)
	frontier, _ := bad.frontierHashes()
	empty := collectionExecutionTerminalEmptyHashes()
	bad.Progress.TerminalRoot = bad.prefixTree(frontier, &empty).Root()
	if bad.validate() == nil {
		t.Fatal("zero accepted count allowed fabricated occupied subtree")
	}
}

func TestCollectionExecutionRetirementCheckpointMaximumCountAndBoundedFrontier(t *testing.T) {
	prepared, _, _ := executionStreamFixture(t, 1, 1)
	checkpoint, _ := NewCollectionExecutionRetirementCheckpoint(prepared.Prepared.Binding, CollectionValidationMaxItems, prepared.Prepared.At)
	independent, _ := newCollectionExecutionTerminalTree(CollectionValidationMaxItems)
	maxBytes, maxNodes := 0, 0
	var nodes func(*collectionExecutionTerminalNodeValue) int
	nodes = func(n *collectionExecutionTerminalNodeValue) int {
		if n == nil {
			return 0
		}
		return 1 + nodes(n.left) + nodes(n.right)
	}
	for ordinal := uint64(1); ordinal <= CollectionValidationMaxItems; ordinal++ {
		_, outcome, terminal := executionStreamFixture(t, 1, ordinal)
		o := outcome.Outcome
		var z *CollectionChildObservation
		if ordinal%17 == 0 || ordinal == 1 || ordinal == CollectionValidationMaxItems {
			z = terminal.Terminal
			if err := independent.Insert(*z); err != nil {
				t.Fatal(err)
			}
		} else {
			o.Decision, o.PreparedID, o.Receipt, o.UID, o.Revision, o.Generation, o.MutationSequence = "conflict", "", nil, "", "", 0, 0
		}
		var err error
		checkpoint, err = checkpoint.append(*o, z)
		if err != nil || checkpoint.Progress.TerminalRoot != independent.Root() {
			t.Fatal(ordinal, err)
		}
		if ordinal&(ordinal-1) != 0 && ordinal&(ordinal+1) != 0 && ordinal != CollectionValidationMaxItems {
			continue
		}
		raw, err := collectionExecutionRetirementCheckpointEncoding(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		maxBytes = max(maxBytes, len(raw))
		tree, err := checkpoint.terminalTree()
		if err != nil {
			t.Fatal(err)
		}
		maxNodes = max(maxNodes, nodes(tree.tree.root))
		if len(checkpoint.TerminalFrontier) > collectionExecutionTerminalDepth || nodes(tree.tree.root) > 2*collectionExecutionTerminalDepth {
			t.Fatal("retired prefix retained more than logarithmic tree state")
		}
		if _, err := decodeCollectionExecutionRetirementCheckpoint(raw); err != nil {
			t.Fatal(err)
		}
	}
	_, repeated, z := executionStreamFixture(t, 1, CollectionValidationMaxItems)
	if _, err := checkpoint.append(*repeated.Outcome, z.Terminal); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("full checkpoint advanced")
	}
	tree, _ := checkpoint.terminalTree()
	if _, err := tree.Proof(0); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("zero proof")
	}
	if _, err := tree.Proof(CollectionValidationMaxItems + 1); !errors.Is(err, ErrCollectionInvalid) {
		t.Fatal("proof beyond original input")
	}
	raw, _ := collectionExecutionRetirementCheckpointEncoding(checkpoint)
	t.Logf("maximum-count canonical bytes=%d, sampled maximum bytes=%d, prefix tree nodes=%d, cap=%d", len(raw), maxBytes, maxNodes, collectionExecutionRetirementCheckpointMaxBytes)
	// An opaque hash is bounded metadata, never an embedded retired payload.
	for _, h := range checkpoint.TerminalFrontier {
		if decoded, err := hex.DecodeString(h); err != nil || len(decoded) != 32 {
			t.Fatal("frontier hash", err)
		}
	}
}
