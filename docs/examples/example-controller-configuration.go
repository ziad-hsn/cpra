// This example demonstrates how to tune the controller using the recommended steps
// from the "How to Configure the Controller" guide.
//
// Usage: go run example-controller-configuration.go
// Expected output: printed configuration summary, controller startup, and shutdown metrics.

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"cpra/internal/controller"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "controller configuration example failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	controller.InitializeLoggers(true)
	defer controller.CloseLoggers()

	config := controller.DefaultConfig()
	config.Debug = true
	config.BatchSize = 2_000
	config.QueueCapacity = 131_072
	config.WorkerConfig.MinWorkers = 10
	config.WorkerConfig.MaxWorkers = 100
	config.WorkerConfig.TargetQueueLatency = 50 * time.Millisecond
	config.WorkerConfig.ResultBatchSize = 256
	config.SizingServiceTime = 15 * time.Millisecond
	config.SizingSLO = 120 * time.Millisecond
	config.SizingHeadroomPct = 0.25

	fmt.Printf("Controller configuration:\n  BatchSize=%d\n  QueueCapacity=%d\n  Workers=%d-%d\n  TargetQueueLatency=%s\n\n",
		config.BatchSize,
		config.QueueCapacity,
		config.WorkerConfig.MinWorkers,
		config.WorkerConfig.MaxWorkers,
		config.WorkerConfig.TargetQueueLatency,
	)

	ctrl := controller.NewController(config)
	started := false
	defer func() {
		if started {
			ctrl.Stop()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sampleFile := "mock-servers/test_10k.yaml"
	if err := ctrl.LoadMonitors(ctx, sampleFile); err != nil {
		return fmt.Errorf("load monitors from %s: %w", sampleFile, err)
	}

	if err := ctrl.Start(); err != nil {
		return fmt.Errorf("start controller: %w", err)
	}
	started = true

	fmt.Println("Controller running with tuned configuration for 3 seconds...")
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return fmt.Errorf("context finished unexpectedly: %w", ctx.Err())
	}

	fmt.Println("Printing shutdown metrics to validate worker sizing...")
	ctrl.PrintShutdownMetrics()

	fmt.Println("Controller tuning example complete.")
	return nil
}
