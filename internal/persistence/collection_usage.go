package persistence

import (
	"math"
	"slices"
	"time"
)

// Samples cover only ledger.db and generation.json in catalogued completed
// generations. They are not a recursive inventory or total host disk usage.
type collectionUsageSample struct {
	apparent, allocated int64
	allocationAvailable bool
	observedAt          time.Time
}

func (g *collectionGenerations) publishUsage(status *CollectionStorageAccounting) {
	status.UsageExpected = len(g.catalog.Completed)
	status.UsageCoverage = "pending"
	status.AllocationAvailable = true
	for name, sample := range g.usage {
		if !slices.Contains(g.catalog.Completed, name) {
			delete(g.usage, name)
			continue
		}
		if sample.apparent < 0 || sample.allocated < 0 || sample.apparent > math.MaxInt64-status.KnownApparentBytes || sample.allocated > math.MaxInt64-status.KnownAllocatedBytes {
			delete(g.usage, name)
			continue
		}
		status.UsageGenerations++
		status.KnownApparentBytes += sample.apparent
		status.KnownAllocatedBytes += sample.allocated
		status.AllocationAvailable = status.AllocationAvailable && sample.allocationAvailable
		if status.UsageOldestAt.IsZero() || sample.observedAt.Before(status.UsageOldestAt) {
			status.UsageOldestAt = sample.observedAt
		}
	}
	switch {
	case status.UsageGenerations == status.UsageExpected:
		status.UsageCoverage = "complete"
	case status.UsageGenerations > 0:
		status.UsageCoverage = "partial"
	case g.usageAttempted:
		status.UsageCoverage = "unavailable"
	}
	if status.UsageExpected > 0 && status.UsageGenerations == 0 {
		status.AllocationAvailable = false
	}
}
