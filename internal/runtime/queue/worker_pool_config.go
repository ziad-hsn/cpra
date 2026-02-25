package queue

import (
	"cpra/internal/config"
	"cpra/internal/constants"
	"math"
	"runtime"
	"time"
)

// WorkerPoolConfig holds configuration for the DynamicWorkerPool.
type WorkerPoolConfig struct {
	MinWorkers         int
	MaxWorkers         int
	AdjustmentInterval time.Duration
	ResultBatchSize    int
	ResultBatchTimeout time.Duration
	ResultChannelDepth int
	TargetQueueLatency time.Duration

	// M/M/c scaling parameters
	// Asymmetric cooldowns - fast up, slow down
	ScaleUpCooldown   time.Duration // Minimum time between scale-up events (default 30s)
	ScaleDownCooldown time.Duration // Minimum time between scale-down events (default 120s)

	// Hysteresis thresholds to prevent oscillation
	ScaleUpThreshold   float64 // Ratio above current to trigger scale-up (default 1.1 = 10% above)
	ScaleDownThreshold float64 // Ratio below current to trigger scale-down (default 0.8 = 20% below)

	// Warm-up period during which no scaling occurs
	WarmupDuration time.Duration // Default 5s - allows system to stabilize after startup

	// Initial capacity for burst-ready startup (scales down after warmup if idle)
	InitialWorkers int // Default 50% of MaxWorkers - handles burst at startup

	// Ants-specific options
	PreAlloc         bool
	NonBlocking      bool
	MaxBlockingTasks int
	ExpiryDuration   time.Duration
}

// capComputation captures intermediate cap values for logging/inspection.
type capComputation struct {
	CPUCap            int
	MemCap            int
	FinalCap          int
	HeadroomPct       float64
	GoroutineBudget   int
	AbsoluteCap       int
	PerCoreMultiplier int
}

// ComputeDynamicMaxWorkersFromConfig computes max workers from centralized config.
func ComputeDynamicMaxWorkersFromConfig(minWorkers int, envCfg *config.EnvConfig) (int, capComputation) {
	perCoreMult := envCfg.WorkerPerCore
	headroom := envCfg.WorkerHeadroom
	goroutineBudget := envCfg.WorkerMemBudget
	absoluteCap := envCfg.MaxWorkers

	cpuCap := runtime.GOMAXPROCS(0) * perCoreMult
	if cpuCap < minWorkers {
		cpuCap = minWorkers
	}

	usableMem := detectUsableMemory()
	memCap := 0
	if usableMem > 0 && goroutineBudget > 0 {
		memCap = int(float64(usableMem)*headroom) / goroutineBudget
		if memCap < minWorkers {
			memCap = minWorkers
		}
	}

	// Start with CPU-based capacity as the baseline
	finalCap := cpuCap

	// Apply memory constraint if available
	if memCap > 0 {
		finalCap = min(finalCap, memCap)
	}

	// Apply absolute cap (MaxWorkers) as a hard safety ceiling
	// This ensures we respect the platform limit (e.g. 25k) or user-defined max
	if absoluteCap > 0 {
		finalCap = min(finalCap, absoluteCap)
	}
	finalCap = max(finalCap, minWorkers)

	return finalCap, capComputation{
		CPUCap:            cpuCap,
		MemCap:            memCap,
		FinalCap:          finalCap,
		HeadroomPct:       headroom,
		GoroutineBudget:   goroutineBudget,
		AbsoluteCap:       absoluteCap,
		PerCoreMultiplier: perCoreMult,
	}
}

// DefaultWorkerPoolConfig returns a default configuration for the worker pool.
// MaxWorkers is capped based on GOMAXPROCS to prevent over-scheduling in containers.
func DefaultWorkerPoolConfig() WorkerPoolConfig {
	minWorkers := 5
	envCfg := config.Load()
	maxWorkers, _ := ComputeDynamicMaxWorkersFromConfig(minWorkers, envCfg)

	// Start at 100% of max with PreAlloc to handle burst at startup
	// Workers are pre-created and ready before any jobs arrive
	initialWorkers := maxWorkers

	return WorkerPoolConfig{
		MinWorkers:         minWorkers,
		MaxWorkers:         maxWorkers,
		InitialWorkers:     initialWorkers,
		AdjustmentInterval: constants.DefaultAdjustmentInterval,
		ResultBatchSize:    constants.DefaultResultBatchSize,
		ResultBatchTimeout: constants.DefaultBatchTimeout,
		ResultChannelDepth: constants.DefaultChannelDepth,
		TargetQueueLatency: constants.DefaultTargetLatency,
		// M/M/c scaling defaults
		ScaleUpCooldown:    constants.DefaultScaleUpCooldown,    // React quickly to increased load
		ScaleDownCooldown:  constants.DefaultScaleDownCooldown,  // Be conservative about reducing capacity
		ScaleUpThreshold:   constants.DefaultScaleUpThreshold,   // Scale up when 10% more workers needed
		ScaleDownThreshold: constants.DefaultScaleDownThreshold, // Scale down when 20% fewer workers needed
		WarmupDuration:     constants.DefaultWarmupDuration,     // Short warmup; adaptive exit if queue builds
		// Ants-specific options
		PreAlloc:         true, // Pre-create all workers for burst-ready startup
		NonBlocking:      false,
		MaxBlockingTasks: 0,
		ExpiryDuration:   constants.DefaultExpiryDuration, // Faster cleanup after scale-down
	}
}

// optimalResultChannelDepth calculates the optimal result channel buffer size
// based on worker count and batch size. This balances memory usage with throughput.
//
// The formula considers:
// - At least 2x batch size to allow double-buffering
// - Scaled to worker count to handle burst capacity
// - Capped at maxWorkers to prevent memory waste at low load
func optimalResultChannelDepth(maxWorkers, minWorkers, batchSize int) int {
	if batchSize <= 0 {
		batchSize = 256
	}

	// Base: 2x batch size for double-buffering
	depth := batchSize * 2

	// Scale up based on worker count (1 result per worker in flight)
	// Use sqrt scaling to balance memory vs throughput
	workerScale := int(math.Sqrt(float64(maxWorkers)))
	if workerScale > depth {
		depth = workerScale
	}

	// Cap at maxWorkers (each worker produces at most 1 result)
	if depth > maxWorkers {
		depth = maxWorkers
	}

	// Minimum of minWorkers * 2 to handle burst at startup
	if depth < minWorkers*2 {
		depth = minWorkers * 2
	}

	// Never smaller than batch size
	if depth < batchSize {
		depth = batchSize
	}

	return depth
}
