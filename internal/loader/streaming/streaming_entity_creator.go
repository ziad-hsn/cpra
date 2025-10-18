package streaming

import (
	"context"
	"cpra/internal/controller/entities"
	"fmt"
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// StreamingEntityCreator handles batch entity creation for Ark ECS.
// It now uses the consolidated EntityManager to create entities.
type StreamingEntityCreator struct {
	startTime        time.Time
	world            *ecs.World
	entityManager    *entities.EntityManager
	entitiesCreated  int64
	batchesProcessed int64
	pulseRate        float64
	mu               sync.RWMutex
}

// EntityCreationConfig holds entity creation configuration.
type EntityCreationConfig struct {
	ProgressChan chan<- EntityProgress
	BatchSize    int
	PreAllocate  int
}

// EntityProgress represents entity creation progress.
type EntityProgress struct {
	EntitiesCreated  int64
	BatchesProcessed int64
	Rate             float64
	MemoryUsage      int64
}

// NewStreamingEntityCreator creates a new, simplified entity creator.
func NewStreamingEntityCreator(world *ecs.World, config EntityCreationConfig) *StreamingEntityCreator {
	creator := &StreamingEntityCreator{
		world:         world,
		entityManager: entities.NewEntityManager(world),
		startTime:     time.Now(),
	}

	if config.PreAllocate > 0 {
		creator.preAllocateEntities(config.PreAllocate)
	}

	return creator
}

// preAllocateEntities pre-allocates entity storage to reduce allocations during creation.
func (c *StreamingEntityCreator) preAllocateEntities(count int) {
	tempEntities := make([]ecs.Entity, count)
	for i := 0; i < count; i++ {
		tempEntities[i] = c.world.NewEntity()
	}
	for _, entity := range tempEntities {
		c.world.RemoveEntity(entity)
	}
}

// ProcessBatches processes monitor batches and creates entities.
func (c *StreamingEntityCreator) ProcessBatches(ctx context.Context, batchChan <-chan MonitorBatch, progressChan chan<- EntityProgress) error {
	progressTicker := time.NewTicker(2 * time.Second)
	defer progressTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-progressTicker.C:
			c.reportProgress(progressChan)

		case batch, ok := <-batchChan:
			if !ok {
				c.reportProgress(progressChan)
				return nil
			}

			if err := c.processBatch(batch); err != nil {
				return fmt.Errorf("failed to process batch %d: %w", batch.BatchID, err)
			}
		}
	}
}

// processBatch creates entities for a single batch of monitors.
func (c *StreamingEntityCreator) processBatch(batch MonitorBatch) error {
	var pulseSum float64
	for _, monitor := range batch.Monitors {
		if err := c.entityManager.CreateEntityFromMonitor(&monitor, c.world); err != nil {
			return fmt.Errorf("failed to create entity for monitor '%s': %w", monitor.Name, err)
		}
		if monitor.Enabled && monitor.Pulse.Interval > 0 {
			sec := monitor.Pulse.Interval.Seconds()
			if sec > 0 {
				pulseSum += 1.0 / sec
			}
		}
	}

	c.mu.Lock()
	c.entitiesCreated += int64(len(batch.Monitors))
	c.batchesProcessed++
	c.pulseRate += pulseSum
	c.mu.Unlock()

	return nil
}

// reportProgress sends a progress update.
func (c *StreamingEntityCreator) reportProgress(progressChan chan<- EntityProgress) {
	if progressChan == nil {
		return
	}

	c.mu.RLock()
	entitiesCreated := c.entitiesCreated
	batchesProcessed := c.batchesProcessed
	c.mu.RUnlock()

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
func (c *StreamingEntityCreator) PulseRate() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pulseRate
}

// GetStats returns current creation statistics.
func (c *StreamingEntityCreator) GetStats() (entitiesCreated int64, batchesProcessed int64, rate float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	elapsed := time.Since(c.startTime)
	if elapsed.Seconds() == 0 {
		return c.entitiesCreated, c.batchesProcessed, 0
	}
	return c.entitiesCreated, c.batchesProcessed, float64(c.entitiesCreated) / elapsed.Seconds()
}
