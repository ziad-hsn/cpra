package httpserver

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCollectionUsageWireAvailabilityAndMeasuredZero(t *testing.T) {
	for _, coverage := range []string{"pending", "unavailable", "partial", "complete"} {
		t.Run(coverage, func(t *testing.T) {
			accounting := persistence.CollectionStorageAccounting{Available: true, UsageCoverage: coverage, UsageExpected: 2}
			if coverage == "partial" || coverage == "complete" {
				accounting.UsageGenerations = 1
			}
			if coverage == "complete" {
				accounting.UsageGenerations = 2
				accounting.AllocationAvailable = true
			}
			raw, err := json.Marshal(collectionStorageObservation(accounting))
			if err != nil {
				t.Fatal(err)
			}
			var response api.CollectionStorageAccounting
			if err := api.DecodeResponse(raw, &response); err != nil {
				t.Fatal(err)
			}
			if response.UsageCoverage == nil || string(*response.UsageCoverage) != coverage || response.UsageExpected == nil || *response.UsageExpected != 2 {
				t.Fatal("coverage was lost on the SDK wire", string(raw))
			}
			if coverage == "pending" || coverage == "unavailable" {
				if response.KnownApparentBytes != nil || response.KnownAllocatedBytes != nil {
					t.Fatal("unavailable bytes fabricated as zero", string(raw))
				}
			} else if response.KnownApparentBytes == nil || *response.KnownApparentBytes != 0 {
				t.Fatal("measured zero omitted", string(raw))
			}
			if coverage == "complete" && !bytes.Contains(raw, []byte(`"knownAllocatedBytes":0`)) {
				t.Fatal("zero allocation omitted", string(raw))
			}
			if coverage == "partial" && response.KnownAllocatedBytes != nil {
				t.Fatal("unsupported allocation presented as measured", string(raw))
			}
		})
	}
}
