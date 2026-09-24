package persistence

import (
	"reflect"
	"strings"
	"testing"
)

func TestCollectionExecutionCommitmentsImmutableChunkBoundaries(t *testing.T) {
	_, _, outcomes, _ := executionProgressFixture(t, 130)
	cache, err := newCollectionExecutionOutcomeCommitments(outcomes[0].Binding)
	if err != nil {
		t.Fatal(err)
	}
	originals := make([]*collectionExecutionOutcomeCommitments, 0, 130)
	for n, o := range outcomes {
		old := cache
		originals = append(originals, old)
		cache, err = cache.append(o)
		if err != nil {
			t.Fatal(err)
		}
		if old.Count != uint64(n) || cache.Count != uint64(n+1) || old.Digest == cache.Digest {
			t.Fatal("append mutated prior commitment")
		}
		if !cache.matchesRecord(collectionExecutionRecord{Version: 1, Outcome: &o}) {
			t.Fatal("new outcome absent")
		}
		if old.matchesRecord(collectionExecutionRecord{Version: 1, Outcome: &o}) {
			t.Fatal("prior prefix certified future record")
		}
		if n == 126 || n == 127 || n == 128 {
			for j := 0; j <= n; j++ {
				if !cache.matchesRecord(collectionExecutionRecord{Version: 1, Outcome: &outcomes[j]}) {
					t.Fatal("lost hash at chunk boundary")
				}
			}
		}
	}
	if len(cache.chunks) != 2 || cache.chunks[0] != originals[128].chunks[0] || cache.chunks[1] == originals[129].chunks[1] {
		t.Fatal("completed chunks not shared or active chunk mutated")
	}
	before := *cache
	wrong := outcomes[129].Clone()
	wrong.Ordinal = 132
	if _, err := cache.append(wrong); err == nil {
		t.Fatal("accepted noncontiguous append")
	}
	wrong.Ordinal = 131
	wrong.Binding.ActivationID = "99999999-9999-4999-8999-999999999999"
	if _, err := cache.append(wrong); err == nil {
		t.Fatal("accepted different binding")
	}
	if !reflect.DeepEqual(before, *cache) {
		t.Fatal("rejected append changed cache")
	}
	wrong = outcomes[0].Clone()
	wrong.OldVersion = "changed"
	if cache.matchesRecord(collectionExecutionRecord{Version: 1, Outcome: &wrong}) {
		t.Fatal("canonical conflicting outcome retained certification")
	}
	if cache.matches(cache.Binding, cache.Count, strings.Repeat("b", 64)) {
		t.Fatal("wrong authoritative digest matched")
	}
	if originals[0].Count != 0 || len(originals[0].chunks) != 0 || originals[0].Digest != collectionExecutionOutcomeInitialDigest() {
		t.Fatal("empty cache changed")
	}
	cost, err := collectionExecutionCommitmentCost(CollectionValidationMaxItems)
	if err != nil || cost*maxCollectionOperations > collectionExecutionCommitmentAuditBytes {
		t.Fatal("documented aggregate allocation cap too small")
	}
	if _, err := collectionExecutionCommitmentCost(CollectionValidationMaxItems + 1); err == nil {
		t.Fatal("unbounded commitment count")
	}
}
