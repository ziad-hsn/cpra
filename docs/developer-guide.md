# CPRA Developer Guide

## Introduction

This guide helps developers navigate and contribute to the CPRA codebase. It covers project structure, development workflow, common tasks, and best practices.

## Prerequisites

- **Go 1.25+**: [Download](https://go.dev/dl/)
- **Docker** (optional): For running intervention tests
- **Make**: For build automation

## Getting Started

### Clone and Build

```bash
git clone https://github.com/ziad/cpra.git
cd cpra
go mod download
go build .
```

### Run Tests

```bash
# All tests
go test ./...

# With race detection
go test -race ./...

# Specific package
go test ./internal/queue/...

# With coverage
go test -cover ./...
```

### Run the Application

```bash
# Default configuration
./cpra --yaml mock-servers/test_10k.yaml

# With debug logging
./cpra --yaml monitors.yaml --debug

# With custom pprof port
./cpra --yaml monitors.yaml --pprof.addr localhost:8080
```

## Project Structure

```
cpra/
├── main.go                    # Entry point
├── internal/                  # Private packages
│   ├── controller/            # Core orchestration
│   │   ├── components/        # ECS components
│   │   ├── entities/          # Entity management
│   │   └── systems/           # ECS systems
│   ├── jobs/                  # Job types
│   ├── queue/                 # Queue implementations
│   ├── loader/                # Config loading
│   │   └── schema/            # YAML schemas
│   ├── logger/                # Logging
│   └── interning/             # String interning
├── cmd/                       # CLI tools
├── mock-servers/              # Test infrastructure
├── docs/                      # Documentation
└── docker/                    # Docker files
```

## Code Navigation

### Finding Where Things Happen

| Task | Location |
|------|----------|
| Application entry | `main.go` |
| Controller creation | `internal/controller/controller.go:NewController` |
| Monitor loading | `internal/loader/pipeline.go:Load` |
| Entity creation | `internal/controller/entities/mapper.go:CreateEntityFromMonitor` |
| Pulse job execution | `internal/jobs/pulse_*.go:Execute` |
| State transitions | `internal/controller/systems/batch_*_result_system.go` |
| Worker scaling | `internal/queue/dynamic_worker_pool.go:autoScale` |
| Queue operations | `internal/queue/hybrid_queue.go` |

### Key Interfaces

```go
// Job interface - all jobs implement this
type Job interface {
    Execute(ctx context.Context) Result
    Copy() Job
    // ... timing methods
}

// Queue interface - all queues implement this
type Queue interface {
    Enqueue(job jobs.Job) error
    Dequeue() (jobs.Job, error)
    // ... batch methods
}

// Logger interface - for structured logging
type Logger interface {
    Debug(msg string, fields ...Field)
    Info(msg string, fields ...Field)
    // ... other levels
}
```

## Common Development Tasks

### Adding a New Pulse Type

1. **Create the job file** (`internal/jobs/pulse_dns.go`):

```go
package jobs

import (
    "context"
    "time"
)

type PulseDNSJob struct {
    BaseNetworkJob
    Hostname string
    Resolver string
}

func (j *PulseDNSJob) Execute(ctx context.Context) Result {
    payload := map[string]interface{}{"type": "pulse", "driver": "dns"}
    
    // Acquire dial slot
    if !j.AcquireDialSlot(ctx) {
        return Result{Ent: j.Entity, Err: ErrDialLimiterTimeout, Payload: payload}
    }
    defer j.ReleaseDialSlot()
    
    // Execute DNS lookup with retries
    attempts := j.GetAttempts()
    var lastErr error
    for i := 0; i < attempts; i++ {
        if err := j.CheckContext(ctx); err != nil {
            return Result{Ent: j.Entity, Err: err, Payload: payload}
        }
        
        // DNS lookup logic here
        _, err := net.LookupHost(j.Hostname)
        if err == nil {
            return Result{Ent: j.Entity, Payload: payload}
        }
        lastErr = err
        j.SleepBetweenRetries(i, attempts)
    }
    
    return Result{Ent: j.Entity, Err: lastErr, Payload: payload}
}

func (j *PulseDNSJob) Copy() Job {
    cpy := *j
    return &cpy
}
```

2. **Add pool functions** (`internal/jobs/pool.go`):

```go
var pulseDNSPool = sync.Pool{
    New: func() interface{} { return &PulseDNSJob{} },
}

func getPulseDNSJob() *PulseDNSJob {
    return pulseDNSPool.Get().(*PulseDNSJob)
}

func putPulseDNSJob(j *PulseDNSJob) {
    *j = PulseDNSJob{}
    pulseDNSPool.Put(j)
}
```

3. **Add schema** (`internal/loader/schema/schema.go`):

```go
type PulseDNSConfig struct {
    Hostname string `yaml:"hostname"`
    Resolver string `yaml:"resolver,omitempty"`
}
```

4. **Add factory case** (`internal/jobs/factory.go`):

```go
case *schema.PulseDNSConfig:
    job := getPulseDNSJob()
    job.Entity = jobID
    job.Hostname = cfg.Hostname
    job.Resolver = cfg.Resolver
    job.Timeout = timeout
    job.Retries = cfg.Retries
    return job, nil
```

### Adding a New Intervention Type

Follow the same pattern as pulse jobs, but:
- Use `BaseJob` instead of `BaseNetworkJob` if no network I/O
- Add to `CreateInterventionJob` factory
- Consider idempotency (safe to retry)

### Adding a New Code Notification

1. Create `internal/jobs/code_teams.go` for Microsoft Teams
2. Add pool functions
3. Add to `CreateCodeJob` factory
4. Add schema for configuration

### Modifying ECS Components

When changing component structures:

1. Update `internal/controller/components/components.go`
2. Update entity creation in `internal/controller/entities/mapper.go`
3. Update affected systems in `internal/controller/systems/`
4. Run tests: `go test ./internal/controller/...`

### Modifying Queue Behavior

1. Queue interface is in `internal/queue/queue.go`
2. HybridQueue is the primary implementation
3. Worker pool scaling is in `dynamic_worker_pool.go`
4. M/M/c calculations are in `sizing.go`

## Testing

### Unit Tests

```bash
# Run all tests
go test ./...

# With verbose output
go test -v ./internal/jobs/...

# With coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Benchmark Tests

```bash
# Run benchmarks
go test -bench=. ./internal/queue/...

# With memory allocation stats
go test -bench=. -benchmem ./internal/jobs/...
```

### Integration Tests

```bash
# Start mock servers
cd mock-servers && go run .

# Run with test config
./cpra --yaml mock-servers/test_10k.yaml --debug
```

### Race Detection

```bash
go test -race ./...
```

## Profiling

### CPU Profiling

```bash
# Start CPRA with pprof
./cpra --yaml monitors.yaml --pprof

# Collect 30-second profile
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
```

### Memory Profiling

```bash
# Heap profile
go tool pprof http://localhost:6060/debug/pprof/heap

# Allocation profile
go tool pprof http://localhost:6060/debug/pprof/allocs
```

### Goroutine Analysis

```bash
# View goroutine stacks
curl http://localhost:6060/debug/pprof/goroutine?debug=2
```

## Performance Optimization

### Memory Optimization

1. **Use value types**: Components are values, not pointers
2. **Object pooling**: Use sync.Pool for frequently allocated objects
3. **String interning**: Use `interning.Intern()` for repeated strings
4. **Batch operations**: Process entities in batches

### CPU Optimization

1. **Lock-free queues**: HybridQueue uses xsync.MPMCQueue
2. **Batch processing**: Systems process entities in batches
3. **Shard scheduling**: Only process one shard per tick
4. **Dial limiting**: Prevent CPU spikes during outages

### GC Optimization

```bash
# Set memory limit (70-80% of container)
GOMEMLIMIT=3200MiB ./cpra

# Tune GC percentage
GOGC=150 ./cpra

# Enable GC tracing
GODEBUG=gctrace=1 ./cpra
```

## Code Style

### Naming Conventions

- **Packages**: lowercase, single word (`queue`, `jobs`)
- **Types**: CamelCase (`MonitorState`, `HybridQueue`)
- **Functions**: CamelCase (`NewController`, `CreatePulseJob`)
- **Constants**: CamelCase (`DefaultShardSlots`)
- **Variables**: camelCase (`currentShard`, `lastError`)

### Error Handling

```go
// Predeclare errors for hot paths
var ErrQueueFull = errors.New("queue is full")

// Wrap errors with context
return fmt.Errorf("failed to create entity: %w", err)

// Check errors immediately
if err != nil {
    return nil, err
}
```

### Documentation

```go
// FunctionName does X.
//
// FunctionName performs the following:
//   - Step 1
//   - Step 2
//
// Parameters:
//   - param1: description
//   - param2: description
//
// Returns an error if X fails.
func FunctionName(param1, param2 Type) error {
    // ...
}
```

## Debugging

### Enable Debug Logging

```bash
./cpra --yaml monitors.yaml --debug
```

### State Transition Logging

The `StateLogger` logs all state transitions when debug is enabled:

```
[STATE] Entity[123] transition: PulsePending -> InterventionNeeded
```

### Inspect ECS World

```go
// In tests or debug code
stats := controller.World().Stats()
fmt.Printf("Entities: %d\n", stats.Entities.Used)
fmt.Printf("Archetypes: %d\n", len(stats.Archetypes))
```

### Queue Metrics

```go
stats := queue.Stats()
fmt.Printf("Depth: %d/%d\n", stats.QueueDepth, stats.Capacity)
fmt.Printf("Enqueue rate: %.2f/s\n", stats.EnqueueRate)
```

## Contributing

### Pull Request Checklist

- [ ] Tests pass: `go test ./...`
- [ ] No race conditions: `go test -race ./...`
- [ ] Linting passes: `go vet ./...`
- [ ] Documentation updated
- [ ] Benchmarks not regressed

### Commit Messages

```
type(scope): short description

Longer description if needed.

Fixes #123
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`

## Troubleshooting

### High Memory Usage

1. Check heap profile: `go tool pprof http://localhost:6060/debug/pprof/heap`
2. Reduce queue capacity in config
3. Enable streaming mode for loading
4. Set `GOMEMLIMIT`

### High CPU Usage

1. Check CPU profile: `go tool pprof http://localhost:6060/debug/pprof/profile`
2. Verify dial limiter is working
3. Check for tight loops in systems
4. Reduce TPS if needed

### Goroutine Leaks

1. Check goroutine profile: `curl http://localhost:6060/debug/pprof/goroutine?debug=2`
2. Ensure context cancellation is respected
3. Check for blocked channel operations

### Queue Saturation

1. Check queue stats via expvar
2. Increase worker pool size
3. Reduce monitor intervals
4. Use DropPolicy to prevent backlog

## Resources

- [Ark ECS Documentation](https://github.com/mlange-42/ark)
- [Ants Pool Documentation](https://github.com/panjf2000/ants)
- [Zap Logger Documentation](https://pkg.go.dev/go.uber.org/zap)
- [M/M/c Queueing Theory](https://en.wikipedia.org/wiki/M/M/c_queue)

