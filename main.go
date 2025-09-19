package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"cpra/internal/controller"
)

func main() {
	// Command line flags
	var (
		configFile = flag.String("config", "", "Configuration file path")
		yamlFile   = flag.String("yaml", "internal/loader/replicated_test.yaml", "YAML file with monitors")
		profile    = flag.Bool("profile", false, "Enable CPU profiling")
		debug      = flag.Bool("debug", false, "Enable debug logging")
	)
	flag.Parse()

	// Initialize loggers first
	controller.InitializeLoggers(*debug)

	controller.SystemLogger.Info("Starting CPRA Optimized Controller for 1M Monitors")
	controller.SystemLogger.Info("Input file: %s", *yamlFile)

	// Create optimized configuration
	config := controller.DefaultConfig()

	// Override configuration if file provided
	if *configFile != "" {
		fmt.Printf("Loading configuration from: %s\n", *configFile)
		// Configuration loading would be implemented here
	}

	// Enable profiling if requested
	if *profile {
		fmt.Println("CPU profiling enabled")
		// CPU profiling would be initialized here
	}

	// Create the new optimized controller
	oc := controller.NewOptimizedController(config)

	// Setup context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handler for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	var shutdownInitiated bool
	var shutdownMutex sync.Mutex

	go func() {
		sig := <-sigChan
		shutdownMutex.Lock()
		if !shutdownInitiated {
			shutdownInitiated = true
			fmt.Printf("\nShutdown signal received (%v)...\n", sig)
			cancel()
		}
		shutdownMutex.Unlock()
	}()

	// Load monitors if YAML file exists
	if _, err := os.Stat(*yamlFile); err == nil {
		fmt.Printf("Loading monitors from %s...\n", *yamlFile)
		start := time.Now()

		if err := oc.LoadMonitors(ctx, *yamlFile); err != nil {
			fmt.Printf("Error loading monitors: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Monitor loading completed in %v\n", time.Since(start))
	} else {
		fmt.Printf("Warning: YAML file %s not found, starting without loading monitors\n", *yamlFile)
	}

	// Start the optimized controller
	if err := oc.Start(ctx); err != nil {
		fmt.Printf("Error starting controller: %v\n", err)
		os.Exit(1)
	}

	// Wait for shutdown signal
	<-ctx.Done()
	fmt.Println("Shutting down...")

	// oc.PrintShutdownMetrics() // This method was part of the old controller and has been removed.

	// Print memory Usage
	PrintMemUsage()

	// Stop the controller
	oc.Stop()

	// Close loggers after everything is done
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