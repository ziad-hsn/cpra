# Package: controller

## Overview

Package `controller` provides the core ECS-based controller for managing monitors in the CPRA (Cloud Platform Reliability Automation) system.

The controller orchestrates the Entity Component System (ECS) architecture, managing monitor lifecycle, job queuing, worker pools, and system coordination. It is designed to handle large-scale deployments (1M+ monitors) efficiently through batch processing, adaptive queuing, and optimized memory management.

## Import Path

```go
import "cpra/internal/controller"
```

## Architecture

The controller uses the [ark](https://github.com/mlange-42/ark) ECS library to manage monitor entities and their components. Key architectural decisions:

- **Batch Processing**: Systems process entities in batches to maximize throughput
- **Queue Abstraction**: Multiple queue implementations (Hybrid, Adaptive, Workiva) can be used based on workload characteristics
- **Worker Pools**: Dynamic worker pools with automatic scaling for pulse, intervention, and code alert processing
- **Streaming Loader**: Efficient YAML/JSON parsing for large monitor configurations

## Key Types

### Controller

The main orchestrator that manages the ECS world and its systems:

```go
type Controller struct {
    world             *ecs.World
    app               *app.App
    mapper            *entities.EntityManager
    pulsePool         *queue.DynamicWorkerPool
    interventionPool  *queue.DynamicWorkerPool
    codePool          *queue.DynamicWorkerPool
    pulseQueue        queue.Queue
    interventionQueue queue.Queue
    codeQueue         queue.Queue
    config            Config
    // ... other fields
}
```

**Methods:**

| Method | Description |
|--------|-------------|
| `NewController(config Config) (*Controller, error)` | Creates a new controller with the specified configuration |
| `LoadMonitors(ctx context.Context, filename string) error` | Loads monitors from a YAML file |
| `Start(ctx context.Context) error` | Begins the main processing loop |
| `Stop()` | Gracefully shuts down the controller |
| `World() *ecs.World` | Returns the ECS world for testing/debugging |
| `Stats() Stats` | Returns runtime statistics |

### Config

Configuration for the controller:

```go
type Config struct {
    Logger            *zap.SugaredLogger
    WorkerConfig      queue.WorkerPoolConfig
    PipelineConfig    loader.PipelineConfig
    QueueCapacity     uint64
    BatchSize         int
    UpdateInterval    time.Duration
    SizingServiceTime time.Duration
    SizingSLO         time.Duration
    SizingHeadroomPct float64
    ShardSlots        int
    ShardTargetSweep  time.Duration
    Debug             bool
}
```

### Stats

Runtime statistics aggregation:

```go
type Stats struct {
    PulseQueue          queue.Stats
    InterventionQueue   queue.Stats
    CodeQueue           queue.Stats
    PulseWorkers        queue.WorkerPoolStats
    InterventionWorkers queue.WorkerPoolStats
    CodeWorkers         queue.WorkerPoolStats
    World               *stats.World
}
```

## Sub-packages

### controller/components

ECS component definitions for monitors:

- `MonitorState`: Consolidated monitor state with bitflags
- `PulseConfig`: Health check configuration
- `InterventionConfig`: Recovery action configuration
- `CodeConfig`: Alert notification configuration
- `JobStorage`: Job references for execution
- `Shard`: Time-partition assignment
- `Disabled`: Tag component for disabled monitors

### controller/entities

Entity management:

- `EntityManager`: Creates and manages monitor entities
- Object pools for component reuse

### controller/systems

ECS systems for processing:

- `BatchPulseSystem`: Enqueues pulse jobs
- `BatchPulseResultSystem`: Processes pulse results
- `BatchInterventionSystem`: Enqueues intervention jobs
- `BatchInterventionResultSystem`: Processes intervention results
- `BatchCodeSystem`: Enqueues code alert jobs
- `BatchCodeResultSystem`: Processes code alert results
- `TerminationSystem`: Handles graceful shutdown
- `StateLogger`: Debug state transitions

## Usage Example

```go
package main

import (
    "context"
    "cpra/internal/controller"
)

func main() {
    // Create configuration
    config := controller.DefaultConfig()
    config.Debug = true
    config.BatchSize = 1000

    // Create controller
    oc, err := controller.NewController(config)
    if err != nil {
        log.Fatal(err)
    }

    ctx := context.Background()

    // Load monitors from YAML
    if err := oc.LoadMonitors(ctx, "monitors.yaml"); err != nil {
        log.Fatal(err)
    }

    // Start processing
    if err := oc.Start(ctx); err != nil {
        log.Fatal(err)
    }
    defer oc.Stop()

    // Wait for shutdown signal
    <-ctx.Done()
}
```

## GC Tuning for Large Deployments

For deployments with 1M+ monitors:

- `GOMEMLIMIT`: Set to 70-80% of container memory limit
- `GOGC`: Start with default (100), then tune based on workload

The controller already uses value-oriented programming to minimize GC pressure:
- Components are value types, not pointers
- Batch processing amortizes allocation overhead
- `World.Shrink()` reclaims memory after loading

## Thread Safety

- `World()` is safe for concurrent read access
- `Start()` and `Stop()` should be called from a single goroutine
- All queue operations are thread-safe

## Dependencies

- `github.com/mlange-42/ark/ecs`: ECS library
- `github.com/mlange-42/ark-tools/app`: ECS application framework
- `go.uber.org/zap`: Structured logging

