package loader

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"cpra/internal/controller/entities"
	"cpra/internal/platform/loader/schema"

	"github.com/mlange-42/ark/ecs"
	"golang.org/x/sync/errgroup"
)

// Pipeline orchestrates concurrent loading of monitor configurations.
type Pipeline struct {
	startTime     time.Time
	world         *ecs.World
	entityManager *entities.EntityManager
	validator     *MonitorValidator
	rawChan       chan RawMonitor
	validatedChan chan ValidatedMonitor
	batchChan     chan MonitorBatch
	config        PipelineConfig
	validated     int64
	skipped       int64
	duplicates    int64
	batched       int64
	created       int64
	pulseRate     float64
	rawParsed     int64
	mu            sync.RWMutex
}

// NewPipeline creates a new concurrent loading pipeline.
func NewPipeline(world *ecs.World, entityManager *entities.EntityManager, config PipelineConfig) *Pipeline {
	if config.Workers <= 0 {
		config.Workers = runtime.NumCPU() * 2
	}
	if config.Workers > 1000 {
		config.Workers = 1000
	}

	return &Pipeline{
		config:        config,
		world:         world,
		entityManager: entityManager,
		validator:     NewValidator(),
		rawChan:       make(chan RawMonitor, config.RawChannelSize),
		validatedChan: make(chan ValidatedMonitor, config.ValidatedChannelSize),
		batchChan:     make(chan MonitorBatch, config.BatchChannelSize),
	}
}

// Load runs the complete pipeline to load monitors from a file.
// Uses errgroup for clean error propagation - first error cancels all stages.
// All goroutines are tracked to prevent leaks (per "Concurrency in Go" p. 90).
func (p *Pipeline) Load(ctx context.Context, filename string) (*PipelineStats, error) {
	p.startTime = time.Now()

	// errgroup.WithContext: first error cancels ctx, Wait returns first error
	g, ctx := errgroup.WithContext(ctx)

	// Stage 1: Sequential file reading (I/O bound)
	g.Go(func() error {
		defer close(p.rawChan)
		return p.readYAMLNodes(ctx, filename)
	})

	// Stage 2: Fan-out to workers (CPU bound parse + validate)
	// Workers are tracked via errgroup to prevent leaks on context cancellation.
	var workerWg sync.WaitGroup
	for i := 0; i < p.config.Workers; i++ {
		workerWg.Add(1)
		g.Go(func() error {
			defer workerWg.Done()
			p.worker(ctx)
			return nil
		})
	}

	// Close validated channel when all workers are done.
	// This is tracked via errgroup to ensure clean shutdown.
	g.Go(func() error {
		workerWg.Wait()
		close(p.validatedChan)
		return nil
	})

	// Stage 3: Fan-in and batch collection
	g.Go(func() error {
		defer close(p.batchChan)
		return p.batchCollector(ctx)
	})

	// Stage 4: Sequential entity creation (Ark constraint)
	g.Go(func() error {
		return p.createEntities(ctx)
	})

	// Wait for all stages - returns first error
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return p.buildStats(), nil
}

// worker processes raw YAML nodes or bytes, parses them, and validates them.
func (p *Pipeline) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case raw, ok := <-p.rawChan:
			if !ok {
				return
			}

			var monitor schema.Monitor
			var err error

			// Handle streaming mode (raw bytes) vs traditional mode (yaml.Node)
			if raw.RawBytes != nil {
				// Streaming mode: parse from raw YAML bytes
				// The bytes represent a single monitor entry like:
				//   - name: foo
				//     pulse: ...
				// We need to parse just the map contents (without the leading "- ")
				err = p.parseMonitorFromBytes(raw.RawBytes, &monitor)
			} else if raw.Node != nil {
				// Traditional mode: decode from yaml.Node
				err = raw.Node.Decode(&monitor)
			} else {
				atomic.AddInt64(&p.skipped, 1)
				continue
			}

			if err != nil {
				atomic.AddInt64(&p.skipped, 1)
				continue
			}

			// Skip empty or malformed entries
			if monitor.Name == "" && monitor.Pulse.Type == "" {
				atomic.AddInt64(&p.skipped, 1)
				continue
			}

			// Apply defaults after decoding, before validation
			schema.DefaultConfig.Apply(&monitor)

			// Validate
			if err := p.validator.Validate(&monitor); err != nil {
				atomic.AddInt64(&p.skipped, 1)
				// Log validation errors when enabled for debugging bad configs
				if p.config.LogValidationErrors && p.config.Logger != nil {
					p.config.Logger.Warnf("Validation failed for monitor %q (line %d): %v",
						monitor.Name, raw.Line, err)
				}
				if p.config.FailFast {
					return
				}
				continue
			}

			atomic.AddInt64(&p.validated, 1)

			// Send to batch collector
			select {
			case p.validatedChan <- ValidatedMonitor{Monitor: monitor, Line: raw.Line}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// createEntities creates ECS entities from batches.
func (p *Pipeline) createEntities(ctx context.Context) error {
	var totalPulseRate float64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case batch, ok := <-p.batchChan:
			if !ok {
				p.mu.Lock()
				p.pulseRate = totalPulseRate
				p.mu.Unlock()
				return nil
			}

			// Calculate pulse rate
			for _, monitor := range batch.Monitors {
				if monitor.Enabled && monitor.Pulse.Interval > 0 {
					sec := monitor.Pulse.Interval.Seconds()
					if sec > 0 {
						totalPulseRate += 1.0 / sec
					}
				}
			}

			// Use batch API for efficient entity creation
			if err := p.entityManager.CreateEntitiesFromMonitors(p.world, batch.Monitors); err != nil {
				return fmt.Errorf("failed to create batch %d: %w", batch.BatchID, err)
			}

			atomic.AddInt64(&p.created, int64(len(batch.Monitors)))
		}
	}
}

// buildStats builds the final statistics.
func (p *Pipeline) buildStats() *PipelineStats {
	elapsed := time.Since(p.startTime)
	created := atomic.LoadInt64(&p.created)

	var parseRate, creationRate float64
	if elapsed.Seconds() > 0 {
		parseRate = float64(atomic.LoadInt64(&p.validated)) / elapsed.Seconds()
		creationRate = float64(created) / elapsed.Seconds()
	}

	p.mu.RLock()
	pulseRate := p.pulseRate
	p.mu.RUnlock()

	return &PipelineStats{
		TotalMonitors:     atomic.LoadInt64(&p.rawParsed),
		EntitiesCreated:   created,
		SkippedMonitors:   atomic.LoadInt64(&p.skipped),
		DuplicateMonitors: atomic.LoadInt64(&p.duplicates),
		LoadingTime:       elapsed,
		ParseRate:         parseRate,
		CreationRate:      creationRate,
		PulseRate:         pulseRate,
	}
}
