package persistence

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCollectionProfileV1OnlyReconcilesCurrentCreates(t *testing.T) {
	s := openCatalogMemory(t)
	c := ticketCreateFixture(t, s)
	c.Create.NormalizationProfile = "cpra.file.base.v1"
	at := c.Create.CreatedAt
	if r := collectionCommand(t, s, c, at); !errors.Is(r.Err, ErrCollectionInvalid) || s.fsm.image.OperationHighWater != 0 {
		t.Fatal("unused v1 ticket allocated new state", r.Err)
	}
	// The retained historical envelope establishes the original operation.
	original := applyCatalogFormat(t, s, CollectionReselectionFormatVersion, Command{Kind: "collection", At: at, Collection: &c})[0]
	if original.Err != nil {
		t.Fatal(original.Err)
	}
	retry := ticketRetry(t, s, c, at.Add(time.Second))
	r := collectionCommand(t, s, retry, retry.Create.CreatedAt)
	if r.Err != nil || r.CollectionID != original.CollectionID || s.fsm.image.OperationHighWater != 1 {
		t.Fatal("consumed v1 ticket lost original handle", r.Err)
	}
}

func TestCollectionProfileV2RequiresLifecycleFormat(t *testing.T) {
	s := openCatalogMemory(t)
	c := ticketCreateFixture(t, s)
	c.Create.NormalizationProfile = "cpra.file.base.v2"
	command := Command{Kind: "collection", At: c.Create.CreatedAt, Collection: &c}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, err := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeEnvelope(raw)
		if (err == nil) != lifecycleStorageFormat(version) {
			t.Fatalf("v2 under format %d: %v", version, err)
		}
	}
	r := collectionCommand(t, s, c, c.Create.CreatedAt)
	if r.Err != nil || !lifecycleStorageFormat(s.fsm.image.Version) {
		t.Fatal("v2 create", r.Err)
	}
	if err := validateCollectionHeaders(s.fsm.image); err != nil {
		t.Fatal(err)
	}
	old := s.fsm.image
	old.Version = DeliveryFormatVersion
	if err := validateCollectionHeaders(old); err == nil {
		t.Fatal("v2 accepted in old snapshot")
	}
}

func TestCollectionProfileV2RetirementFormat(t *testing.T) {
	for _, source := range []bool{false, true} {
		for _, nested := range []bool{false, true} {
			fence := &CollectionExecutionRetirementFence{}
			if nested {
				fence.Retirement = &CollectionExecutionRetirementState{}
				fence.Retirement.Result.Summary.NormalizationProfile = "cpra.file.base.v2"
			} else {
				fence.Result.Summary.NormalizationProfile = "cpra.file.base.v2"
			}
			c := Command{Kind: "collection_execute", CollectionExecute: &CollectionExecuteCommand{Retirement: fence}}
			if source {
				c.CollectionExecute.Retirement = nil
				c.CollectionExecute.SourceRetirement = &CollectionExecutionSourceRetirementFence{Retirement: fence}
			}
			if commandMinimumFormat(c) != LifecycleFormatVersion {
				t.Fatal("retained v2 summary did not require format 42")
			}
		}
	}
}

func TestCollectionProfileV3RequiresActionFormat(t *testing.T) {
	s := openCatalogMemory(t)
	c := ticketCreateFixture(t, s)
	c.Create.NormalizationProfile = "cpra.file.base.v3"
	command := Command{Kind: "collection", At: c.Create.CreatedAt, Collection: &c}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, err := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeEnvelope(raw)
		if (err == nil) != actionStorageFormat(version) {
			t.Fatalf("v3 under format %d: %v", version, err)
		}
	}
	r := collectionCommand(t, s, c, c.Create.CreatedAt)
	if r.Err != nil || !actionStorageFormat(s.fsm.image.Version) {
		t.Fatal("v3 create", r.Err)
	}
	if err := validateCollectionHeaders(s.fsm.image); err != nil {
		t.Fatal(err)
	}
	old := s.fsm.image
	old.Version = LifecycleFormatVersion
	if err := validateCollectionHeaders(old); err == nil {
		t.Fatal("v3 accepted in old snapshot")
	}
}
