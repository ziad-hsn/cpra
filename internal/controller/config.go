package controller

import (
	"cpra/internal/config"
	"cpra/internal/constants"
	"cpra/internal/platform/loader"
	"cpra/internal/runtime/queue"
	"time"

	"go.uber.org/zap"
)

const (
	// defaultECSCapacity is the initial entity capacity for the ECS world.
	defaultECSCapacity = 1024

	// defaultTPS is the ticks per second for the ark-tools scheduler.
	defaultTPS = 10

	// defaultServiceTime is the assumed job execution time for worker sizing.
	defaultServiceTime = 20 * time.Millisecond

	// defaultSLO is the target end-to-end latency for worker sizing.
	defaultSLO = 200 * time.Millisecond

	// defaultHeadroom references the centralized constant for worker sizing safety margin.
	defaultHeadroom = constants.DefaultHeadroom

	// interventionPoolRatio references the centralized constant for intervention worker ratio.
	interventionPoolRatio = constants.InterventionPoolRatio

	// codePoolRatio references the centralized constant for code worker ratio.
	codePoolRatio = constants.CodePoolRatio

	// shutdownTimeout is the maximum wait time for a graceful shutdown.
	shutdownTimeout = 5 * time.Second

	// shrinkBudget is the time budget per incremental memory shrink pass.
	shrinkBudget = 10 * time.Millisecond
)

// keepConstantsReferenced guards against staticcheck false positives on const usage.
var (
	_ = defaultServiceTime
	_ = defaultSLO
	_ = defaultHeadroom
	_ = interventionPoolRatio
	_ = codePoolRatio
	_ = shutdownTimeout
)

// Config holds all configuration for the controller.
//
// Configuration can be set programmatically or via environment variables
// for sizing parameters (CPRA_SIZING_TAU_MS, CPRA_SIZING_SLO_MS, CPRA_SIZING_HEADROOM_PCT).
//
// Default values are optimized for large-scale deployments but can be adjusted
// based on workload characteristics and resource constraints.
type Config struct {
	Logger            *zap.SugaredLogger
	WorkerConfig      queue.WorkerPoolConfig
	PipelineConfig    loader.PipelineConfig
	EnvConfig         *config.EnvConfig // Centralized environment configuration
	QueueCapacity     uint64
	BatchSize         int
	UpdateInterval    time.Duration
	SizingServiceTime time.Duration
	SizingSLO         time.Duration
	SizingHeadroomPct float64
	Debug             bool

	// API command channel configuration
	APICommandCapacity int // Buffer size for command channel (default: 1000)
	APICommandLimit    int // Max commands processed per tick (default: 100)

	// Shard tuning
	ShardSlots       int           // Explicit shard slot count; if <=0, auto-calculated
	ShardTargetSweep time.Duration // Desired full sweep duration across all shards; used when ShardSlots <= 0
}

// DefaultConfig returns a default configuration optimized for large-scale deployments.
//
// The default configuration uses:
//   - Queue capacity of 65536 (must be power of 2)
//   - Batch size of 1000 entities per system update
//   - Default worker pool configuration
//   - Streaming loader defaults optimized for large files
//   - Centralized environment configuration
//
// These defaults can be overridden based on specific deployment requirements.
func DefaultConfig() Config {
	envCfg := config.Load()
	return Config{
		PipelineConfig: loader.DefaultPipelineConfig(),
		QueueCapacity:  8192, // Reduced from 65536 to save ~25MB memory per queue instance
		WorkerConfig:   queue.DefaultWorkerPoolConfig(),
		EnvConfig:      envCfg,
		BatchSize:      1000,
		// UpdateInterval removed - ark-tools TPS=100 controls all timing
		SizingServiceTime:  0,
		SizingSLO:          0,
		SizingHeadroomPct:  0,
		ShardSlots:         0,
		ShardTargetSweep:   10 * time.Second, // aim for ~10s sweep by default
		APICommandCapacity: 1000,             // Buffer for command channel
		APICommandLimit:    100,              // Max commands per tick
	}
}
