---
title: Performance Tuning and SLOs
parent: How-to Guides
---

# Performance Tuning and SLOs

CPRA's performance is driven by its ability to dynamically size its worker pools to meet a Service Level Objective (SLO). This guide explains how to configure and tune these settings for optimal performance in your environment.

## 1. Understanding the SLO

The SLO in CPRA is defined as the target latency for job processing in each pipeline. By default, the target is **100ms P95**, meaning 95% of all jobs should be processed within 100 milliseconds.

The worker pool for each pipeline (Pulse, Intervention, Code) is independently sized to meet this SLO based on the principles of M/M/c queueing theory.

## 2. Configuring the Global Controller

The primary configuration for performance tuning is done in the `ControllerConfig` struct when initializing CPRA.

| Configuration Field | Description | Tuning Impact |
| :--- | :--- | :--- |
| `SizingSLO` | The target latency for P95 job completion (as `time.Duration`, e.g., `100*time.Millisecond`). | **Lowering** this value forces the system to provision **more workers** and can increase resource consumption. |
| `SizingServiceTime` | Expected average service time per job (τ). | Used for pre-sizing worker pools at startup. |
| `SizingHeadroomPct` | Safety margin above calculated minimum (default 15%). | Higher values provide more buffer for spikes. |
| `WorkerConfig.MinWorkers` | The minimum number of workers to keep active, even under no load. | Prevents cold start latency. Set to a small number (e.g., 10). |
| `WorkerConfig.MaxWorkers` | The absolute maximum number of workers the pool can scale to. | Acts as a safety limit to prevent resource exhaustion during extreme load spikes. |
| `BatchSize` | The number of entities processed by a System in a single iteration. | **Increasing** this value can improve throughput but may increase the latency of individual state updates. |

### Environment Variable Overrides

These can be set at runtime without code changes:

| Variable | Description | Example |
| :--- | :--- | :--- |
| `CPRA_SIZING_TAU_MS` | Service time in milliseconds | `20` (20ms) |
| `CPRA_SIZING_SLO_MS` | SLO target in milliseconds | `100` (100ms) |
| `CPRA_SIZING_HEADROOM_PCT` | Headroom percentage (0-100 or 0.0-1.0) | `15` or `0.15` |

## 3. Tuning the Pulse Pipeline

The Pulse pipeline is the most critical and highest-volume pipeline. Its performance is heavily influenced by the network latency of the health checks themselves.

*   **Monitor Interval (`interval`):** A shorter interval increases the job arrival rate ($\lambda$), which forces the worker pool to scale up.
*   **Monitor Timeout (`timeout`):** A longer timeout increases the average service time ($\mu$), which also forces the worker pool to scale up.

**Best Practice:** Set the `timeout` to be as short as possible. A long timeout directly increases the service time, which is the most significant factor in the M/M/c calculation.

### High-Performance HTTP Implementation

CPRA uses [fasthttp](https://github.com/valyala/fasthttp) instead of `net/http` for HTTP health checks. This provides significant performance benefits at scale:

| Optimization | Benefit |
| :--- | :--- |
| Zero-allocation request/response pooling | ~80% reduction in GC pressure |
| Per-host connection pooling | Connection reuse across checks |
| Optimized header parsing | Faster request processing |

The fasthttp client pool is managed automatically in `internal/jobs/http_pool.go`. Each unique host gets its own `fasthttp.HostClient` with connection pooling.

### TCP Connection Optimization

TCP health checks use optimized socket settings:

- **SO_REUSEADDR**: Faster socket recycling in TIME_WAIT state
- **Disabled dual-stack**: IPv4-only reduces connection attempts
- **Disabled Happy Eyeballs**: Prevents parallel dial overhead
- **Semaphore limiting**: Prevents socket exhaustion (8192 concurrent dials)

## 4. Capacity Planning

Use the sizing functions in `internal/queue/sizing.go` for capacity planning:

```go
import "cpra/internal/queue"

// Example: 100k monitors, 5s average interval, 20ms service time
lambda := 100000.0 / 5.0  // 20,000 jobs/sec
tau := 0.020              // 20ms service time
mu := 1.0 / tau           // 50 jobs/sec/worker

// Check minimum workers for stability
minStable := queue.MinWorkersForStability(lambda, tau)  // ~401 workers

// Check workers for 80% utilization target
workers80 := queue.WorkersForUtilization(lambda, tau, 0.8)  // ~500 workers

// Get full queue metrics at target worker count
metrics, _ := queue.GetQueueMetrics(lambda, mu, 500)
// metrics.Utilization = 0.80
// metrics.QueueProb = 0.23 (23% probability of waiting)
// metrics.AvgWaitQueue = 0.012s (12ms average wait)
```

**Rule of Thumb:** For production systems, use `WorkersForUtilization(λ, τ, 0.8)` as your baseline, then add the 15% headroom configured in `SizingHeadroomPct`.

## 5. Monitoring and Profiling

To validate your tuning efforts, use the built-in profiling tools:

1.  **Enable Profiling:** Start CPRA with the `--pprof` flag.
2.  **Access Metrics:** The profiling server will be available at `http://localhost:6060/debug/pprof/`.
3.  **Analyze Worker Pool Metrics:** Pay close attention to the `queue_size` and `worker_count` metrics exposed by the controller. If the `queue_size` is consistently high, it indicates that the worker pool is undersized for the current load and SLO target.

**Tips:**
- If you observe high CPU utilization but low throughput, consider increasing the `BatchSize` to reduce system overhead.
- If you observe high latency, consider lowering the `SizingSLO` (if resources allow) or increasing the `MaxWorkers`.
- Use `queue.GetQueueMetrics()` to understand your system's theoretical performance at different worker counts.
- A utilization (ρ) above 90% will cause queue times to grow rapidly — stay below 80% for stable operation.

## 6. Scaling to 1M+ Monitors

CPRA is optimized for large-scale deployments. Key optimizations for 1M+ monitors:

### Memory Optimizations

| Optimization | Description |
| :--- | :--- |
| Job struct pooling | All job types use `sync.Pool` to reduce allocations |
| Pre-allocated payloads | Common payloads are shared, not allocated per-job |
| String interning | Job types and drivers use interned strings |
| No UUID generation | Removed crypto/rand overhead (~5% CPU savings) |

### Concurrency Limits

Default limits are tuned for 1M scale:

| Resource | Limit | Configurable |
| :--- | :--- | :--- |
| HTTP connections per host | 512 | Via `fasthttp.HostClient.MaxConns` |
| Concurrent TCP dials | 8192 | `jobs.SetTCPConcurrencyLimit()` |
| Concurrent ICMP pings | 4096 | Compile-time constant |

### Expected Resource Usage (1M monitors, 5s interval)

| Metric | Expected Value |
| :--- | :--- |
| Jobs/sec | ~200,000 |
| CPU (12 cores) | 50-60% |
| Memory | 600-800 MB |
| Network sockets | 8,000-16,000 |

## 7. Network Outage Protection

During network outages, all monitored targets may become unreachable simultaneously. Without protection, this causes CPU to spike from 50% to 800%+ in seconds as failed connection attempts pile up.

### The Problem

When 100k+ health checks fail simultaneously:
- Each failed dial consumes CPU for syscalls
- No connection reuse (every attempt creates new socket)
- Goroutines pile up waiting for timeouts
- System becomes unresponsive

### Global Dial Limiter

CPRA protects against this with a unified dial limiter (`internal/jobs/dial_limiter.go`):

```go
// Default configuration (tunable at startup)
DialLimiterConfig{
    MaxConcurrentDials: 8192,   // Max in-flight connection attempts
    DialsPerSecond:     50000,  // Rate limit (token bucket) - 2.5x headroom for 100k monitors
    BurstSize:          5000,   // Allow bursts when check intervals align
    AcquireTimeout:     5*time.Second,
}
```

| Protection | How It Helps |
| :--- | :--- |
| Concurrency limit (semaphore) | Caps goroutines blocked in dial |
| Rate limit (token bucket) | Smooths bursts during mass failures |
| Acquire timeout | Fails fast when system is overloaded |

### Configuration

Override at startup before any health checks run:

```go
// Example: More conservative for resource-constrained systems
jobs.SetDialLimiterConfig(jobs.DialLimiterConfig{
    MaxConcurrentDials: 4096,   // Lower for constrained systems
    DialsPerSecond:     20000,  // More conservative rate
    BurstSize:          2000,
})
```

### automaxprocs for Containers

In containerized environments (Kubernetes, Docker), Go's default `GOMAXPROCS=runtime.NumCPU()` can cause excessive context switching when CPU limits are lower than host cores.

CPRA automatically sets `GOMAXPROCS` based on container CPU quota using [automaxprocs](https://github.com/uber-go/automaxprocs):

```go
import _ "go.uber.org/automaxprocs"  // Auto-configures at init
```

### Production Mode Logging

Heavy logging during outages can contribute to CPU spikes. Set production mode to reduce logging overhead:

```bash
export CPRA_ENV=production
```

This disables:
- Stack traces on warnings
- Debug-level logging
- Verbose error details

### Observability During Outages

Monitor these metrics during suspected outages:

```go
// Get dial limiter stats
stats := jobs.GetDialLimiter().Stats()
// stats.InFlightDials - current concurrent dials
// stats.MaxConcurrentDials - configured limit
// stats.TokensAvailable - rate limiter tokens
```

### Recommended Limits by Scale

| Scale | MaxConcurrentDials | DialsPerSecond | Rationale |
| :--- | :---: | :---: | :--- |
| 10k monitors | 2048 | 10000 | 2k checks/sec × 5x headroom |
| 100k monitors | 8192 | 50000 | 20k checks/sec × 2.5x headroom |
| 1M monitors | 16384 | 100000 | 200k checks/sec × 0.5x (constrained) |

**Note:** These are starting points. Profile under simulated outages (`tc netem` or unreachable targets) to tune for your environment.

## 8. Concurrency Patterns

CPRA uses advanced Go concurrency patterns to maximize throughput and minimize CPU usage.

### sync.Once for Lazy Initialization

Global resources are initialized lazily using `sync.Once` to avoid startup overhead and race conditions:

```go
// Docker client (internal/jobs/docker_pool.go)
var dockerClientOnce sync.Once

func GetDockerClient() (*client.Client, error) {
    dockerClientOnce.Do(func() {
        // Initialized exactly once, even under concurrent access
    })
}
```

**Benefits:**
- No initialization until first use
- Thread-safe without explicit locking after init
- Zero overhead on subsequent calls

### Timer-Based Batching (vs Ticker)

Result batching uses `time.AfterFunc` instead of `time.Ticker` to avoid CPU burn when idle:

```go
// Old pattern (wastes CPU checking empty batches):
ticker := time.NewTicker(10 * time.Millisecond)
for {
    select {
    case <-ticker.C:  // Fires even when batch is empty
        flush()
    }
}

// New pattern (timer only when needed):
if len(batch) == 1 {
    timer = time.AfterFunc(timeout, flush)  // Only set when items pending
}
```

**Benefits:**
- No timer overhead when batch is empty
- Immediate flush when batch is full (no waiting for tick)
- ~30% CPU reduction during idle periods

### Dynamic Buffer Sizing

Channel buffers are sized dynamically based on worker count:

```go
func optimalResultChannelDepth(maxWorkers, minWorkers, batchSize int) int {
    depth := batchSize * 2  // Double-buffering base
    workerScale := int(math.Sqrt(float64(maxWorkers)))
    // Scale with sqrt(workers) to balance memory vs throughput
}
```

**Configuration:**
- `ResultChannelDepth = 0` enables automatic sizing
- Explicit values override the calculation

### sync.Map for Read-Heavy Data

String interning uses `sync.Map` instead of `RWMutex` for 2-3x faster reads:

```go
// internal/interning/interning.go
var internedStrings sync.Map

func Intern(s string) string {
    if v, ok := internedStrings.Load(s); ok {  // Lock-free read
        return v.(string)
    }
    // ... slow path with LoadOrStore
}
```

**When to use sync.Map:**
- Write-once, read-many data (caches, interning)
- High read concurrency (>8 goroutines)
- Keys are stable after initial population

### Exponential Backoff for Backpressure

When channels are full, exponential backoff reduces retry overhead:

```go
// internal/queue/dynamic_worker_pool.go
backoff := baseBackoff
for attempt := 0; attempt < maxAttempts; attempt++ {
    select {
    case ch <- batch:
        return
    case <-time.After(backoff):
        backoff = backoff * 2  // Exponential growth
        if backoff > 500*time.Millisecond {
            backoff = 500 * time.Millisecond  // Cap
        }
    }
}
```

### Running Benchmarks

To measure concurrency performance:

```bash
# Interning benchmarks
go test -bench=. ./internal/interning/...

# Queue concurrency benchmarks
go test -bench=. ./internal/queue/... -run=^$

# Example output:
# BenchmarkInternHit-12      50000000    25.3 ns/op
# BenchmarkBatchCollector-12  5000000   312 ns/op
```

---

### **Next Steps**

*   **[Queueing Theory for Dynamic Scaling](../explanation/queueing-theory.md)**: Understand the mathematical model behind the dynamic scaling.
*   **[Monitor Configuration Schema](../reference/config-schema.md)**: Review the configuration fields that affect job arrival and service rates.
