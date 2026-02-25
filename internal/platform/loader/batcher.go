package loader

import (
	"context"
	"cpra/internal/platform/loader/schema"
	"sync/atomic"
)

// batchCollector collects validated monitors and sends batches for entity creation.
// Uses bounded deduplication to prevent OOM with large monitor counts.
func (p *Pipeline) batchCollector(ctx context.Context) error {
	batch := make([]schema.Monitor, 0, p.config.BatchSize)
	seen := make(map[string]struct{})
	batchID := 0

	// Bounded deduplication: track insertion order for FIFO eviction
	maxDedup := p.config.MaxDeduplicationEntries
	var seenOrder []string
	if maxDedup > 0 {
		seenOrder = make([]string, 0, maxDedup)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case vm, ok := <-p.validatedChan:
			if !ok {
				// Channel closed, send remaining batch
				if len(batch) > 0 {
					p.batchChan <- MonitorBatch{Monitors: batch, BatchID: batchID}
					atomic.AddInt64(&p.batched, int64(len(batch)))
				}
				return nil
			}

			// Deduplicate by name
			if _, exists := seen[vm.Monitor.Name]; exists {
				atomic.AddInt64(&p.duplicates, 1)
				continue
			}

			// Bounded deduplication: evict oldest entries when at capacity
			if maxDedup > 0 && len(seen) >= maxDedup {
				// Remove oldest 10% to amortize eviction cost
				evictCount := maxDedup / 10
				if evictCount < 1 {
					evictCount = 1
				}
				for i := 0; i < evictCount && len(seenOrder) > 0; i++ {
					delete(seen, seenOrder[0])
					seenOrder = seenOrder[1:]
				}
			}

			seen[vm.Monitor.Name] = struct{}{}
			if maxDedup > 0 {
				seenOrder = append(seenOrder, vm.Monitor.Name)
			}

			batch = append(batch, vm.Monitor)

			if len(batch) >= p.config.BatchSize {
				// Send batch copy to avoid race
				batchCopy := make([]schema.Monitor, len(batch))
				copy(batchCopy, batch)
				p.batchChan <- MonitorBatch{Monitors: batchCopy, BatchID: batchID}
				atomic.AddInt64(&p.batched, int64(len(batch)))
				batchID++
				batch = batch[:0]
			}
		}
	}
}
