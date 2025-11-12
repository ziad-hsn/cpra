# Quick Start Guide

CPRA is a high-performance monitoring system designed for large-scale environments, using a data-oriented design for performance and scalability.

## Prerequisites
- Go 1.25 or later
- Docker (optional, for running mock servers for testing)

**Note:** This guide takes approximately 5-10 minutes to complete, including setup time. <!-- [IMPROVED] Realistic time estimate -->

## Installation

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/ziad/cpra.git
    cd cpra
    ```

2.  **Build the application:**
    ```bash
    go build .
    ```

## First Working Example

This example shows how to initialize the controller, load a simple monitor, and run the monitoring system.

1.  **Create a `monitors.yaml` file:**

    ```yaml
    - name: "example-http-check"
      pulse:
        type: "http"
        interval: "10s"
        timeout: "5s"
        http:
          url: "http://localhost:8080/health"
    ```

2.  **Create a `main.go` file to run the controller:**

    ```go
    package main

    import (
    	"context"
    	"log"
    	"os"
    	"os/signal"
    	"syscall"
    	"time"

    	"cpra/internal/controller" // [IMPROVED] Fixed import path to match actual module
    )

    func main() {
    	// Initialize loggers
    	controller.InitializeLoggers(true)
    	defer controller.CloseLoggers()

    	// Create a new controller
    	config := controller.DefaultConfig()
    	ctrl := controller.NewController(config)

    	// Load monitors
    	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    	defer cancel()
    	if err := ctrl.LoadMonitors(ctx, "monitors.yaml"); err != nil {
    		log.Fatalf("Failed to load monitors: %v", err)
    	}

    	// Start the controller
    	if err := ctrl.Start(); err != nil {
    		log.Fatalf("Failed to start controller: %v", err)
    	}
    	defer ctrl.Stop()

    	// Wait for shutdown signal
    	shutdown := make(chan os.Signal, 1)
    	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
    	<-shutdown

    	log.Println("Shutting down...")
    }
    ```

3.  **Run the example:**

    Before running, you can start the mock server provided in the `mock-servers` directory to have a running endpoint for the monitor to check.

    ```bash
    # In a separate terminal, from the mock-servers directory
    go run main.go &

    # Run the main application
    go run main.go
    ```

    You should see output indicating that the monitor is being checked.

## Understanding Monitor Lifecycle

Each monitor goes through a lifecycle of states as it processes health checks:

![Monitor Lifecycle State Machine](../images/monitor-lifecycle.png)

**States:**
- **Idle**: Monitor is ready for scheduling
- **Scheduled**: System has identified the monitor for processing
- **Enqueued**: Job is in the queue waiting for a worker
- **Executing**: Worker is actively processing the job
- **ProcessingResult**: Job complete, result being processed
- **Failed**: Error occurred, monitor will be reset

For a complete explanation of the system architecture and all three processing pipelines (Pulse, Intervention, Code), see the [Architecture Overview](../explanation/architecture-overview.md) document.

## What's Next?

-   Learn about common tasks in the [How-To Guide](../how-to/common-tasks.md)
-   Explore the full API in the [API Reference](../reference/api-reference.md)
-   Understand the architecture in the [Architecture Overview](../explanation/architecture-overview.md)
