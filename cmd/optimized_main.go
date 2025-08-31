package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cpra/internal/controller"
	"cpra/internal/controller/systems/optimized"
)

func main() {
	// Command line flags
	var (
		configFile = flag.String("config", "", "Configuration file path")
		yamlFile   = flag.String("yaml", "internal/loader/replicated_test.yaml", "YAML file with monitors")
		profile    = flag.Bool("profile", false, "Enable CPU profiling")
	)
	flag.Parse()

	// Initialize loggers first
	controller.InitializeLoggers(false)
	
	controller.SystemLogger.Info("Starting CPRA Optimized Controller for 1M Monitors")
	controller.SystemLogger.Info("Input file: %s", *yamlFile)

	// Create mock result channels for testing
	mockChannels := optimized.NewMockResultChannels()
	defer mockChannels.Close()

	// Create optimized configuration
	config := controller.DefaultOptimizedConfig()
	
	// Result channels are created internally by optimized controller

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

	// Create optimized controller
	oc := controller.NewOptimizedController(config)

	// Setup context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handler for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutdown signal received...")
		cancel()
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

	// Stop the controller
	oc.Stop()

	fmt.Println("CPRA Optimized Controller stopped")
}
