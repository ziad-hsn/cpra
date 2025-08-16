package workerspool

import (
	"fmt"
	"sync/atomic"
	"time"
)

// PoolMetrics tracks performance metrics for worker pools
type PoolMetrics struct {
	JobsProcessed    int64
	JobsDropped      int64
	ResultsDropped   int64
	TotalLatency     int64 // in nanoseconds
	MaxLatency       int64
	ActiveWorkers    int64
	QueueDepth       int64
	LastResetTime    time.Time
}

// PerformanceMonitor provides real-time pool performance tracking
type PerformanceMonitor struct {
	pools   map[string]*Pool
	metrics map[string]*PoolMetrics
	started time.Time
}

func NewPerformanceMonitor(pools map[string]*Pool) *PerformanceMonitor {
	pm := &PerformanceMonitor{
		pools:   pools,
		metrics: make(map[string]*PoolMetrics),
		started: time.Now(),
	}
	
	// Initialize metrics for each pool
	for alias := range pools {
		pm.metrics[alias] = &PoolMetrics{
			LastResetTime: time.Now(),
		}
	}
	
	return pm
}

// RecordJobProcessed increments job counter and tracks latency
func (pm *PerformanceMonitor) RecordJobProcessed(poolAlias string, latency time.Duration) {
	if metrics, ok := pm.metrics[poolAlias]; ok {
		atomic.AddInt64(&metrics.JobsProcessed, 1)
		atomic.AddInt64(&metrics.TotalLatency, latency.Nanoseconds())
		
		// Update max latency (simple atomic approach)
		for {
			oldMax := atomic.LoadInt64(&metrics.MaxLatency)
			if latency.Nanoseconds() <= oldMax {
				break
			}
			if atomic.CompareAndSwapInt64(&metrics.MaxLatency, oldMax, latency.Nanoseconds()) {
				break
			}
		}
	}
}

// RecordJobDropped increments dropped job counter
func (pm *PerformanceMonitor) RecordJobDropped(poolAlias string) {
	if metrics, ok := pm.metrics[poolAlias]; ok {
		atomic.AddInt64(&metrics.JobsDropped, 1)
	}
}

// RecordResultDropped increments dropped result counter
func (pm *PerformanceMonitor) RecordResultDropped(poolAlias string) {
	if metrics, ok := pm.metrics[poolAlias]; ok {
		atomic.AddInt64(&metrics.ResultsDropped, 1)
	}
}

// UpdateQueueDepth records current queue depth
func (pm *PerformanceMonitor) UpdateQueueDepth(poolAlias string, depth int) {
	if metrics, ok := pm.metrics[poolAlias]; ok {
		atomic.StoreInt64(&metrics.QueueDepth, int64(depth))
	}
}

// GetStats returns current performance statistics
func (pm *PerformanceMonitor) GetStats(poolAlias string) (PoolMetrics, bool) {
	if metrics, ok := pm.metrics[poolAlias]; ok {
		return PoolMetrics{
			JobsProcessed:  atomic.LoadInt64(&metrics.JobsProcessed),
			JobsDropped:    atomic.LoadInt64(&metrics.JobsDropped),
			ResultsDropped: atomic.LoadInt64(&metrics.ResultsDropped),
			TotalLatency:   atomic.LoadInt64(&metrics.TotalLatency),
			MaxLatency:     atomic.LoadInt64(&metrics.MaxLatency),
			QueueDepth:     atomic.LoadInt64(&metrics.QueueDepth),
			LastResetTime:  metrics.LastResetTime,
		}, true
	}
	return PoolMetrics{}, false
}

// PrintStats outputs formatted performance statistics
func (pm *PerformanceMonitor) PrintStats() {
	uptime := time.Since(pm.started)
	fmt.Printf("\n=== Worker Pool Performance (Uptime: %v) ===\n", uptime)
	
	for alias, pool := range pm.pools {
		stats, ok := pm.GetStats(alias)
		if !ok {
			continue
		}
		
		// Calculate rates and averages
		var avgLatency time.Duration
		var throughput float64
		
		if stats.JobsProcessed > 0 {
			avgLatency = time.Duration(stats.TotalLatency / stats.JobsProcessed)
			throughput = float64(stats.JobsProcessed) / uptime.Seconds()
		}
		
		maxLatency := time.Duration(stats.MaxLatency)
		
		fmt.Printf("Pool: %s\n", alias)
		fmt.Printf("  Workers: %d\n", pool.workers)
		fmt.Printf("  Jobs Processed: %d (%.1f/sec)\n", stats.JobsProcessed, throughput)
		fmt.Printf("  Jobs Dropped: %d\n", stats.JobsDropped)
		fmt.Printf("  Results Dropped: %d\n", stats.ResultsDropped)
		fmt.Printf("  Current Queue Depth: %d\n", stats.QueueDepth)
		fmt.Printf("  Avg Latency: %v\n", avgLatency)
		fmt.Printf("  Max Latency: %v\n", maxLatency)
		
		// Calculate efficiency
		if stats.JobsProcessed+stats.JobsDropped > 0 {
			efficiency := float64(stats.JobsProcessed) / float64(stats.JobsProcessed+stats.JobsDropped) * 100
			fmt.Printf("  Efficiency: %.1f%%\n", efficiency)
		}
		fmt.Println()
	}
}

// ResetStats clears all metrics counters
func (pm *PerformanceMonitor) ResetStats() {
	for _, metrics := range pm.metrics {
		atomic.StoreInt64(&metrics.JobsProcessed, 0)
		atomic.StoreInt64(&metrics.JobsDropped, 0)
		atomic.StoreInt64(&metrics.ResultsDropped, 0)
		atomic.StoreInt64(&metrics.TotalLatency, 0)
		atomic.StoreInt64(&metrics.MaxLatency, 0)
		metrics.LastResetTime = time.Now()
	}
	pm.started = time.Now()
}