// Package pipeline provides a concurrent pipeline for loading and validating
// monitor configurations with high throughput.
//
// The pipeline architecture:
//   - Stage 1 (Sequential): Read YAML file and decode raw nodes
//   - Stage 2 (Parallel): Parse and validate monitors (up to 1000 workers)
//   - Stage 3 (Sequential): Batch collection with deduplication
//   - Stage 4 (Sequential): Entity creation using Ark batch API
//
// Key constraint: Ark ECS world is NOT thread-safe, so entity creation
// must be sequential.
package loader

import (
	"time"

	"cpra/internal/loader/schema"

	"gopkg.in/yaml.v3"
)

// PipelineConfig holds configuration for the concurrent loading pipeline.
type PipelineConfig struct {
	// Workers is the number of concurrent workers for parse+validate stage.
	// Recommended: runtime.NumCPU() * 2 for CPU-bound work, up to 1000.
	Workers int

	// BatchSize is the number of monitors to batch before entity creation.
	BatchSize int

	// BufferSize is the size of the file read buffer in bytes.
	BufferSize int

	// RawChannelSize is the buffer size for the raw YAML node channel.
	RawChannelSize int

	// ValidatedChannelSize is the buffer size for the validated monitor channel.
	ValidatedChannelSize int

	// BatchChannelSize is the buffer size for the batch channel.
	BatchChannelSize int

	// StrictUnknownFields rejects unknown YAML fields when true.
	StrictUnknownFields bool

	// FailFast stops processing on first validation error when true.
	// When false, invalid monitors are skipped and logged.
	FailFast bool

	// ProgressInterval is the interval for progress reporting.
	ProgressInterval time.Duration
}

// DefaultPipelineConfig returns optimized default configuration.
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{
		Workers:              1000,
		BatchSize:            10000,
		BufferSize:           4 * 1024 * 1024, // 4MB
		RawChannelSize:       10000,
		ValidatedChannelSize: 10000,
		BatchChannelSize:     100,
		StrictUnknownFields:  false,
		FailFast:             false,
		ProgressInterval:     time.Second,
	}
}

// RawMonitor holds a raw YAML node for a monitor before parsing.
type RawMonitor struct {
	Node *yaml.Node
	Line int
}

// ValidatedMonitor holds a parsed and validated monitor.
type ValidatedMonitor struct {
	Monitor schema.Monitor
	Line    int
}

// MonitorBatch holds a batch of validated monitors ready for entity creation.
type MonitorBatch struct {
	Monitors []schema.Monitor
	BatchID  int
}

// PipelineProgress represents the current state of the pipeline.
type PipelineProgress struct {
	// Stage identifies the current bottleneck stage
	Stage string

	// RawParsed is the number of raw YAML nodes read
	RawParsed int64

	// Validated is the number of monitors validated
	Validated int64

	// Skipped is the number of monitors skipped due to validation errors
	Skipped int64

	// Batched is the number of monitors batched
	Batched int64

	// Created is the number of entities created
	Created int64

	// Rate is the overall monitors/second throughput
	Rate float64

	// Elapsed is the total elapsed time
	Elapsed time.Duration
}

// PipelineStats holds final pipeline statistics.
type PipelineStats struct {
	// TotalMonitors is the total number of monitors processed
	TotalMonitors int64

	// EntitiesCreated is the number of ECS entities created
	EntitiesCreated int64

	// SkippedMonitors is the number of monitors skipped due to errors
	SkippedMonitors int64

	// DuplicateMonitors is the number of duplicate monitors skipped
	DuplicateMonitors int64

	// LoadingTime is the total time to load all monitors
	LoadingTime time.Duration

	// ParseRate is the average monitors/second during parsing
	ParseRate float64

	// CreationRate is the average entities/second during creation
	CreationRate float64

	// PulseRate is the aggregated expected pulse arrival rate (jobs/sec)
	PulseRate float64
}
