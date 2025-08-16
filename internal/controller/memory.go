package controller

import (
	"log"
	"runtime"
	"runtime/debug"
	"time"
)

// MemoryManager handles memory optimization and monitoring
type MemoryManager struct {
	maxMemory     uint64
	gcInterval    time.Duration
	lastGC        time.Time
	memoryStats   runtime.MemStats
	alertThreshold float64 // Percentage of max memory before alert
}

func NewMemoryManager(maxMemoryGB uint64, gcIntervalSeconds int) *MemoryManager {
	return &MemoryManager{
		maxMemory:      maxMemoryGB << 30, // Convert GB to bytes
		gcInterval:     time.Duration(gcIntervalSeconds) * time.Second,
		alertThreshold: 0.8, // Alert at 80% memory usage
	}
}

// MonitorMemory checks current memory usage and triggers cleanup if needed
func (m *MemoryManager) MonitorMemory() {
	runtime.ReadMemStats(&m.memoryStats)
	
	currentUsage := m.memoryStats.Alloc
	usagePercent := float64(currentUsage) / float64(m.maxMemory)
	
	if usagePercent > m.alertThreshold {
		log.Printf("HIGH MEMORY USAGE: %.2f%% (%d MB / %d MB)", 
			usagePercent*100, 
			currentUsage>>20, 
			m.maxMemory>>20)
		
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
	log.Printf("Forced GC: freed %d MB (before: %d MB, after: %d MB)", 
		freed>>20, before>>20, after>>20)
	
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
	log.Printf("Memory limit set to: %d GB", m.maxMemory>>30)
}

// LogMemoryStats provides detailed memory information
func (m *MemoryManager) LogMemoryStats() {
	stats := m.GetMemoryStats()
	
	log.Printf("Memory Stats:")
	log.Printf("  Alloc: %d MB", stats.Alloc>>20)
	log.Printf("  TotalAlloc: %d MB", stats.TotalAlloc>>20)
	log.Printf("  Sys: %d MB", stats.Sys>>20)
	log.Printf("  NumGC: %d", stats.NumGC)
	log.Printf("  GCCPUFraction: %.4f", stats.GCCPUFraction)
}