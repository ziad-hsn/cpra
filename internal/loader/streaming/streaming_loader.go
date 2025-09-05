package streaming

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"
	
	"github.com/mlange-42/ark/ecs"
)

// StreamingLoader orchestrates the streaming loading process
type StreamingLoader struct {
	filename    string
	world       *ecs.World
	config      StreamingConfig
	
	// Progress channels
	parseProgress  chan Progress
	entityProgress chan EntityProgress
	
	// Statistics
	totalStartTime time.Time
	loadingStats   LoadingStats
}

// StreamingConfig holds all streaming configuration
type StreamingConfig struct {
	// Parser config
	ParseBatchSize   int   // Monitors per parse batch
	ParseBufferSize  int   // File read buffer size
	MaxParseMemory   int64 // Maximum memory for parsing
	
	// Entity creation config
	EntityBatchSize  int   // Entities per creation batch
	PreAllocateCount int   // Pre-allocate entity storage
	
	// Performance config
	MaxWorkers       int   // Max concurrent workers (for non-ECS operations)
	ProgressInterval time.Duration // Progress reporting interval
	
	// Memory management
	GCInterval       time.Duration // Garbage collection interval
	MemoryLimit      int64         // Memory usage limit
}

// LoadingStats holds comprehensive loading statistics
type LoadingStats struct {
	TotalEntities    int64
	LoadingTime      time.Duration
	ParseRate        float64  // entities per second
	CreationRate     float64  // entities per second
	MemoryUsage      int64    // bytes
	GCCount          int      // garbage collections triggered
}

// DefaultStreamingConfig returns optimized default configuration for large files
func DefaultStreamingConfig() StreamingConfig {
	return StreamingConfig{
		ParseBatchSize:   10000, // Parse 10K monitors per batch to match entity creation
		ParseBufferSize:  4 * 1024 * 1024, // 4MB buffer for large files
		MaxParseMemory:   1 * 1024 * 1024 * 1024, // 1GB for parsing
		
		EntityBatchSize:  10000, // Create 10K entities per batch for maximum performance
		PreAllocateCount: 500000, // Pre-allocate for 500K entities
		
		MaxWorkers:       runtime.NumCPU() * 2, // More workers for concurrent operations
		ProgressInterval: 1 * time.Second, // More frequent progress updates
		
		GCInterval:       5 * time.Second, // More frequent GC for large loads
		MemoryLimit:      2 * 1024 * 1024 * 1024, // 2GB memory limit
	}
}

// NewStreamingLoader creates a new streaming loader
func NewStreamingLoader(filename string, world *ecs.World, config StreamingConfig) *StreamingLoader {
	return &StreamingLoader{
		filename:       filename,
		world:         world,
		config:        config,
		parseProgress:  make(chan Progress, 10),
		entityProgress: make(chan EntityProgress, 10),
		totalStartTime: time.Now(),
	}
}

// Load performs the complete streaming load operation
func (sl *StreamingLoader) Load(ctx context.Context) (*LoadingStats, error) {
	fmt.Printf("Starting streaming load of %s...\n", sl.filename)
	
	// Start memory management
	memCtx, memCancel := context.WithCancel(ctx)
	defer memCancel()
	go sl.manageMemory(memCtx)
	
	// Start progress reporting
	progressCtx, progressCancel := context.WithCancel(ctx)
	defer progressCancel()
	go sl.reportProgress(progressCtx)
	
	// Phase 1: Parse file into batches (auto-detect YAML vs JSON)
	var batchChan <-chan MonitorBatch
	var errorChan <-chan error
	
	// Check file extension to determine parser type
	if strings.HasSuffix(strings.ToLower(sl.filename), ".json") {
		fmt.Printf("DEBUG: Using JSON parser for faster loading\n")
		jsonParser, err := NewStreamingJsonParser(sl.filename, ParseConfig{
			BatchSize:    sl.config.ParseBatchSize,
			BufferSize:   sl.config.ParseBufferSize,
			MaxMemory:    sl.config.MaxParseMemory,
			ProgressChan: sl.parseProgress,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create JSON parser: %w", err)
		}
		batchChan, errorChan = jsonParser.ParseBatches(ctx, sl.parseProgress)
	} else {
		fmt.Printf("DEBUG: Using YAML parser\n")
		yamlParser, err := NewStreamingYamlParser(sl.filename, ParseConfig{
			BatchSize:    sl.config.ParseBatchSize,
			BufferSize:   sl.config.ParseBufferSize,
			MaxMemory:    sl.config.MaxParseMemory,
			ProgressChan: sl.parseProgress,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create YAML parser: %w", err)
		}
		batchChan, errorChan = yamlParser.ParseBatches(ctx, sl.parseProgress)
	}
	
	// Phase 2: Create entities from batches
	// NOTE: This must be single-threaded due to Ark ECS constraints
	entityCreator := NewStreamingEntityCreator(sl.world, EntityCreationConfig{
		BatchSize:    sl.config.EntityBatchSize,
		PreAllocate:  sl.config.PreAllocateCount,
		ProgressChan: sl.entityProgress,
	})
	
	// Process batches sequentially (required for Ark ECS)
	err := entityCreator.ProcessBatches(ctx, batchChan, sl.entityProgress)
	if err != nil {
		return nil, fmt.Errorf("failed to create entities: %w", err)
	}
	
	// Check for parsing errors
	select {
	case parseErr := <-errorChan:
		if parseErr != nil {
			return nil, fmt.Errorf("parsing error: %w", parseErr)
		}
	default:
	}
	
	// Finalize statistics (without parser dependency)
	sl.finalizeStats(entityCreator)
	
	fmt.Printf("Streaming load completed: %d entities in %v (%.0f entities/sec)\n",
		sl.loadingStats.TotalEntities,
		sl.loadingStats.LoadingTime,
		sl.loadingStats.CreationRate)
	
	return &sl.loadingStats, nil
}

// manageMemory handles memory management during loading
func (sl *StreamingLoader) manageMemory(ctx context.Context) {
	ticker := time.NewTicker(sl.config.GCInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check memory usage
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			
			if int64(m.Alloc) > sl.config.MemoryLimit {
				fmt.Printf("Memory usage high (%d MB), forcing GC\n", m.Alloc/1024/1024)
				runtime.GC()
				sl.loadingStats.GCCount++
			}
		}
	}
}

// reportProgress handles progress reporting
func (sl *StreamingLoader) reportProgress(ctx context.Context) {
	ticker := time.NewTicker(sl.config.ProgressInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
			
		case <-ticker.C:
			sl.printProgress()
			
		case progress := <-sl.parseProgress:
			sl.handleParseProgress(progress)
			
		case progress := <-sl.entityProgress:
			sl.handleEntityProgress(progress)
		}
	}
}

// handleParseProgress processes parse progress updates
func (sl *StreamingLoader) handleParseProgress(progress Progress) {
	sl.loadingStats.ParseRate = progress.Rate
}

// handleEntityProgress processes entity creation progress updates
func (sl *StreamingLoader) handleEntityProgress(progress EntityProgress) {
	sl.loadingStats.TotalEntities = progress.EntitiesCreated
	sl.loadingStats.CreationRate = progress.Rate
	sl.loadingStats.MemoryUsage = progress.MemoryUsage
}

// printProgress prints current progress to console
func (sl *StreamingLoader) printProgress() {
	elapsed := time.Since(sl.totalStartTime)
	
	fmt.Printf("Progress: %d entities created in %v (%.0f/sec) - Memory: %d MB\n",
		sl.loadingStats.TotalEntities,
		elapsed.Truncate(time.Second),
		sl.loadingStats.CreationRate,
		sl.loadingStats.MemoryUsage/1024/1024)
}

// finalizeStats calculates final loading statistics
func (sl *StreamingLoader) finalizeStats(creator *StreamingEntityCreator) {
	sl.loadingStats.LoadingTime = time.Since(sl.totalStartTime)
	
	// Get final stats from components
	entitiesCreated, _, creationRate := creator.GetStats()
	sl.loadingStats.TotalEntities = entitiesCreated
	sl.loadingStats.CreationRate = creationRate
	
	// Get memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	sl.loadingStats.MemoryUsage = int64(m.Alloc)
}

// LoadWithDefaults loads with default configuration
func LoadWithDefaults(filename string, world *ecs.World) (*LoadingStats, error) {
	loader := NewStreamingLoader(filename, world, DefaultStreamingConfig())
	return loader.Load(context.Background())
}