// This example demonstrates defensive error handling around CPRA monitor loading
// so operators can recover from missing files and canceled contexts without crashing.
//
// Usage: go run example-error-handling.go
// Expected output: graceful fallback messages for a missing file and context cancellation, followed by a clean shutdown log.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"cpra/internal/controller"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error-handling example failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	controller.InitializeLoggers(true)
	defer controller.CloseLoggers()

	cfg := controller.DefaultConfig()
	cfg.Debug = true

	ctrl := controller.NewController(cfg)
	started := false
	defer func() {
		if started {
			ctrl.Stop()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	missingFile := "monitors/missing.yaml"
	fmt.Printf("Trying to load %s to demonstrate file-not-found handling...\n", missingFile)
	if err := ctrl.LoadMonitors(ctx, missingFile); err != nil {
		var pathErr *fs.PathError
		switch {
		case errors.As(err, &pathErr):
			fmt.Printf("Fallback: %s (attempted path %s)\n", pathErr.Err, pathErr.Path)
		default:
			return fmt.Errorf("unexpected error when checking %s: %w", missingFile, err)
		}
	}

	sampleFile := "mock-servers/test_10k.yaml"

	abortedCtx, abort := context.WithCancel(context.Background())
	abort() // simulate an upstream cancellation
	fmt.Println("Trying to load with a canceled context to demonstrate deadline handling...")
	if err := ctrl.LoadMonitors(abortedCtx, sampleFile); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println("As expected, the load aborted because the context was canceled early.")
		} else {
			return fmt.Errorf("expected context cancellation but received: %w", err)
		}
	}

	fmt.Printf("Reloading monitors from %s with a fresh context...\n", sampleFile)
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer loadCancel()
	if err := ctrl.LoadMonitors(loadCtx, sampleFile); err != nil {
		return fmt.Errorf("load monitors after recovery: %w", err)
	}

	fmt.Println("Starting controller after successful recovery path...")
	if err := ctrl.Start(); err != nil {
		return fmt.Errorf("start controller: %w", err)
	}
	started = true

	fmt.Println("Controller running long enough to prove startup succeeded...")
	select {
	case <-time.After(1 * time.Second):
	case <-ctx.Done():
		return fmt.Errorf("outer context finished unexpectedly: %w", ctx.Err())
	}

	fmt.Println("Stopping controller after demonstrating error handling flows.")
	return nil
}
