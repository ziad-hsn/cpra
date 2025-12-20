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

// stringBuilderPool reduces allocations by reusing strings.Builder instances.
// Each builder is pre-allocated with capacity for a typical monitor (~1KB).
var stringBuilderPool = sync.Pool{
	New: func() interface{} {
		b := &strings.Builder{}
		b.Grow(1024) // Pre-allocate for typical monitor size
		return b
	},
}

func getStringBuilder() *strings.Builder {
	return stringBuilderPool.Get().(*strings.Builder)
}

func putStringBuilder(b *strings.Builder) {
	b.Reset()
	stringBuilderPool.Put(b)
}

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
// Uses streaming mode if configured, otherwise loads full yaml.Node tree.
func (p *Pipeline) readYAMLNodes(ctx context.Context, filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file size for progress reporting
	var totalSize int64
	if stat, err := file.Stat(); err == nil {
		totalSize = stat.Size()
	}

	var r io.Reader = file
	isGzip := strings.HasSuffix(strings.ToLower(filename), ".gz")
	if isGzip {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gz.Close()
		r = gz
		totalSize = 0 // Can't know decompressed size
	}

	// Use streaming mode for large files to avoid OOM
	if p.config.StreamingMode {
		return p.readYAMLStreaming(ctx, r, totalSize)
	}

	// Traditional mode: load full yaml.Node tree
	return p.readYAMLTraditional(ctx, r, totalSize)
}

// readYAMLTraditional loads the full yaml.Node tree into memory.
// Fast but uses ~500MB+ for 1M monitors - may OOM.
func (p *Pipeline) readYAMLTraditional(ctx context.Context, r io.Reader, totalSize int64) error {
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

// readYAMLStreaming parses YAML line-by-line to minimize memory usage.
// Accumulates lines for each monitor (between `-` markers) and sends raw bytes.
// Uses ~10MB for 1M monitors instead of 500MB+.
func (p *Pipeline) readYAMLStreaming(ctx context.Context, r io.Reader, totalSize int64) error {
	// Create counting reader for progress
	cr := &countingReader{reader: r, totalSize: totalSize}

	// Use large buffer for better I/O performance
	bufSize := p.config.BufferSize
	if bufSize <= 0 {
		bufSize = 4 * 1024 * 1024 // 4MB default for streaming
	}
	scanner := bufio.NewScanner(cr)
	scanner.Buffer(make([]byte, bufSize), bufSize)

	var (
		currentMonitor = getStringBuilder() // Use pooled builder
		inMonitors     bool                 // True after seeing "monitors:" line
		inMonitor      bool                 // True when accumulating a monitor
		lineNum        int
		monitorLine    int
		lastProgress   time.Time
		progressEvery  = p.config.ProgressInterval
		gcCounter      int // Counter for periodic GC hints
	)
	defer putStringBuilder(currentMonitor)

	if progressEvery <= 0 {
		progressEvery = 250 * time.Millisecond
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Check for context cancellation and report progress periodically
		if lineNum%10000 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Report progress
			if p.config.ProgressCallback != nil && time.Since(lastProgress) >= progressEvery {
				lastProgress = time.Now()
				p.config.ProgressCallback(LoadProgress{
					BytesRead:      cr.bytesRead,
					TotalBytes:     totalSize,
					MonitorsParsed: atomic.LoadInt64(&p.rawParsed),
					Elapsed:        time.Since(p.startTime),
					Stage:          "reading",
				})
			}
		}

		trimmed := strings.TrimSpace(line)

		// Look for "monitors:" to start parsing
		if !inMonitors {
			if trimmed == "monitors:" || strings.HasPrefix(trimmed, "monitors:") {
				inMonitors = true
			}
			continue
		}

		// Detect start of a new monitor (line starting with "- " at proper indent)
		// A monitor entry starts with "  - " (2 space indent + dash)
		if len(line) >= 2 && line[0] == ' ' && line[1] == ' ' {
			restTrimmed := strings.TrimLeft(line[2:], " ")
			if strings.HasPrefix(restTrimmed, "- ") || restTrimmed == "-" {
				// Flush previous monitor
				if inMonitor && currentMonitor.Len() > 0 {
					raw := RawMonitor{
						RawBytes: []byte(currentMonitor.String()),
						Line:     monitorLine,
					}
					select {
					case p.rawChan <- raw:
						atomic.AddInt64(&p.rawParsed, 1)
						gcCounter++
						// Hint GC every 50k monitors to reclaim memory
						if gcCounter%50000 == 0 {
							runtime.GC()
						}
					case <-ctx.Done():
						return ctx.Err()
					}
					currentMonitor.Reset()
				}
				inMonitor = true
				monitorLine = lineNum
			}
		}

		// Accumulate lines for current monitor
		if inMonitor {
			currentMonitor.WriteString(line)
			currentMonitor.WriteByte('\n')
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	// Flush last monitor
	if inMonitor && currentMonitor.Len() > 0 {
		raw := RawMonitor{
			RawBytes: []byte(currentMonitor.String()),
			Line:     monitorLine,
		}
		select {
		case p.rawChan <- raw:
			atomic.AddInt64(&p.rawParsed, 1)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Final progress report
	if p.config.ProgressCallback != nil {
		p.config.ProgressCallback(LoadProgress{
			BytesRead:      cr.bytesRead,
			TotalBytes:     totalSize,
			MonitorsParsed: atomic.LoadInt64(&p.rawParsed),
			Elapsed:        time.Since(p.startTime),
			Stage:          "reading",
		})
	}

	return nil
}

// countingReader wraps an io.Reader to track bytes read.
type countingReader struct {
	reader    io.Reader
	bytesRead int64
	totalSize int64
}

func (c *countingReader) Read(p []byte) (n int, err error) {
	n, err = c.reader.Read(p)
	c.bytesRead += int64(n)
	return n, err
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

// parseMonitorFromBytes parses a single monitor from raw YAML bytes.
// The input is expected to be a list item like:
//
//   - name: foo
//     pulse:
//     type: http
//     ...
//
// We convert it to a proper YAML document for parsing.
func (p *Pipeline) parseMonitorFromBytes(rawBytes []byte, monitor *schema.Monitor) error {
	// The raw bytes contain a list item starting with "  - "
	// We need to convert it to a standalone YAML document
	// by stripping the leading "  - " and reducing indentation

	lines := strings.Split(string(rawBytes), "\n")
	if len(lines) == 0 {
		return fmt.Errorf("empty monitor bytes")
	}

	var normalized strings.Builder
	normalized.Grow(len(rawBytes))

	for i, line := range lines {
		if len(line) == 0 {
			continue
		}

		if i == 0 {
			// First line: strip the leading spaces and "- " or just "-"
			trimmed := strings.TrimLeft(line, " ")
			if strings.HasPrefix(trimmed, "- ") {
				// "- name: foo" -> "name: foo"
				normalized.WriteString(trimmed[2:])
				normalized.WriteByte('\n')
			} else if trimmed == "-" {
				// Just "-", next lines have the content
				continue
			} else {
				normalized.WriteString(trimmed)
				normalized.WriteByte('\n')
			}
		} else {
			// Subsequent lines: remove 4 spaces of indentation (list item indent)
			if len(line) >= 4 && line[:4] == "    " {
				normalized.WriteString(line[4:])
			} else if len(line) >= 2 && line[:2] == "  " {
				// Sometimes only 2 space indent after the "- "
				normalized.WriteString(line[2:])
			} else {
				normalized.WriteString(strings.TrimLeft(line, " "))
			}
			normalized.WriteByte('\n')
		}
	}

	return yaml.Unmarshal([]byte(normalized.String()), monitor)
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
