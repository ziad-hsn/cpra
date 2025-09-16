// Optimized Configuration for High-Performance Ark Migration

package controller

import (
	"time"
	"cpra/internal/loader/streaming"
)

// OptimizedConfig provides high-performance settings for ark-migration
type OptimizedConfig struct {
	// Streaming loader config
	StreamingConfig streaming.StreamingConfig

	// Queue configuration - optimized for speed and low latency
	QueueConfig QueueConfig

	// Batch processing - adaptive and efficient
	BatchConfig BatchConfig

	// System performance settings
	PerformanceConfig PerformanceConfig

	// Recovery and reliability settings
	ReliabilityConfig ReliabilityConfig
}

type QueueConfig struct {
	// Use power-of-2 sizes for efficient ring buffer operations
	PulseQueueSize        int           // 16384 (2^14) - large enough to handle bursts
	InterventionQueueSize int           // 4096 (2^12) - smaller for less frequent operations
	CodeQueueSize         int           // 4096 (2^12) - smaller for notifications
	
	// Worker pool settings
	MinWorkers            int           // Start with minimum workers
	MaxWorkers            int           // Scale up to this limit
	WorkerIdleTimeout     time.Duration // How long workers wait before scaling down
	
	// Queue behavior
	DropPolicy            string        // "drop_oldest" or "drop_newest" when full
	EnableMetrics         bool          // Track queue performance metrics
}

type BatchConfig struct {
	// Adaptive batching settings
	MinBatchSize          int           // Minimum batch size (responsive)
	MaxBatchSize          int           // Maximum batch size (efficient)
	InitialBatchSize      int           // Starting batch size
	
	// Batch timing
	MaxBatchWaitTime      time.Duration // Max time to wait for batch to fill
	BatchCollectionTime   time.Duration // Time spent collecting entities per batch
	
	// Memory management
	EnableMemoryPooling   bool          // Use memory pools for batch allocations
	MaxPooledBatchSize    int           // Don't pool batches larger than this
	
	// Load adaptation
	LoadSampleWindow      int           // Number of samples for load calculation
	HighLoadThreshold     float64       // Load level to trigger small batches
	LowLoadThreshold      float64       // Load level to trigger large batches
}

type PerformanceConfig struct {
	// System update timing
	UpdateInterval        time.Duration // How often to run ECS systems
	StatsInterval         time.Duration // How often to print statistics
	
	// Parallel processing
	EnableParallelResults bool          // Process results in parallel
	ResultChunkSize       int           // Size of chunks for parallel processing
	MaxResultWorkers      int           // Maximum parallel result processors
	
	// Memory optimization
	GCPercent             int           // Garbage collection target percentage
	EnableMemoryLimit     bool          // Set memory limit
	MemoryLimitMB         int           // Memory limit in megabytes
	
	// CPU optimization
	MaxCPUCores           int           // Maximum CPU cores to use
	EnableCPUProfiling    bool          // Enable CPU profiling
}

type ReliabilityConfig struct {
	// Timeout and recovery
	PendingTimeout        time.Duration // How long entities can stay in pending state
	RecoveryCheckInterval time.Duration // How often to check for stuck entities
	
	// Retry logic
	MaxRetries            int           // Maximum retry attempts
	RetryBackoff          time.Duration // Base backoff time for retries
	RetryBackoffMultiplier float64      // Backoff multiplier for exponential backoff
	
	// Health monitoring
	EnableHealthChecks    bool          // Enable system health monitoring
	HealthCheckInterval   time.Duration // How often to run health checks
	
	// Error handling
	MaxErrorRate          float64       // Maximum acceptable error rate
	ErrorRateWindow       time.Duration // Time window for error rate calculation
}

// OptimizedConfigForLoad returns configuration optimized for specific load characteristics
func OptimizedConfigForLoad(monitorCount int, avgInterval time.Duration) OptimizedConfig {
	// Calculate optimal settings based on expected load
	expectedJobsPerSecond := float64(monitorCount) / avgInterval.Seconds()
	
	// Queue sizes based on expected throughput (2-3 seconds of buffer)
	pulseQueueSize := nextPowerOf2(int(expectedJobsPerSecond * 3))
	if pulseQueueSize < 1024 {
		pulseQueueSize = 1024
	}
	if pulseQueueSize > 65536 {
		pulseQueueSize = 65536
	}
	
	// Worker count based on expected load and CPU cores
	minWorkers := max(4, int(expectedJobsPerSecond/1000)) // 1 worker per 1000 jobs/sec
	maxWorkers := max(minWorkers*2, 32)                   // Allow scaling up
	
	// Batch sizes based on throughput requirements
	minBatch := 10  // Small batches for responsiveness
	maxBatch := 50  // Larger batches for efficiency, but not too large
	if expectedJobsPerSecond > 10000 {
		maxBatch = 100 // Larger batches for very high throughput
	}
	
	return OptimizedConfig{
		StreamingConfig: streaming.StreamingConfig{
			BufferSize:      10000,
			BatchSize:       1000,
			EnableStreaming: true,
		},
		
		QueueConfig: QueueConfig{
			PulseQueueSize:        pulseQueueSize,
			InterventionQueueSize: pulseQueueSize / 4,
			CodeQueueSize:         pulseQueueSize / 4,
			MinWorkers:            minWorkers,
			MaxWorkers:            maxWorkers,
			WorkerIdleTimeout:     30 * time.Second,
			DropPolicy:            "drop_oldest",
			EnableMetrics:         true,
		},
		
		BatchConfig: BatchConfig{
			MinBatchSize:         minBatch,
			MaxBatchSize:         maxBatch,
			InitialBatchSize:     (minBatch + maxBatch) / 2,
			MaxBatchWaitTime:     5 * time.Millisecond,
			BatchCollectionTime:  1 * time.Millisecond,
			EnableMemoryPooling:  true,
			MaxPooledBatchSize:   200,
			LoadSampleWindow:     10,
			HighLoadThreshold:    0.8,
			LowLoadThreshold:     0.3,
		},
		
		PerformanceConfig: PerformanceConfig{
			UpdateInterval:        10 * time.Millisecond, // 10x more responsive than current
			StatsInterval:         10 * time.Second,
			EnableParallelResults: true,
			ResultChunkSize:       25,
			MaxResultWorkers:      8,
			GCPercent:             20, // More aggressive GC for lower latency
			EnableMemoryLimit:     true,
			MemoryLimitMB:         512, // Reasonable limit for monitoring system
			MaxCPUCores:           0,   // Use all available cores
			EnableCPUProfiling:    false,
		},
		
		ReliabilityConfig: ReliabilityConfig{
			PendingTimeout:         30 * time.Second, // Recover stuck entities
			RecoveryCheckInterval:  5 * time.Second,  // Check frequently
			MaxRetries:             3,
			RetryBackoff:           100 * time.Millisecond,
			RetryBackoffMultiplier: 2.0,
			EnableHealthChecks:     true,
			HealthCheckInterval:    60 * time.Second,
			MaxErrorRate:           0.05, // 5% error rate threshold
			ErrorRateWindow:        5 * time.Minute,
		},
	}
}

// DefaultOptimizedConfig returns a high-performance configuration for typical use
func DefaultOptimizedConfig() OptimizedConfig {
	return OptimizedConfigForLoad(100000, 10*time.Second) // 100K monitors, 10s interval
}

// HighThroughputConfig returns configuration optimized for maximum throughput
func HighThroughputConfig() OptimizedConfig {
	config := DefaultOptimizedConfig()
	
	// Optimize for maximum throughput
	config.BatchConfig.MinBatchSize = 50
	config.BatchConfig.MaxBatchSize = 200
	config.BatchConfig.MaxBatchWaitTime = 10 * time.Millisecond
	
	config.QueueConfig.PulseQueueSize = 65536 // Large queue
	config.QueueConfig.MaxWorkers = 64        // Many workers
	
	config.PerformanceConfig.UpdateInterval = 5 * time.Millisecond // Very responsive
	config.PerformanceConfig.MaxResultWorkers = 16                 // More parallel processing
	
	return config
}

// LowLatencyConfig returns configuration optimized for minimum latency
func LowLatencyConfig() OptimizedConfig {
	config := DefaultOptimizedConfig()
	
	// Optimize for minimum latency
	config.BatchConfig.MinBatchSize = 5
	config.BatchConfig.MaxBatchSize = 25
	config.BatchConfig.MaxBatchWaitTime = 1 * time.Millisecond
	
	config.PerformanceConfig.UpdateInterval = 1 * time.Millisecond // Ultra responsive
	config.PerformanceConfig.GCPercent = 10                        // Very aggressive GC
	
	config.ReliabilityConfig.PendingTimeout = 5 * time.Second      // Quick recovery
	config.ReliabilityConfig.RecoveryCheckInterval = 1 * time.Second
	
	return config
}

// Helper function to find next power of 2
func nextPowerOf2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n++
	return n
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

