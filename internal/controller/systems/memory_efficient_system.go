package systems

import (
	"fmt"
	"runtime"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// MemoryEfficientSystem provides memory optimization utilities for ECS systems
type MemoryEfficientSystem struct {
	lastGC          time.Time
	world           *ecs.World
	gcInterval      time.Duration
	memoryThreshold int64
	allocsBefore    uint64
	allocsAfter     uint64
	gcCount         uint32
}

// MemoryConfig holds memory management configuration
type MemoryConfig struct {
	GCInterval      time.Duration // How often to check memory
	MemoryThreshold int64         // Memory threshold for forced GC (bytes)
	EnableProfiling bool          // Enable memory profiling
}

// MemoryStats holds memory statistics
type MemoryStats struct {
	LastGCTime   time.Time
	Alloc        uint64
	TotalAlloc   uint64
	Sys          uint64
	GCPauseTotal time.Duration
	GCCount      uint32
}

// NewMemoryEfficientSystem creates a new memory management system
func NewMemoryEfficientSystem(world *ecs.World, config MemoryConfig) *MemoryEfficientSystem {
	return &MemoryEfficientSystem{
		world:           world,
		gcInterval:      config.GCInterval,
		memoryThreshold: config.MemoryThreshold,
		lastGC:          time.Now(),
	}
}

// Update performs memory management tasks
func (mes *MemoryEfficientSystem) Update() {
	now := time.Now()

	// Check if it's time for memory management
	if now.Sub(mes.lastGC) < mes.gcInterval {
		return
	}

	// Get current memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Force GC if memory usage is high
	if int64(m.Alloc) > mes.memoryThreshold {
		mes.allocsBefore = m.Alloc
		runtime.GC()

		// Get stats after GC
		runtime.ReadMemStats(&m)
		mes.allocsAfter = m.Alloc
		mes.gcCount++

		fmt.Printf("Forced GC: Memory %d MB -> %d MB (freed %d MB)\n",
			mes.allocsBefore/1024/1024,
			m.Alloc/1024/1024,
			(mes.allocsBefore-m.Alloc)/1024/1024)
	}

	mes.lastGC = now
}

// OptimizeEntityStorage optimizes entity storage by removing unused entities
func (mes *MemoryEfficientSystem) OptimizeEntityStorage() {
	// This would implement entity defragmentation if Ark supports it
	// For now, we just track the optimization request
	fmt.Println("Entity storage optimization requested (not implemented in Ark)")
}

// GetMemoryStats returns current memory statistics
func (mes *MemoryEfficientSystem) GetMemoryStats() MemoryStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return MemoryStats{
		Alloc:        m.Alloc,
		TotalAlloc:   m.TotalAlloc,
		Sys:          m.Sys,
		GCCount:      mes.gcCount,
		LastGCTime:   mes.lastGC,
		GCPauseTotal: time.Duration(m.PauseTotalNs),
	}
}

// ForceGC forces garbage collection immediately
func (mes *MemoryEfficientSystem) ForceGC() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	mes.allocsBefore = m.Alloc

	runtime.GC()

	runtime.ReadMemStats(&m)
	mes.allocsAfter = m.Alloc
	mes.gcCount++
	mes.lastGC = time.Now()

	fmt.Printf("Manual GC: Memory %d MB -> %d MB (freed %d MB)\n",
		mes.allocsBefore/1024/1024,
		m.Alloc/1024/1024,
		(mes.allocsBefore-m.Alloc)/1024/1024)
}

// SetMemoryThreshold updates the memory threshold for automatic GC
func (mes *MemoryEfficientSystem) SetMemoryThreshold(threshold int64) {
	mes.memoryThreshold = threshold
}

// GetGCCount returns the number of forced GC runs
func (mes *MemoryEfficientSystem) GetGCCount() uint32 {
	return mes.gcCount
}
