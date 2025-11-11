package streaming

import (
	"context"
	"cpra/internal/loader/schema"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// MonitorBatch represents a batch of monitors read from a file.
type MonitorBatch struct {
	Monitors []schema.Monitor
	BatchID  int
	Offset   int64
}

// ParseConfig holds configuration for the streaming parsers.
type ParseConfig struct {
	ProgressChan        chan<- Progress
	BatchSize           int
	BufferSize          int
	MaxMemory           int64
	StrictUnknownFields bool
	JSONUseNumber       bool
}

// Progress represents parsing progress.
type Progress struct {
	EntitiesProcessed  int64
	TotalBytes         int64
	ProcessedBytes     int64
	Percentage         float64
	Rate               float64 // entities per second
	EstimatedRemaining time.Duration
}

// StreamingLoader orchestrates the streaming loading process
type StreamingLoader struct {
	totalStartTime time.Time
	world          *ecs.World
	parseProgress  chan Progress
	entityProgress chan EntityProgress
	filename       string
	config         StreamingConfig
	loadingStats   LoadingStats
}

// StreamingConfig holds all streaming configuration
type StreamingConfig struct {
	ParseBatchSize      int
	ParseBufferSize     int
	MaxParseMemory      int64
	EntityBatchSize     int
	PreAllocateCount    int
	MaxWorkers          int
	ProgressInterval    time.Duration
	GCInterval          time.Duration
	MemoryLimit         int64
	StrictUnknownFields bool
	JSONUseNumber       bool
}

// LoadingStats holds comprehensive loading statistics
type LoadingStats struct {
	TotalEntities int64
	LoadingTime   time.Duration
	ParseRate     float64
	CreationRate  float64
	MemoryUsage   int64
	GCCount       int
	PulseRate     float64
}

// DefaultStreamingConfig returns optimized default configuration for large files
func DefaultStreamingConfig() StreamingConfig {
	return StreamingConfig{
		ParseBatchSize:      10000,
		ParseBufferSize:     4 * 1024 * 1024,
		MaxParseMemory:      1 * 1024 * 1024 * 1024,
		EntityBatchSize:     10000,
		PreAllocateCount:    500000,
		MaxWorkers:          runtime.NumCPU() * 2,
		ProgressInterval:    1 * time.Second,
		GCInterval:          5 * time.Second,
		MemoryLimit:         2 * 1024 * 1024 * 1024,
		StrictUnknownFields: false,
		JSONUseNumber:       false,
	}
}

// NewStreamingLoader creates a new streaming loader
func NewStreamingLoader(filename string, world *ecs.World, config StreamingConfig) *StreamingLoader {
	return &StreamingLoader{
		filename:       filename,
		world:          world,
		config:         config,
		parseProgress:  make(chan Progress, 10),
		entityProgress: make(chan EntityProgress, 10),
		totalStartTime: time.Now(),
	}
}

// Load performs the complete streaming load operation
func (sl *StreamingLoader) Load(ctx context.Context) (*LoadingStats, error) {
	fmt.Printf("Starting streaming load of %s...\n", sl.filename)

	var batchChan <-chan MonitorBatch
	var errorChan <-chan error

	parseConfig := ParseConfig{
		BatchSize:           sl.config.ParseBatchSize,
		BufferSize:          sl.config.ParseBufferSize,
		MaxMemory:           sl.config.MaxParseMemory,
		ProgressChan:        sl.parseProgress,
		StrictUnknownFields: sl.config.StrictUnknownFields,
		JSONUseNumber:       sl.config.JSONUseNumber,
	}

	lower := strings.ToLower(sl.filename)
	trimmed := strings.TrimSuffix(lower, ".gz")
	if strings.HasSuffix(trimmed, ".json") {
		jsonParser, err := NewStreamingJsonParser(sl.filename, parseConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create JSON parser: %w", err)
		}
		batchChan, errorChan = jsonParser.ParseBatches(ctx, sl.parseProgress)
	} else {
		yamlParser, err := NewStreamingYamlParser(sl.filename, parseConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create YAML parser: %w", err)
		}
		batchChan, errorChan = yamlParser.ParseBatches(ctx, sl.parseProgress)
	}

	entityCreator := NewStreamingEntityCreator(sl.world, EntityCreationConfig{
		BatchSize:    sl.config.EntityBatchSize,
		PreAllocate:  sl.config.PreAllocateCount,
		ProgressChan: sl.entityProgress,
	})

	err := entityCreator.ProcessBatches(ctx, batchChan, sl.entityProgress)
	if err != nil {
		return nil, fmt.Errorf("failed to create entities: %w", err)
	}

	select {
	case parseErr := <-errorChan:
		if parseErr != nil {
			return nil, fmt.Errorf("parsing error: %w", parseErr)
		}
	default:
	}

	sl.finalizeStats(entityCreator)

	fmt.Printf("Streaming load completed: %d entities in %v (%.0f entities/sec)\n",
		sl.loadingStats.TotalEntities,
		sl.loadingStats.LoadingTime,
		sl.loadingStats.CreationRate)

	return &sl.loadingStats, nil
}

func (sl *StreamingLoader) finalizeStats(creator *StreamingEntityCreator) {
	sl.loadingStats.LoadingTime = time.Since(sl.totalStartTime)
	entitiesCreated, _, creationRate := creator.GetStats()
	sl.loadingStats.TotalEntities = entitiesCreated
	sl.loadingStats.CreationRate = creationRate
	sl.loadingStats.PulseRate = creator.PulseRate()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	sl.loadingStats.MemoryUsage = int64(m.Alloc)
}
