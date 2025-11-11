// This example demonstrates bootstrapping the CPRA controller with the sample monitor set
// so you can verify the pipelines start cleanly without wiring the full CLI.
//
// Usage: go run example-basic-setup.go
// Expected output: monitor loading summary, "Controller started successfully", then shutdown logs.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"cpra/internal/controller"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "basic setup failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	controller.InitializeLoggers(true)
	defer controller.CloseLoggers()

	cfg := controller.DefaultConfig()
	cfg.Debug = true
	cfg.StreamingConfig.PreAllocateCount = 5_000
	cfg.WorkerConfig.MinWorkers = 8
	cfg.WorkerConfig.MaxWorkers = 32

	ctrl := controller.NewController(cfg)
	started := false
	defer func() {
		if started {
			ctrl.Stop()
		}
	}()

	sampleFile := "mock-servers/test_10k.yaml"
	info, err := os.Stat(sampleFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("sample monitors file %s is missing: %w", sampleFile, err)
		}
		return fmt.Errorf("unable to read metadata for %s: %w", sampleFile, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("sample monitors file %s is empty", sampleFile)
	}

	fmt.Printf("Loading %s...\n", sampleFile)
	if err := ctrl.LoadMonitors(ctx, sampleFile); err != nil {
		return fmt.Errorf("loading monitors: %w", err)
	}

	fmt.Println("Starting controller...")
	if err := ctrl.Start(); err != nil {
		return fmt.Errorf("starting controller: %w", err)
	}
	started = true

	fmt.Println("Controller is running simulated work for 2 seconds...")
	select {
	case <-time.After(2 * time.Second):
		fmt.Println("Elapsed time reached, stopping now...")
	case <-ctx.Done():
		return fmt.Errorf("context finished before sample run completed: %w", ctx.Err())
	}

	fmt.Println("Controller stopped cleanly")
	return nil
}
