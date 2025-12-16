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
	"sync"
	"syscall"
	"time"

	"cpra/internal/controller"
	"cpra/internal/jobs"
)

// ShutdownTimeout is the maximum time allowed for graceful shutdown
const ShutdownTimeout = 30 * time.Second

func main() {

	// Command line flags
	var (
		configFile  = flag.String("config", "", "Configuration file path")
		yamlFile    = flag.String("yaml", "internal/loader/replicated_test.yaml", "YAML file with monitors")
		debug       = flag.Bool("debug", false, "Enable debug logging")
		pprofEnable = flag.Bool("pprof", true, "Enable pprof web server")
		pprofAddr   = flag.String("pprof.addr", "localhost:6060", "pprof listen address (host:port)")
	)
	flag.Parse()

	// Initialize loggers first
	controller.InitializeLoggers(*debug)

	controller.SystemLogger.Infof("Starting CPRA Optimized Controller for 1M Monitors")

	// Setup pprof server with graceful shutdown capability
	var pprofServer *http.Server
	if *pprofEnable {
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

		pprofServer = &http.Server{
			Addr:    *pprofAddr,
			Handler: mux,
		}

		go func() {
			controller.SystemLogger.Infof("Profiling server listening at https://%s/debug/pprof/", *pprofAddr)
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

	// Setup context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handler for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

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
