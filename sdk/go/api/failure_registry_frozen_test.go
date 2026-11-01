package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestFailureTaxonomyOneHasFrozenMeaning(t *testing.T) {
	var vocabulary any
	if err := json.Unmarshal(failureCodeBytes, &vocabulary); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	const frozen = "411304eb3bf4dcaa0079034d644f8df420aac14241a7d754c2e63910a766e713"
	if hex.EncodeToString(digest[:]) != frozen {
		t.Fatal("taxonomy 1 changed: preserve its existing vocabulary and add a new explicit version")
	}
}
