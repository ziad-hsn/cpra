package runtime

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/dustin/go-humanize"
)

// Ballast is a memory allocation that helps stabilize GC behavior.
var ballast []byte

// ConfigureContentionProfiling enables block and mutex profiling for contention analysis.
func ConfigureContentionProfiling(blockRate, mutexFrac int) {
	if blockRate > 0 {
		runtime.SetBlockProfileRate(blockRate)
		fmt.Printf("Profiling: Block profile rate set to %d\n", blockRate)
	}
	if mutexFrac > 0 {
		runtime.SetMutexProfileFraction(mutexFrac)
		fmt.Printf("Profiling: Mutex profile fraction set to 1/%d\n", mutexFrac)
	}
}

// ConfigureGC sets up GC tuning for large-scale deployments.
// For 1M+ monitors, proper GC tuning can significantly reduce pause times and CPU usage.
func ConfigureGC(memLimitMiB, gogcPercent, ballastSizeMB int) {
	// Set memory limit if specified (enables soft memory limit)
	if memLimitMiB > 0 {
		limit := int64(memLimitMiB) * 1024 * 1024
		debug.SetMemoryLimit(limit)
		fmt.Printf("GC: GOMEMLIMIT set to %d MiB\n", memLimitMiB)
	}

	// Set GOGC percentage if specified
	if gogcPercent > 0 {
		oldGOGC := debug.SetGCPercent(gogcPercent)
		fmt.Printf("GC: GOGC changed from %d to %d\n", oldGOGC, gogcPercent)
	}

	// Allocate ballast if specified
	if ballastSizeMB > 0 {
		ballast = make([]byte, ballastSizeMB*1024*1024)
		fmt.Printf("GC: Allocated %d MB ballast\n", ballastSizeMB)
	}
}

// PrintMemUsage outputs the current, total, and system memory usage
func PrintMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("\nMemory usage on exit:\n")
	fmt.Printf("Alloc = %s", humanize.IBytes(m.Alloc))
	fmt.Printf("\tTotalAlloc = %s", humanize.IBytes(m.TotalAlloc))
	fmt.Printf("\tSys = %s", humanize.IBytes(m.Sys))
	fmt.Printf("\tNumGC = %v\n", m.NumGC)
}
