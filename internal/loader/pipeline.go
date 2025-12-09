package loader

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cpra/internal/controller/entities"
	"cpra/internal/loader/schema"

	"github.com/mlange-42/ark/ecs"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

// Pipeline orchestrates concurrent loading of monitor configurations.
type Pipeline struct {
	config        PipelineConfig
	world         *ecs.World
	entityManager *entities.EntityManager
	validator     *MonitorValidator

	// Channels for pipeline stages
	rawChan       chan RawMonitor
	validatedChan chan ValidatedMonitor
	batchChan     chan MonitorBatch

	// Statistics (atomic for thread-safety)
	rawParsed  int64
	validated  int64
	skipped    int64
	duplicates int64
	batched    int64
	created    int64
	pulseRate  float64

	startTime time.Time
	mu        sync.RWMutex
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
	var workerWg sync.WaitGroup
	for i := 0; i < p.config.Workers; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			p.worker(ctx)
		}()
	}

	// Close validated channel when all workers are done
	go func() {
		workerWg.Wait()
		close(p.validatedChan)
	}()

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

// readYAMLNodes reads the YAML file and sends raw nodes to the channel.
func (p *Pipeline) readYAMLNodes(ctx context.Context, filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var r io.Reader = file
	if strings.HasSuffix(strings.ToLower(filename), ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gz.Close()
		r = gz
	}

	bufSize := p.config.BufferSize
	if bufSize <= 0 {
		bufSize = 64 * 1024
	}
	bufr := bufio.NewReaderSize(r, bufSize)

	decoder := yaml.NewDecoder(bufr)
	decoder.KnownFields(p.config.StrictUnknownFields)

	// Decode top-level structure
	var topLevel struct {
		Monitors yaml.Node `yaml:"monitors"`
	}
	if err := decoder.Decode(&topLevel); err != nil {
		if err == io.EOF {
			return nil // Empty file is not an error
		}
		return fmt.Errorf("failed to decode top-level: %w", err)
	}

	if topLevel.Monitors.Kind != yaml.SequenceNode {
		return fmt.Errorf("'monitors' field must be a YAML sequence")
	}

	// Send each monitor node to the workers
	for _, node := range topLevel.Monitors.Content {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			raw := RawMonitor{Node: node, Line: node.Line}
			select {
			case p.rawChan <- raw:
				atomic.AddInt64(&p.rawParsed, 1)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	return nil
}

// worker processes raw YAML nodes, parses them, and validates them.
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
			if err := raw.Node.Decode(&monitor); err != nil {
				atomic.AddInt64(&p.skipped, 1)
				continue
			}

			// Skip empty or malformed entries
			if monitor.Name == "" && monitor.Pulse.Type == "" {
				atomic.AddInt64(&p.skipped, 1)
				continue
			}

			// Validate
			if err := p.validator.Validate(&monitor); err != nil {
				atomic.AddInt64(&p.skipped, 1)
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

// batchCollector collects validated monitors and sends batches for entity creation.
func (p *Pipeline) batchCollector(ctx context.Context) error {
	batch := make([]schema.Monitor, 0, p.config.BatchSize)
	seen := make(map[string]struct{})
	batchID := 0

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
			seen[vm.Monitor.Name] = struct{}{}

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
