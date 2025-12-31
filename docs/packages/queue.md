# Package: queue

## Overview

Package `queue` provides thread-safe queue implementations and worker pool management for the CPRA system.

The queue package abstracts job queuing behind a common interface, allowing different queue implementations to be used based on workload characteristics. It also provides dynamic worker pool management with automatic scaling based on queue depth and latency metrics.

## Import Path

```go
import "cpra/internal/queue"
```

## Queue Implementations

### HybridQueue

Combines a lock-free ring buffer with a fallback channel-based queue. Optimized for high-throughput scenarios with configurable drop policies.

```go
type HybridQueue struct {
    ring     *xsync.MPMCQueue[jobs.Job]  // Lock-free fast path
    overflow []jobs.Job                   // Burst absorption
    cfg      HybridQueueConfig
    // ... metrics and state
}
```

**Configuration:**

```go
type HybridQueueConfig struct {
    Name             string
    RingCapacity     int         // Default: 131,072
    OverflowCapacity int         // Default: 32,768
    SoftWatermark    float64     // Default: 0.90
    HardWatermark    float64     // Default: 1.0
    DropPolicy       DropPolicy  // Reject, DropNewest, DropOldest
    Logger           *zap.Logger
}
```

### AdaptiveQueue

Lock-free circular queue with fixed capacity (power of 2). Best for predictable workloads with bounded memory requirements.

### WorkivaQueue

Capacity-expanding queue using Workiva's RingBuffer. Automatically grows to handle bursty workloads.

### BoundedQueue

Simple bounded queue for testing and low-volume scenarios.

## Queue Interface

All queue implementations implement:

```go
type Queue interface {
    Enqueue(job jobs.Job) error
    EnqueueBatch(jobs []interface{}) error
    Dequeue() (jobs.Job, error)
    DequeueBatch(maxSize int) ([]jobs.Job, error)
    Close()
    Stats() Stats
    Notify() <-chan struct{}
}
```

## Drop Policies

| Policy | Behavior |
|--------|----------|
| `DropPolicyReject` | Reject new jobs when queue is full |
| `DropPolicyDropNewest` | Drop the just-arrived job |
| `DropPolicyDropOldest` | Evict oldest job to admit new one |

## Queue Statistics

```go
type Stats struct {
    LastEnqueue   time.Time
    LastDequeue   time.Time
    AvgQueueTime  time.Duration
    Dequeued      int64
    Dropped       int64
    MaxQueueTime  time.Duration
    QueueDepth    int
    MaxJobLatency time.Duration
    AvgJobLatency time.Duration
    EnqueueRate   float64
    DequeueRate   float64
    Enqueued      int64
    Capacity      int
    SampleWindow  time.Duration
}
```

## Dynamic Worker Pool

### Overview

`DynamicWorkerPool` manages a pool of workers that execute jobs from a queue. It dynamically adjusts the number of workers based on load using M/M/c queueing theory.

```go
type DynamicWorkerPool struct {
    queue    Queue
    antsPool *ants.PoolWithFunc
    router   *ResultRouter
    config   WorkerPoolConfig
    metrics  *ScalingMetrics
    // ... state
}
```

### Configuration

```go
type WorkerPoolConfig struct {
    MinWorkers         int           // Default: 5
    MaxWorkers         int           // Default: 8192
    AdjustmentInterval time.Duration // Default: 5s
    ResultBatchSize    int           // Default: 512
    ResultBatchTimeout time.Duration // Default: 10ms
    ResultChannelDepth int           // Default: 2048
    TargetQueueLatency time.Duration // Default: 100ms

    // M/M/c scaling parameters
    ScaleUpCooldown    time.Duration // Default: 30s
    ScaleDownCooldown  time.Duration // Default: 120s
    ScaleUpThreshold   float64       // Default: 1.10 (10% above)
    ScaleDownThreshold float64       // Default: 0.80 (20% below)
    WarmupDuration     time.Duration // Default: 60s

    // Ants options
    PreAlloc         bool
    NonBlocking      bool
    MaxBlockingTasks int
    ExpiryDuration   time.Duration
}
```

### Worker Pool Statistics

```go
type WorkerPoolStats struct {
    LastScaleTime   time.Time
    MinWorkers      int
    MaxWorkers      int
    CurrentCapacity int
    RunningWorkers  int
    WaitingTasks    int
    TargetWorkers   int
    TasksSubmitted  int64
    TasksCompleted  int64
    ScalingEvents   int64
    PendingResults  int
}
```

### Methods

| Method | Description |
|--------|-------------|
| `NewDynamicWorkerPool(ctx, q, config, logger)` | Creates a new worker pool |
| `Start()` | Begins worker pool operations |
| `DrainAndStop()` | Gracefully stops with drain |
| `Tune(capacity int)` | Manually adjust capacity |
| `Stats() WorkerPoolStats` | Returns runtime statistics |
| `GetRouter() *ResultRouter` | Returns the result router |

## Result Router

Routes job results to type-specific channels for ECS system processing:

```go
type ResultRouter struct {
    PulseResultChan        chan []jobs.Result
    InterventionResultChan chan []jobs.Result
    CodeResultChan         chan []jobs.Result
}
```

## M/M/c Queueing Theory

### Core Functions

#### FindCForSLO

Finds minimum workers to meet latency SLO:

```go
func FindCForSLO(lambda, tau, wTarget, ca, cs float64, cMax int) (int, float64, error)
```

Parameters:
- `lambda`: Arrival rate (jobs/second)
- `tau`: Service time (seconds/job)
- `wTarget`: Target total latency (seconds)
- `ca`, `cs`: Variability coefficients (Allen-Cunneen)
- `cMax`: Maximum workers to try

#### MmcWait

Computes M/M/c wait times:

```go
func MmcWait(lambda, mu float64, c int, ca, cs float64) (wq, w float64, err error)
```

Returns:
- `wq`: Average queue wait time
- `w`: Total system time (wq + service)

#### GetQueueMetrics

Comprehensive M/M/c metrics:

```go
func GetQueueMetrics(lambda, mu float64, c int) (QueueMetrics, error)
```

```go
type QueueMetrics struct {
    Lambda       float64 // Arrival rate
    Mu           float64 // Service rate per worker
    Servers      int     // Number of workers
    Utilization  float64 // ρ = λ/(c*μ)
    QueueProb    float64 // Probability of waiting
    IdleProb     float64 // Probability all idle
    AvgWaitQueue float64 // Wq
    AvgWaitTotal float64 // W = Wq + 1/μ
    AvgWaitGiven float64 // Wq/Pw (tail latency)
    Stable       bool    // ρ < 1
}
```

### Utility Functions

```go
// Check if queue is stable
func IsStable(lambda, tau float64, c int) bool

// Minimum workers for stability
func MinWorkersForStability(lambda, tau float64) int

// Workers for target utilization
func WorkersForUtilization(lambda, tau, targetUtil float64) int

// Current utilization
func Utilization(lambda, tau float64, c int) float64

// Approximate workers for queue probability
func ApproxWorkersForQueueProb(lambda, mu, targetProb float64) int
```

## Usage Example

```go
// Create a hybrid queue
cfg := queue.DefaultQueueConfig()
cfg.Name = "pulse"
cfg.HybridConfig.DropPolicy = queue.DropPolicyDropNewest
q, err := queue.NewQueue(cfg)
if err != nil {
    log.Fatal(err)
}
defer q.Close()

// Create a worker pool
poolCfg := queue.DefaultWorkerPoolConfig()
poolCfg.MinWorkers = 10
poolCfg.MaxWorkers = 500
poolCfg.TargetQueueLatency = 100 * time.Millisecond

pool, err := queue.NewDynamicWorkerPool(ctx, q, poolCfg, logger)
if err != nil {
    log.Fatal(err)
}

pool.Start()
defer pool.DrainAndStop()

// Enqueue jobs
job := createJob()
if err := q.Enqueue(job); err != nil {
    log.Printf("Failed to enqueue: %v", err)
}

// Get results from router
router := pool.GetRouter()
for batch := range router.PulseResultChan {
    processBatch(batch)
}
```

## Scaling Algorithm

The auto-scaling algorithm uses:

1. **Short-window metrics** for spike detection
2. **Long-window metrics** for scale-down decisions
3. **Hysteresis thresholds** to prevent oscillation
4. **Asymmetric cooldowns**: Fast up (30s), slow down (120s)
5. **Warmup period**: No scaling during first 60s

```
if desired > current:
    if ratio >= ScaleUpThreshold && cooldown elapsed:
        scale up
else:
    if ratio <= ScaleDownThreshold && long-term utilization low && cooldown elapsed:
        scale down
```

## Dependencies

- `github.com/puzpuzpuz/xsync/v4`: Lock-free MPMC queue
- `github.com/panjf2000/ants/v2`: Goroutine pool
- `github.com/Workiva/go-datastructures`: Ring buffer
- `go.uber.org/zap`: Logging

