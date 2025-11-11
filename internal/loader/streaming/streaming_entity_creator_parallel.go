package streaming

import (
	"context"
	"cpra/internal/controller/entities"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// ParallelEntityCreator processes batches in parallel for better CPU utilization.
type ParallelEntityCreator struct {
	startTime        time.Time
	world            *ecs.World
	entityManager    *entities.EntityManager
	entitiesCreated  int64
	batchesProcessed int64
	pulseRate        float64
	mu               sync.RWMutex
	numWorkers       int
}

// NewParallelEntityCreator creates a parallel entity creator.
func NewParallelEntityCreator(world *ecs.World, config EntityCreationConfig) *ParallelEntityCreator {
	numWorkers := config.MaxWorkers
	if numWorkers <= 0 {
		numWorkers = 4 // Default to 4 workers
	}

	creator := &ParallelEntityCreator{
		world:         world,
		entityManager: entities.NewEntityManager(world),
		startTime:     time.Now(),
		numWorkers:    numWorkers,
	}

	if config.PreAllocate > 0 {
		creator.preAllocateEntities(config.PreAllocate)
	}

	return creator
}

// preAllocateEntities pre-allocates entity storage.
func (c *ParallelEntityCreator) preAllocateEntities(count int) {
	tempEntities := make([]ecs.Entity, count)
	for i := 0; i < count; i++ {
		tempEntities[i] = c.world.NewEntity()
	}
	for _, entity := range tempEntities {
		c.world.RemoveEntity(entity)
	}
}

// ProcessBatches processes monitor batches in parallel using a worker pool.
func (c *ParallelEntityCreator) ProcessBatches(ctx context.Context, batchChan <-chan MonitorBatch, progressChan chan<- EntityProgress) error {
	progressTicker := time.NewTicker(2 * time.Second)
	defer progressTicker.Stop()

	// Worker pool for parallel batch processing
	var wg sync.WaitGroup
	errChan := make(chan error, c.numWorkers)
	workerBatchChan := make(chan MonitorBatch, c.numWorkers*2)

	// Start workers
	for i := 0; i < c.numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range workerBatchChan {
				if err := c.processBatch(batch); err != nil {
					select {
					case errChan <- err:
					default:
					}
					return
				}
			}
		}()
	}

	// Progress reporting goroutine
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		for {
			select {
			case <-progressTicker.C:
				c.reportProgress(progressChan)
			case <-ctx.Done():
				return
			case <-progressDone:
				return
			}
		}
	}()

	// Feed batches to workers
	for {
		select {
		case <-ctx.Done():
			close(workerBatchChan)
			wg.Wait()
			return ctx.Err()

		case batch, ok := <-batchChan:
			if !ok {
				// No more batches - wait for workers to finish
				close(workerBatchChan)
				wg.Wait()
				close(progressDone)
				c.reportProgress(progressChan)

				// Check for errors
				select {
				case err := <-errChan:
					return err
				default:
					return nil
				}
			}

			// Send to worker pool
			select {
			case workerBatchChan <- batch:
			case err := <-errChan:
				close(workerBatchChan)
				wg.Wait()
				return err
			case <-ctx.Done():
				close(workerBatchChan)
				wg.Wait()
				return ctx.Err()
			}
		}
	}
}

// processBatch creates entities for a single batch.
func (c *ParallelEntityCreator) processBatch(batch MonitorBatch) error {
	// Create entities using batch API
	if err := c.entityManager.CreateEntitiesFromMonitors(c.world, batch.Monitors); err != nil {
		return fmt.Errorf("failed to create entities for batch %d: %w", batch.BatchID, err)
	}

	// Calculate pulse rate for this batch
	var pulseSum float64
	for _, monitor := range batch.Monitors {
		if monitor.Enabled && monitor.Pulse.Interval > 0 {
			sec := monitor.Pulse.Interval.Seconds()
			if sec > 0 {
				pulseSum += 1.0 / sec
			}
		}
	}

	// Update counters atomically
	atomic.AddInt64(&c.entitiesCreated, int64(len(batch.Monitors)))
	atomic.AddInt64(&c.batchesProcessed, 1)

	// Update pulse rate with lock
	c.mu.Lock()
	c.pulseRate += pulseSum
	c.mu.Unlock()

	return nil
}

// reportProgress sends a progress update.
func (c *ParallelEntityCreator) reportProgress(progressChan chan<- EntityProgress) {
	if progressChan == nil {
		return
	}

	entitiesCreated := atomic.LoadInt64(&c.entitiesCreated)
	batchesProcessed := atomic.LoadInt64(&c.batchesProcessed)

	elapsed := time.Since(c.startTime)
	rate := 0.0
	if elapsed.Seconds() > 0 {
		rate = float64(entitiesCreated) / elapsed.Seconds()
	}

	select {
	case progressChan <- EntityProgress{
		EntitiesCreated:  entitiesCreated,
		BatchesProcessed: batchesProcessed,
		Rate:             rate,
	}:
	default:
	}
}

// PulseRate returns the aggregated expected pulse arrival rate (jobs/sec).
func (c *ParallelEntityCreator) PulseRate() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pulseRate
}

// GetStats returns current creation statistics.
func (c *ParallelEntityCreator) GetStats() (entitiesCreated int64, batchesProcessed int64, rate float64) {
	entitiesCreated = atomic.LoadInt64(&c.entitiesCreated)
	batchesProcessed = atomic.LoadInt64(&c.batchesProcessed)

	elapsed := time.Since(c.startTime)
	if elapsed.Seconds() == 0 {
		return entitiesCreated, batchesProcessed, 0
	}
	return entitiesCreated, batchesProcessed, float64(entitiesCreated) / elapsed.Seconds()
}
