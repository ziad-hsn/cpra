# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

CPRA (Cloud Platform Reliability Automation) is a high-performance monitoring and alerting system designed to handle over 1 million concurrent monitors. It uses an Entity-Component-System (ECS) architecture with optimized queue implementations for extreme scalability.

## Building and Running

### Build Commands
```bash
# Standard build
make build          # Outputs to bin/cpra

# Secure build (with hardening flags)
make buildsec

# Quick development build
go build -o cpra .
```

### Running the Application
```bash
# Run with default YAML file
./cpra

# Run with custom YAML file
./cpra -yaml path/to/monitors.yaml

# Enable debug logging
./cpra -debug

# With custom config
./cpra -config path/to/config.yaml
```

### Development Tasks
```bash
# Format code
make fmt

# Run tests (with race detector and coverage)
make test

# Clean build artifacts
make clean

# Tidy dependencies
make tidy
```

### Profiling
The application runs a pprof server on `localhost:6060`. Access profiles at:
- CPU: http://localhost:6060/debug/pprof/profile?seconds=30
- Heap: http://localhost:6060/debug/pprof/heap
- Goroutines: http://localhost:6060/debug/pprof/goroutine

## Architecture

### ECS (Entity-Component-System) Design

The system uses the `github.com/mlange-42/ark` ECS framework with a **consolidated component design** to minimize archetype fragmentation and maximize performance.

**Key Principle:** Instead of creating many small components (which would create exponential archetype combinations), we use a few large components with internal maps and bitfields.

#### Core Components (internal/controller/components/components.go)

1. **MonitorState**: Single consolidated state component with:
   - Bitfield flags (StateDisabled, StatePulseNeeded, StatePulsePending, etc.)
   - Timing data (LastCheckTime, NextCheckTime)
   - Error tracking (ConsecutiveFailures, LastError)
   - Atomic operations for thread-safe state updates

2. **PulseConfig**: All pulse check configuration (HTTP, TCP, ICMP)

3. **InterventionConfig**: Intervention actions (e.g., Docker container restarts)

4. **CodeConfig**: Map of color-based alert configurations (red, yellow, green, cyan, gray) instead of separate components per color

5. **CodeStatus**: Map of color-based alert status instead of separate status components

6. **JobStorage**: All job templates (PulseJob, InterventionJob, CodeJobs map)

**Impact:** This design reduces archetypes from potentially hundreds to just a handful, dramatically improving iteration speed and memory locality.

### System Processing Pipeline

The application uses `ark-tools` for system scheduling (TPS=100 for high-frequency updates):

1. **BatchPulseScheduleSystem**: Determines when monitors need pulse checks based on intervals
2. **BatchPulseSystem**: Enqueues pulse jobs to queue
3. **BatchPulseResultSystem**: Processes pulse results, triggers interventions or code alerts
4. **BatchInterventionSystem**: Enqueues intervention jobs
5. **BatchInterventionResultSystem**: Processes intervention results, triggers code alerts
6. **BatchCodeSystem**: Enqueues code notification jobs
7. **BatchCodeResultSystem**: Processes code notification results

### Queue Architecture (internal/queue/)

CPRA uses a **pluggable queue system** with two implementations:

#### WorkivaQueue (Default for high scale)
- Lock-free MPMC queue using Workiva RingBuffer
- **Dynamically expands capacity** by linking RingBuffer segments
- Unbounded semantics via sentinel capacity (1e6 default)
- Lower overhead than bounded queues
- Ideal for high-throughput workloads

#### AdaptiveQueue
- Bounded power-of-two ring buffer
- Fixed capacity with backpressure handling
- More predictable memory footprint
- Rich statistics tracking

**Queue Factory** (internal/queue/queue_factory.go):
- `NewQueue(config QueueConfig)` creates queues based on type
- Default configuration uses Workiva for scale

**Dynamic Queue Switching:**
The controller can switch queue implementations at runtime based on entity count thresholds (see `CheckEntityCountAndSwitchQueue()` and `switchToAdaptiveQueues()` in optimized_controller.go).

### Worker Pool System (internal/queue/dynamic_worker_pool.go)

Each job type (pulse, intervention, code) has a dedicated worker pool:
- Built on `github.com/panjf2000/ants/v2` goroutine pool
- **Dynamic scaling**: adjusts worker count based on load
- **Router pattern**: distributes results back to appropriate systems via channels
- Pause/Resume support for queue switching operations

### Job System (internal/jobs/)

Jobs implement the `Job` interface with timing metadata:
- **Pulse Jobs**: PulseHTTPJob, PulseTCPJob, PulseICMPJob
- **Intervention Jobs**: InterventionDockerJob
- **Code Jobs**: CodeLogJob, CodePagerDutyJob, CodeSlackJob, CodeEmailJob, CodeWebhookJob

Each job tracks EnqueueTime and StartTime for latency metrics.

### Monitor Loading (internal/loader/)

**Streaming Loader** (internal/loader/streaming/):
- Loads monitors from YAML in streaming fashion
- Handles replication for stress testing (copies monitors with " - Copy N" suffix)
- Creates entities directly in ECS world during parsing
- Optimized for loading 1M+ monitors efficiently

**Schema** (internal/loader/schema/):
- Defines monitor configuration structure
- Supports multiple pulse types (http, tcp, icmp)
- Intervention configurations
- Color-coded alerting (red, yellow, green, cyan, gray)

## Common Patterns

### Adding a New Component
1. Define component struct in `internal/controller/components/components.go`
2. Add corresponding mapper in `internal/controller/entities/mapper.go` (EntityManager)
3. Use `ecs.NewMap1[ComponentType](world)` for single-component access
4. Consider consolidation: can this be a field in an existing component rather than a new one?

### Adding a New System
1. Create system in `internal/controller/systems/`
2. Implement the system logic with batch processing
3. Register in `NewOptimizedController()` using `arkApp.AddSystem()`
4. Systems execute in order of registration

### Working with Monitor State
```go
// Get monitor state
state := entityManager.MonitorState.Get(entity)

// Check state flags (thread-safe)
if state.IsPulseNeeded() { ... }

// Set state flags (thread-safe)
state.SetPulsePending(true)

// Update timing
state.NextCheckTime = time.Now().Add(interval)
```

### Queue Operations
```go
// Enqueue single job
err := queue.Enqueue(job)

// Enqueue batch (more efficient)
err := queue.EnqueueBatch(jobs)

// Dequeue batch
jobs, err := queue.DequeueBatch(1000)

// Get statistics
stats := queue.Stats()
```

## Configuration

### Default Configuration (internal/controller/optimized_controller.go)
```go
Config{
    StreamingConfig: streaming.DefaultStreamingConfig(),
    QueueCapacity:   65536,  // Must be power of 2
    WorkerConfig:    queue.DefaultWorkerPoolConfig(),
    BatchSize:       1000,
}
```

### Monitor YAML Structure
See `internal/loader/replicated_test.yaml` for examples:
```yaml
monitors:
  - name: "Service Health Check"
    pulse_check:
      type: http
      interval: 1m
      timeout: 10s
      max_failures: 3
      config:
        url: https://api.example.com/health
        method: GET
        retries: 2
    intervention:
      action: docker
      retries: 1
      target:
        container: my-service-container
    codes:
      red:
        dispatch: true
        notify: pagerduty
        config:
          url: pagerduty-integration-url
      yellow:
        dispatch: true
        notify: log
        config:
          file: alerts.log
```

## Performance Considerations

1. **Archetype Fragmentation**: Always prefer adding fields to existing components over creating new components
2. **Batch Operations**: Use batch enqueue/dequeue for efficiency
3. **Memory Allocation**: Jobs use `strings.Clone()` to ensure independent allocations
4. **Atomic Operations**: MonitorState flags use atomic operations for safe concurrent access
5. **Queue Selection**: Workiva queue for unbounded growth; Adaptive for bounded memory

## Future Development

See `ALERTING_PLAN.md` for planned alerting architecture improvements:
- Alert policy layer with debounce/cooldown
- Per-color queue strategies
- Enhanced observability and metrics

## Important Files

- `main.go`: Application entry point, signal handling, graceful shutdown
- `internal/controller/optimized_controller.go`: Main controller orchestrating ECS, queues, and worker pools
- `internal/controller/components/components.go`: Consolidated component definitions
- `internal/controller/entities/mapper.go`: Entity creation and management
- `internal/controller/systems/`: All ECS systems for processing pipeline
- `internal/queue/`: Queue implementations and worker pools
- `internal/jobs/`: Job definitions and execution logic
- `internal/loader/`: Monitor loading and parsing

## Testing

Currently minimal test coverage. When adding tests:
- Place in `*_test.go` files next to implementation
- Use `make test` to run with race detector
- Consider benchmarks for performance-critical code

## Dependencies

Key external dependencies:
- `github.com/mlange-42/ark`: ECS framework
- `github.com/mlange-42/ark-tools`: ECS app framework with system scheduling
- `github.com/Workiva/go-datastructures`: Lock-free RingBuffer for WorkivaQueue
- `github.com/panjf2000/ants/v2`: Goroutine pool for worker pools
- `github.com/moby/moby/client`: Docker client for interventions
- `github.com/google/uuid`: Job ID generation
- `gopkg.in/yaml.v3`: YAML parsing
