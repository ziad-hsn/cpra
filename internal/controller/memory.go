package controller

import (
	"runtime"
	"runtime/debug"
	"time"

	"github.com/dustin/go-humanize"

	"cpra/internal/logger"
)

// MemoryManager handles memory optimization and monitoring
type MemoryManager struct {
	lastGC         time.Time
	memoryStats    runtime.MemStats
	maxMemory      uint64
	gcInterval     time.Duration
	alertThreshold float64
	logger         logger.Logger
}

func NewMemoryManager(log logger.Logger, maxMemoryGB uint64, gcIntervalSeconds int) *MemoryManager {
	return &MemoryManager{
		maxMemory:      maxMemoryGB << 30, // Convert GB to bytes
		gcInterval:     time.Duration(gcIntervalSeconds) * time.Second,
		alertThreshold: 0.8, // Alert at 80% memory usage
		logger:         log,
	}
}

// MonitorMemory checks current memory usage and triggers cleanup if needed
func (m *MemoryManager) MonitorMemory() {
	runtime.ReadMemStats(&m.memoryStats)

	currentUsage := m.memoryStats.Alloc
	usagePercent := float64(currentUsage) / float64(m.maxMemory)

	if usagePercent > m.alertThreshold {
		m.logger.Warn("HIGH MEMORY USAGE",
			logger.Field{Key: "percent", Value: usagePercent * 100},
			logger.Field{Key: "used", Value: humanize.IBytes(currentUsage)},
			logger.Field{Key: "max", Value: humanize.IBytes(m.maxMemory)})

		// Force garbage collection
		m.ForceGC()
	}

	// Periodic garbage collection
	if time.Since(m.lastGC) > m.gcInterval {
		runtime.GC()
		m.lastGC = time.Now()
	}
}

// ForceGC triggers immediate garbage collection with logging
func (m *MemoryManager) ForceGC() {
	before := m.memoryStats.Alloc
	runtime.GC()
	runtime.ReadMemStats(&m.memoryStats)
	after := m.memoryStats.Alloc

	freed := before - after
	m.logger.Info("Forced GC",
		logger.Field{Key: "freed", Value: humanize.IBytes(freed)},
		logger.Field{Key: "before", Value: humanize.IBytes(before)},
		logger.Field{Key: "after", Value: humanize.IBytes(after)})

	m.lastGC = time.Now()
}

// GetMemoryStats returns current memory statistics
func (m *MemoryManager) GetMemoryStats() runtime.MemStats {
	runtime.ReadMemStats(&m.memoryStats)
	return m.memoryStats
}

// SetMemoryLimit configures runtime memory limits
func (m *MemoryManager) SetMemoryLimit() {
	debug.SetMemoryLimit(int64(m.maxMemory))
	m.logger.Info("Memory limit set", logger.Field{Key: "gb", Value: m.maxMemory >> 30})
}

// LogMemoryStats provides detailed memory information
func (m *MemoryManager) LogMemoryStats() {
	stats := m.GetMemoryStats()
	m.logger.Info("Memory Stats",
		logger.Field{Key: "alloc", Value: humanize.IBytes(stats.Alloc)},
		logger.Field{Key: "total_alloc", Value: humanize.IBytes(stats.TotalAlloc)},
		logger.Field{Key: "sys", Value: humanize.IBytes(stats.Sys)},
		logger.Field{Key: "num_gc", Value: stats.NumGC},
		logger.Field{Key: "gc_cpu_fraction", Value: stats.GCCPUFraction})
}
