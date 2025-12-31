package main

import (
	"context"
	"errors"
	"expvar"
	"flag"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	// automaxprocs automatically sets GOMAXPROCS to match container CPU quota.
	// This is critical for containerized deployments (Kubernetes, Docker) where
	// the default GOMAXPROCS=NumCPU() can cause excessive context switching
	// when CPU limits are lower than host cores.
	_ "go.uber.org/automaxprocs"

	"cpra/internal/controller"
	"cpra/internal/jobs"
)

// ballast is a memory allocation that helps stabilize GC behavior.
// It's allocated at startup and never used, but prevents the GC from
// triggering too frequently when heap size is small. This is especially
// useful for applications with large numbers of small objects.
// See: https://blog.twitch.tv/en/2019/04/10/go-memory-ballast-how-i-learnt-to-stop-worrying-and-love-the-heap/
var ballast []byte

// ShutdownTimeout is the maximum time allowed for graceful shutdown
const ShutdownTimeout = 30 * time.Second

// configureContentionProfiling enables block and mutex profiling for contention analysis.
// Use with: curl http://localhost:6060/debug/pprof/block or /debug/pprof/mutex
//
// Block profiling shows where goroutines block on synchronization primitives.
// Mutex profiling shows mutex contention specifically.
func configureContentionProfiling(blockRate, mutexFrac int) {
	if blockRate > 0 {
		runtime.SetBlockProfileRate(blockRate)
		fmt.Printf("Profiling: Block profile rate set to %d\n", blockRate)
	}
	if mutexFrac > 0 {
		runtime.SetMutexProfileFraction(mutexFrac)
		fmt.Printf("Profiling: Mutex profile fraction set to 1/%d\n", mutexFrac)
	}
}

// configureGC sets up GC tuning for large-scale deployments.
// For 1M+ monitors, proper GC tuning can significantly reduce pause times and CPU usage.
//
// Recommended settings for different container sizes:
//   - 4GB container: GOMEMLIMIT=3200MiB, GOGC=150
//   - 8GB container: GOMEMLIMIT=6400MiB, GOGC=200
//   - 16GB container: GOMEMLIMIT=12800MiB, GOGC=200
func configureGC(memLimitMiB, gogcPercent, ballastSizeMB int) {
	// Set memory limit if specified (enables soft memory limit)
	if memLimitMiB > 0 {
		limit := int64(memLimitMiB) * 1024 * 1024
		debug.SetMemoryLimit(limit)
		fmt.Printf("GC: GOMEMLIMIT set to %d MiB\n", memLimitMiB)
	}

	// Set GOGC percentage if specified
	// Higher values = less frequent GC but more memory usage
	// Lower values = more frequent GC but lower memory usage
	if gogcPercent > 0 {
		oldGOGC := debug.SetGCPercent(gogcPercent)
		fmt.Printf("GC: GOGC changed from %d to %d\n", oldGOGC, gogcPercent)
	}

	// Allocate ballast if specified
	// Ballast helps stabilize GC behavior by keeping the heap at a minimum size
	// This reduces GC frequency when the actual heap is small
	if ballastSizeMB > 0 {
		ballast = make([]byte, ballastSizeMB*1024*1024)
		fmt.Printf("GC: Allocated %d MB ballast\n", ballastSizeMB)
	}
}

func main() {

	// Command line flags
	var (
		configFile  = flag.String("config", "", "Configuration file path")
		yamlFile    = flag.String("yaml", "internal/loader/replicated_test.yaml", "YAML file with monitors")
		debug       = flag.Bool("debug", false, "Enable debug logging")
		pprofEnable = flag.Bool("pprof", false, "Enable pprof web server (security risk if exposed)")
		pprofAddr   = flag.String("pprof.addr", "localhost:6060", "pprof listen address (host:port)")

		// GC tuning flags for large-scale deployments (1M+ monitors)
		// These can also be set via environment variables: GOMEMLIMIT, GOGC
		memLimitMiB   = flag.Int("memlimit", 0, "Memory limit in MiB (sets GOMEMLIMIT, 0=use env or default)")
		gogcPercent   = flag.Int("gogc", 0, "GC target percentage (sets GOGC, 0=use env or default 100)")
		ballastSizeMB = flag.Int("ballast", 0, "GC ballast size in MB (0=disabled, recommended: 512-1024 for large heaps)")

		// Profiling flags for performance analysis
		blockProfileRate = flag.Int("blockprofile", 0, "Block profile rate (0=disabled, 1=all, >1=sampling rate in ns)")
		mutexProfileFrac = flag.Int("mutexprofile", 0, "Mutex profile fraction (0=disabled, 1=all, >1=1/n sampling)")
	)
	flag.Parse()

	// Configure GC tuning for large-scale deployments
	configureGC(*memLimitMiB, *gogcPercent, *ballastSizeMB)

	// Configure contention profiling if requested
	configureContentionProfiling(*blockProfileRate, *mutexProfileFrac)

	// Initialize loggers first
	controller.InitializeLoggers(*debug)

	controller.SystemLogger.Infof("Starting CPRA Optimized Controller for 1M Monitors")

	// Setup pprof server with graceful shutdown capability
	var pprofServer *http.Server
	if *pprofEnable {
		// Security check: warn if pprof is bound to non-localhost (CVE-2019-11248)
		if !strings.HasPrefix(*pprofAddr, "localhost:") && !strings.HasPrefix(*pprofAddr, "127.0.0.1:") {
			controller.SystemLogger.Warnf("SECURITY WARNING: pprof bound to non-localhost address %s - exposes sensitive runtime data!", *pprofAddr)
		}

		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

		// Expose expvar metrics on the same server for unified observability
		// Access via: curl http://localhost:6060/debug/vars | jq .
		mux.Handle("/debug/vars", expvar.Handler())

		pprofServer = &http.Server{
			Addr:    *pprofAddr,
			Handler: mux,
		}

		go func() {
			controller.SystemLogger.Infof("Profiling server listening at http://%s/debug/pprof/", *pprofAddr)
			if err := pprofServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				controller.SystemLogger.Warnf("Profiling server error: %v", err)
			}
		}()
	}
	controller.SystemLogger.Infof("Input file: %s", *yamlFile)

	// Create an optimized configuration
	config := controller.DefaultConfig()
	config.Debug = *debug

	// Override configuration if file provided
	if *configFile != "" {
		fmt.Printf("Loading configuration from: %s\n", *configFile)
		// Configuration loading would be implemented here
	}

	// Create the new optimized controller
	oc, err := controller.NewController(config)
	if err != nil {
		controller.SystemLogger.Errorf("Failed to create controller: %v", err)
		os.Exit(1)
	}

	// Publish expvar metrics for pull-based telemetry
	expvar.Publish("cpra_controller", expvar.Func(func() any {
		stats := oc.Stats()
		return map[string]any{
			"pulse_queue":          stats.PulseQueue,
			"intervention_queue":   stats.InterventionQueue,
			"code_queue":           stats.CodeQueue,
			"pulse_workers":        stats.PulseWorkers,
			"intervention_workers": stats.InterventionWorkers,
			"code_workers":         stats.CodeWorkers,
			"world":                stats.World,
		}
	}))

	// Publish runtime metrics for observability (GC stats, goroutines, memory)
	// Access via: curl http://localhost:6060/debug/vars | jq '.cpra_runtime'
	expvar.Publish("cpra_runtime", expvar.Func(func() any {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		// Get the last GC pause time (most recent)
		lastPauseNs := uint64(0)
		if m.NumGC > 0 {
			lastPauseNs = m.PauseNs[(m.NumGC+255)%256]
		}

		return map[string]any{
			// Goroutine metrics
			"goroutines": runtime.NumGoroutine(),
			"gomaxprocs": runtime.GOMAXPROCS(0),
			"num_cpu":    runtime.NumCPU(),

			// Heap memory metrics (in MB for readability)
			"heap_alloc_mb":    m.Alloc / 1024 / 1024,
			"heap_sys_mb":      m.HeapSys / 1024 / 1024,
			"heap_idle_mb":     m.HeapIdle / 1024 / 1024,
			"heap_inuse_mb":    m.HeapInuse / 1024 / 1024,
			"heap_released_mb": m.HeapReleased / 1024 / 1024,
			"heap_objects":     m.HeapObjects,

			// Stack metrics
			"stack_inuse_mb": m.StackInuse / 1024 / 1024,
			"stack_sys_mb":   m.StackSys / 1024 / 1024,

			// Total system memory
			"sys_mb":         m.Sys / 1024 / 1024,
			"total_alloc_mb": m.TotalAlloc / 1024 / 1024,

			// GC metrics
			"num_gc":            m.NumGC,
			"gc_cpu_fraction":   m.GCCPUFraction,
			"gc_pause_total_ms": m.PauseTotalNs / 1_000_000,
			"gc_last_pause_us":  lastPauseNs / 1000,
			"num_forced_gc":     m.NumForcedGC,
			"next_gc_mb":        m.NextGC / 1024 / 1024,

			// Allocation rates (useful for detecting memory leaks)
			"mallocs":      m.Mallocs,
			"frees":        m.Frees,
			"live_objects": m.Mallocs - m.Frees,
		}
	}))

	// Setup context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handler for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	var shutdownInitiated sync.Once

	go func() {
		sig := <-sigChan
		shutdownInitiated.Do(func() {
			controller.SystemLogger.Infof("Shutdown signal received (%v), initiating graceful shutdown...", sig)
			cancel()
		})
	}()

	// Load monitors
	controller.SystemLogger.Infof("Loading monitors from %s...", *yamlFile)
	start := time.Now()

	if err := oc.LoadMonitors(ctx, *yamlFile); err != nil {
		controller.SystemLogger.Errorf("Error loading monitors: %v", err)
		os.Exit(1)
	}

	controller.SystemLogger.Infof("Monitor loading completed in %v", time.Since(start))

	// Start the optimized controller
	if err := oc.Start(ctx); err != nil {
		controller.SystemLogger.Errorf("Error starting controller: %v", err)
		os.Exit(1)
	}

	// Start watchdog in separate goroutine with heartbeat monitoring
	watchdogHeartbeat := make(chan struct{}, 1)
	wdConfig := controller.DefaultWatchdogConfig()
	if wdConfig.Enabled {
		wd := controller.NewWatchdog(oc, wdConfig, watchdogHeartbeat, controller.WatchdogLogger)
		go func() {
			if err := wd.Run(ctx); err != nil {
				controller.SystemLogger.Errorf("Watchdog error: %v", err)
			}
		}()

		// Monitor watchdog health from main
		go monitorWatchdogHealth(ctx, watchdogHeartbeat, wdConfig.CheckInterval*3)
	}

	// Wait for a shutdown signal
	<-ctx.Done()

	// Create a shutdown context with timeout
	shutdownStart := time.Now()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer shutdownCancel()

	controller.SystemLogger.Infof("=== GRACEFUL SHUTDOWN STARTED ===")

	// 1. Stop the controller (drains worker pools, closes queues)
	controller.SystemLogger.Infof("[1/5] Stopping controller and draining worker pools...")
	controllerDone := make(chan struct{})
	go func() {
		oc.Stop()
		close(controllerDone)
	}()

	select {
	case <-controllerDone:
		controller.SystemLogger.Infof("[1/5] Controller stopped successfully")
	case <-shutdownCtx.Done():
		controller.SystemLogger.Warnf("[1/5] Controller stop timed out after %v", ShutdownTimeout)
	}

	// 2. Shutdown the log manager to flush pending logs
	controller.SystemLogger.Infof("[2/5] Flushing log manager...")
	logManagerDone := make(chan struct{})
	go func() {
		jobs.GetLogManager().Shutdown()
		close(logManagerDone)
	}()

	select {
	case <-logManagerDone:
		controller.SystemLogger.Infof("[2/5] Log manager flushed successfully")
	case <-shutdownCtx.Done():
		controller.SystemLogger.Warnf("[2/5] Log manager flush timed out")
	}

	// 3. Stop pprof server if running
	if pprofServer != nil {
		controller.SystemLogger.Infof("[3/5] Stopping profiling server...")
		pprofCtx, pprofCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := pprofServer.Shutdown(pprofCtx); err != nil {
			controller.SystemLogger.Warnf("[3/5] Profiling server shutdown error: %v", err)
		} else {
			controller.SystemLogger.Infof("[3/5] Profiling server stopped")
		}
		pprofCancel()
	} else {
		controller.SystemLogger.Infof("[3/5] Profiling server not running, skipping")
	}

	// 4. Print final memory stats
	controller.SystemLogger.Infof("[4/5] Final memory statistics:")
	PrintMemUsage()

	// 5. Close loggers (flush any remaining buffered logs)
	controller.SystemLogger.Infof("[5/5] Closing loggers...")
	controller.SystemLogger.Infof("=== GRACEFUL SHUTDOWN COMPLETED in %v ===", time.Since(shutdownStart))

	// Close loggers after a final message
	controller.CloseLoggers()

	fmt.Println("CPRA Optimized Controller stopped")
}

// bToMb converts bytes to megabytes
func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}

// PrintMemUsage outputs the current, total, and system memory usage
func PrintMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("\nMemory usage on exit:\n")
	fmt.Printf("Alloc = %v MiB", bToMb(m.Alloc))
	fmt.Printf("\tTotalAlloc = %v MiB", bToMb(m.TotalAlloc))
	fmt.Printf("\tSys = %v MiB", bToMb(m.Sys))
	fmt.Printf("\tNumGC = %v\n", m.NumGC)
}

// monitorWatchdogHealth monitors the watchdog goroutine health via heartbeat.
// If no heartbeat is received within the timeout, logs a warning.
// Uses a reusable time.Timer to avoid the time.After allocation/leak pattern.
func monitorWatchdogHealth(ctx context.Context, heartbeat <-chan struct{}, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat:
			// Watchdog is alive - reset timer
			if !timer.Stop() {
				// Drain the channel if timer already fired
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		case <-timer.C:
			controller.SystemLogger.Warnf("Watchdog heartbeat missed for %v", timeout)
			timer.Reset(timeout)
		}
	}
}
