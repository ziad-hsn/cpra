# Types Reference

This document provides detailed documentation for all exported types in the CPRA monitoring system.

## Package: internal/controller

### Config

Configuration struct for the Controller.

**Fields:**
- **Debug** (bool) - Enable debug-level logging
- **StreamingConfig** (streaming.StreamingConfig) - Configuration for streaming loader
- **QueueCapacity** (uint64) - Initial queue capacity (must be power of 2)
- **WorkerConfig** (queue.WorkerPoolConfig) - Worker pool configuration
- **BatchSize** (int) - Batch size for system processing
- **UpdateInterval** (time.Duration) - Update interval (deprecated, ark-tools TPS=100 controls timing)
- **SizingServiceTime** (time.Duration) - τ (tau) - expected service time per job
- **SizingSLO** (time.Duration) - W target - end-to-end latency SLO
- **SizingHeadroomPct** (float64) - Safe headroom as fraction (e.g., 0.15 = 15%)

**Methods:**
- None (data struct)

**When to use:**
- When creating a new Controller instance
- When customizing system behavior before initialization

**Example:**
```go
config := controller.DefaultConfig()
config.Debug = true
config.QueueCapacity = 131072
config.BatchSize = 2000
config.SizingServiceTime = 20 * time.Millisecond
config.SizingSLO = 200 * time.Millisecond
config.SizingHeadroomPct = 0.15
ctrl := controller.NewController(config)
```

### Controller

Manages the ECS world and its systems using ark-tools.

**Fields:**
- All fields are unexported (internal state)

**Methods:**
- LoadMonitors(ctx context.Context, filename string) error
- Start() error
- Stop()
- GetWorld() *ecs.World
- PrintShutdownMetrics()
- CheckEntityCountAndSwitchQueue()

**Used by:**
- Main application entry point
- Integration tests

**Example:**
```go
config := controller.DefaultConfig()
ctrl := controller.NewController(config)
defer ctrl.Stop()

ctx := context.Background()
if err := ctrl.LoadMonitors(ctx, "monitors.yaml"); err != nil {
    log.Fatal(err)
}

if err := ctrl.Start(); err != nil {
    log.Fatal(err)
}
```

### Logger

Structured logger for component-specific logging with multiple log levels.

**Fields:**
- All fields are unexported

**Methods:**
- Debug(format string, args ...interface{})
- Info(format string, args ...interface{})
- Warn(format string, args ...interface{})
- Error(format string, args ...interface{})
- LogSystemPerformance(name string, duration time.Duration, count int)

**When to use:**
- When creating component-specific loggers
- For structured logging with different severity levels

**Example:**
```go
logger := controller.NewLogger("MyComponent", true)
logger.Info("Component started")
logger.Debug("Processing item %d", itemID)
logger.Error("Failed to process: %v", err)
```

### LoggerAdapter

Adapts the controller loggers to the systems interface.

**Fields:**
- logger (interface) - Logger implementation

**Methods:**
- Info(format string, args ...interface{})
- Debug(format string, args ...interface{})
- Warn(format string, args ...interface{})
- Error(format string, args ...interface{})
- LogSystemPerformance(name string, duration time.Duration, count int)
- LogComponentState(entityID uint32, component string, action string)

**When to use:**
- Internal adapter - typically not used directly by applications

### MetricsAggregator

Aggregates system performance metrics.

**Fields:**
- All fields are unexported

**Methods:**
- RecordSystemMetric(name string, count int, duration time.Duration)
- GetAggregateMetrics() AggregateMetrics

**When to use:**
- When collecting and aggregating system performance data
- For performance monitoring and analysis

**Example:**
```go
metrics := controller.NewMetricsAggregator()
metrics.RecordSystemMetric("pulse", 100, 50*time.Millisecond)
aggregate := metrics.GetAggregateMetrics()
fmt.Printf("Total operations: %d\n", aggregate.TotalOperations)
```

### MemoryManager

Monitors and controls application memory usage.

**Fields:**
- All fields are unexported

**Methods:**
- Start()
- Stop()
- GetMemoryStats() (alloc, totalAlloc, sys uint64)

**When to use:**
- When managing application memory limits
- For automatic GC triggering based on memory thresholds

**Example:**
```go
memMgr := controller.NewMemoryManager(8, 30) // 8GB max, 30s GC interval
memMgr.Start()
defer memMgr.Stop()
```

### RecoverySystem

Tracks errors and provides circuit breaker functionality.

**Fields:**
- All fields are unexported

**Methods:**
- RecordError()
- ShouldRecover() bool
- Reset()

**When to use:**
- When implementing error tracking and recovery logic
- For circuit breaker patterns

**Example:**
```go
recovery := controller.NewRecoverySystem(10, 1*time.Minute)
if err := doOperation(); err != nil {
    recovery.RecordError()
    if recovery.ShouldRecover() {
        // Trigger recovery logic
    }
}
```

### Tracer

Distributed tracer for component tracing.

**Fields:**
- All fields are unexported

**Methods:**
- StartSpan(operation string) *TraceSpan
- GetSpan(spanID string) (*TraceSpan, bool)

**When to use:**
- When implementing distributed tracing
- For performance analysis and debugging

**Example:**
```go
tracer := controller.NewTracer("PulseSystem", true)
span := tracer.StartSpan("ProcessPulse")
defer span.End()
// ... do work ...
```

### TraceSpan

Represents a single trace span.

**Fields:**
- SpanID (string) - Unique span identifier
- Operation (string) - Operation name
- StartTime (time.Time) - Span start time
- EndTime (time.Time) - Span end time (zero if not ended)
- Duration (time.Duration) - Span duration
- Component (string) - Component name

**Methods:**
- End()
- AddMetadata(key string, value interface{})

**When to use:**
- Automatically created by Tracer.StartSpan()
- For tracking operation timing

---

## Package: internal/queue

![Queue and Worker Pool Architecture](../images/queue-worker-pool.png)

*The queue package provides multiple queue implementations and a dynamic worker pool system. For detailed architecture explanation, see the [Architecture Overview](../explanation/architecture-overview.md) document.*

### Queue (Interface)

Defines the interface for a generic, thread-safe queue system.

**Methods:**
- Enqueue(job jobs.Job) error
- EnqueueBatch(jobs []interface{}) error
- Dequeue() (jobs.Job, error)
- DequeueBatch(maxSize int) ([]jobs.Job, error)
- Close()
- Stats() Stats

**Implementations:**
- AdaptiveQueue
- WorkivaQueue
- HybridQueue
- BoundedQueue

**When to use:**
- When you need a decoupled queue interface
- For dependency injection and testing

**Example:**
```go
var q queue.Queue
config := queue.DefaultQueueConfig()
q, err := queue.NewQueue(config)
if err != nil {
    log.Fatal(err)
}
defer q.Close()

err = q.Enqueue(myJob)
stats := q.Stats()
fmt.Printf("Queue depth: %d\n", stats.QueueDepth)
```

### Stats

Performance metrics for a queue.

**Fields:**
- **LastEnqueue** (time.Time) - Time of last enqueue operation
- **LastDequeue** (time.Time) - Time of last dequeue operation
- **AvgQueueTime** (time.Duration) - Average time jobs spend in queue
- **MaxQueueTime** (time.Duration) - Maximum time a job spent in queue
- **Dequeued** (int64) - Total jobs dequeued
- **Dropped** (int64) - Total jobs dropped
- **QueueDepth** (int) - Current number of jobs in queue
- **MaxJobLatency** (time.Duration) - Maximum job latency observed
- **AvgJobLatency** (time.Duration) - Average job latency
- **EnqueueRate** (float64) - Enqueue rate (jobs/sec)
- **DequeueRate** (float64) - Dequeue rate (jobs/sec)
- **Enqueued** (int64) - Total jobs enqueued
- **Capacity** (int) - Queue capacity
- **SampleWindow** (time.Duration) - Time window for rate calculations

**Methods:**
- None (data struct)

**When to use:**
- When monitoring queue performance
- For capacity planning and sizing decisions

**Example:**
```go
stats := myQueue.Stats()
fmt.Printf("Enqueue rate: %.2f jobs/sec\n", stats.EnqueueRate)
fmt.Printf("Queue depth: %d/%d (%.1f%% full)\n", 
    stats.QueueDepth, stats.Capacity, 
    100.0*float64(stats.QueueDepth)/float64(stats.Capacity))
fmt.Printf("Avg queue time: %v\n", stats.AvgQueueTime)
```

### QueueConfig

Configuration for queue creation.

**Fields:**
- **Name** (string) - Queue name for logging
- **Type** (QueueType) - Type of queue to create
- **Capacity** (int) - Queue capacity
- **HybridConfig** (HybridQueueConfig) - Configuration for hybrid queues

**Methods:**
- None (data struct)

**When to use:**
- When creating queues with specific configuration
- For customizing queue behavior

**Example:**
```go
config := queue.QueueConfig{
    Name:     "pulse",
    Type:     queue.QueueTypeHybrid,
    Capacity: 65536,
    HybridConfig: queue.HybridQueueConfig{
        RingCapacity: 65536,
        HeapCapacity: 100000,
        DropPolicy:   queue.DropPolicyDropNewest,
    },
}
q, err := queue.NewQueue(config)
```

### QueueType

Represents the type of queue to create.

**Constants:**
- **QueueTypeAdaptive** ("adaptive") - Adaptive queue that adjusts behavior
- **QueueTypeWorkiva** ("workiva") - Workiva ring buffer queue
- **QueueTypeHybrid** ("hybrid") - Hybrid ring buffer + heap queue

**When to use:**
- When specifying queue type in QueueConfig

### DynamicWorkerPool

Manages a pool of workers that execute jobs from a queue with dynamic scaling.

**Fields:**
- All fields are unexported

**Methods:**
- Start()
- DrainAndStop()
- GetRouter() *ResultRouter
- Stats() WorkerPoolStats
- Pause()
- Resume()
- ReplaceQueue(newQueue Queue) error

**When to use:**
- When you need concurrent job processing with auto-scaling
- For processing jobs from queues with result routing

**Example:**
```go
config := queue.DefaultWorkerPoolConfig()
config.MinWorkers = 5
config.MaxWorkers = 100
pool, err := queue.NewDynamicWorkerPool(myQueue, config, logger)
if err != nil {
    log.Fatal(err)
}
pool.Start()
defer pool.DrainAndStop()

router := pool.GetRouter()
go func() {
    for results := range router.PulseResultChan {
        processResults(results)
    }
}()
```

### WorkerPoolConfig

Configuration for the DynamicWorkerPool.

**Fields:**
- **MinWorkers** (int) - Minimum number of workers
- **MaxWorkers** (int) - Maximum number of workers
- **AdjustmentInterval** (time.Duration) - How often to adjust worker count
- **ResultBatchSize** (int) - Batch size for result processing
- **ResultBatchTimeout** (time.Duration) - Timeout for partial batches
- **ResultChannelDepth** (int) - Buffer size for result channels
- **TargetQueueLatency** (time.Duration) - Target queue latency for scaling
- **PreAlloc** (bool) - Pre-allocate worker goroutines
- **NonBlocking** (bool) - Use non-blocking mode
- **MaxBlockingTasks** (int) - Max tasks to block on (0 = unlimited)
- **ExpiryDuration** (time.Duration) - Worker expiry duration

**Methods:**
- None (data struct)

**When to use:**
- When creating DynamicWorkerPool instances
- For customizing worker pool behavior

**Example:**
```go
config := queue.WorkerPoolConfig{
    MinWorkers:         10,
    MaxWorkers:         1000,
    AdjustmentInterval: 5 * time.Second,
    ResultBatchSize:    512,
    ResultBatchTimeout: 10 * time.Millisecond,
    ResultChannelDepth: 2048,
    TargetQueueLatency: 100 * time.Millisecond,
    PreAlloc:           false,
    NonBlocking:        false,
    ExpiryDuration:     5 * time.Minute,
}
pool, _ := queue.NewDynamicWorkerPool(myQueue, config, logger)
```

### WorkerPoolStats

Runtime metrics for the dynamic worker pool.

**Fields:**
- **LastScaleTime** (time.Time) - Time of last scaling event
- **MinWorkers** (int) - Minimum worker limit
- **MaxWorkers** (int) - Maximum worker limit
- **CurrentCapacity** (int) - Current worker capacity
- **RunningWorkers** (int) - Currently running workers
- **WaitingTasks** (int) - Tasks waiting for workers
- **TargetWorkers** (int) - Target worker count
- **TasksSubmitted** (int64) - Total tasks submitted
- **TasksCompleted** (int64) - Total tasks completed
- **ScalingEvents** (int64) - Number of scaling events
- **PendingResults** (int) - Results waiting to be processed

**Methods:**
- None (data struct)

**When to use:**
- When monitoring worker pool performance
- For debugging worker pool behavior

**Example:**
```go
stats := pool.Stats()
utilization := 100.0 * float64(stats.RunningWorkers) / float64(stats.CurrentCapacity)
fmt.Printf("Workers: %d/%d (%.1f%% utilized)\n", 
    stats.RunningWorkers, stats.CurrentCapacity, utilization)
fmt.Printf("Tasks: %d submitted, %d completed\n", 
    stats.TasksSubmitted, stats.TasksCompleted)
```

### ResultRouter

Routes job results to type-specific channels.

**Fields:**
- **PulseResultChan** (chan []jobs.Result) - Channel for pulse results
- **InterventionResultChan** (chan []jobs.Result) - Channel for intervention results
- **CodeResultChan** (chan []jobs.Result) - Channel for code results

**Methods:**
- RouteResults(results []jobs.Result)
- Close()

**When to use:**
- Automatically used by DynamicWorkerPool
- For accessing type-specific result channels

**Example:**
```go
router := pool.GetRouter()

go func() {
    for results := range router.PulseResultChan {
        for _, result := range results {
            processPulseResult(result)
        }
    }
}()

go func() {
    for results := range router.InterventionResultChan {
        for _, result := range results {
            processInterventionResult(result)
        }
    }
}()
```

### AdaptiveQueue

Adaptive queue that adjusts its behavior based on load.

**Fields:**
- All fields are unexported

**Methods:**
- Implements Queue interface

**When to use:**
- For very large entity counts (>500K monitors)
- When load patterns are unpredictable

**Example:**
```go
q, err := queue.NewAdaptiveQueue(65536)
if err != nil {
    log.Fatal(err)
}
```

### HybridQueue

Combines ring buffer and heap with configurable drop policy.

**Fields:**
- All fields are unexported

**Methods:**
- Implements Queue interface

**When to use:**
- Default queue choice for most workloads
- When you need configurable drop policies

**Example:**
```go
config := queue.DefaultHybridQueueConfig()
config.DropPolicy = queue.DropPolicyDropNewest
config.RingCapacity = 32768
config.HeapCapacity = 100000
q, err := queue.NewHybridQueue(config)
```

### HybridQueueConfig

Configuration for HybridQueue.

**Fields:**
- **Name** (string) - Queue name for logging
- **RingCapacity** (int) - Ring buffer capacity (must be power of 2)
- **HeapCapacity** (int) - Heap capacity
- **DropPolicy** (DropPolicy) - Policy when both ring and heap are full
- **SampleWindow** (time.Duration) - Window for statistics calculation

**Methods:**
- None (data struct)

**When to use:**
- When creating HybridQueue instances

**Example:**
```go
config := queue.HybridQueueConfig{
    Name:         "myqueue",
    RingCapacity: 65536,
    HeapCapacity: 100000,
    DropPolicy:   queue.DropPolicyDropOldest,
    SampleWindow: 30 * time.Second,
}
```

### DropPolicy

Policy for dropping items when queue is full.

**Constants:**
- **DropPolicyReject** - Reject new items (return error)
- **DropPolicyDropOldest** - Drop oldest items
- **DropPolicyDropNewest** - Drop newest items

**When to use:**
- When configuring HybridQueue behavior

### BoundedQueue

Fixed-capacity queue with blocking behavior.

**Fields:**
- All fields are unexported

**Methods:**
- Implements Queue interface

**When to use:**
- When you need strict capacity limits
- For testing or simple scenarios

---

## Package: internal/controller/components

### Disabled

Zero-size tag component marking an entity as disabled.

**Fields:**
- None (zero-size struct)

**Methods:**
- None

**When to use:**
- Added to entities that should be excluded from processing
- Using a tag allows filters to exclude disabled entities efficiently at the archetype level

**Example:**
```go
// Add Disabled component to an entity
world.Add(entity, ecs.C[components.Disabled]())

// Filter excludes disabled entities
filter := ecs.NewFilter2[components.MonitorState, components.PulseConfig](world).
    Without(ecs.C[components.Disabled]())
```

### MonitorState

Consolidates all monitor state into a single component.

**Fields:**
- **LastCheckTime** (time.Time) - Time of last health check
- **LastSuccessTime** (time.Time) - Time of last successful check
- **NextCheckTime** (time.Time) - Scheduled time for next check
- **LastError** (error) - Last error encountered
- **Name** (string) - Monitor name
- **PendingCode** (string) - Pending code color
- **ConsecutiveFailures** (int) - Number of consecutive failures
- **PulseFailures** (int) - Total pulse failures
- **InterventionFailures** (int) - Total intervention failures
- **RecoveryStreak** (int) - Current recovery streak
- **VerifyRemaining** (int) - Remaining verification checks
- **Flags** (uint32) - Bitfield for state flags

**State Flag Constants:**
- StatePulseNeeded (1 << 1)
- StatePulsePending (1 << 2)
- StatePulseFirstCheck (1 << 3)
- StateInterventionNeeded (1 << 5)
- StateInterventionPending (1 << 6)
- StateCodeNeeded (1 << 7)
- StateCodePending (1 << 8)
- StateIncidentOpen (1 << 9)
- StateVerifying (1 << 10)

**Methods:**
- IsPulseNeeded() bool
- IsPulsePending() bool
- IsPulseFirstCheck() bool
- IsInterventionNeeded() bool
- IsInterventionPending() bool
- IsCodeNeeded() bool
- IsCodePending() bool
- SetPulseNeeded(needed bool)
- SetPulsePending(pending bool)
- SetPulseFirstCheck(firstCheck bool)
- SetInterventionNeeded(needed bool)
- SetInterventionPending(pending bool)
- SetCodeNeeded(needed bool)
- SetCodePending(pending bool)

**When to use:**
- Required component for all monitor entities
- Tracks complete monitor state in a single component

**Example:**
```go
state := &components.MonitorState{
    Name:         "web-server-01",
    NextCheckTime: time.Now().Add(60 * time.Second),
}
state.SetPulseNeeded(true)
world.Add(entity, ecs.C[components.MonitorState](), state)
```

### PulseConfig

Consolidates pulse configuration.

**Fields:**
- **Config** (schema.PulseConfig) - Type-specific pulse configuration
- **Type** (string) - Pulse type (http, tcp, icmp, etc.)
- **Timeout** (time.Duration) - Check timeout
- **Interval** (time.Duration) - Check interval
- **Retries** (int) - Number of retries on failure
- **UnhealthyThreshold** (int) - Failures before marking unhealthy
- **HealthyThreshold** (int) - Successes before marking healthy

**Methods:**
- Copy() *PulseConfig - Creates a deep copy

**When to use:**
- Attached to entities that require health checking
- Defines how and when pulse checks are performed

**Example:**
```go
pulseCfg := &components.PulseConfig{
    Type:               "http",
    Interval:           60 * time.Second,
    Timeout:            5 * time.Second,
    Retries:            3,
    UnhealthyThreshold: 3,
    HealthyThreshold:   2,
}
world.Add(entity, ecs.C[components.PulseConfig](), pulseCfg)
```

### InterventionConfig

Consolidates intervention configuration.

**Fields:**
- **Target** (schema.InterventionTarget) - Intervention target configuration
- **Action** (string) - Action to perform (restart, reboot, etc.)
- **MaxFailures** (int) - Maximum failures before giving up

**Methods:**
- Copy() *InterventionConfig - Creates a deep copy

**When to use:**
- Attached to entities that support automated remediation
- Defines remediation actions

**Example:**
```go
intCfg := &components.InterventionConfig{
    Action:      "restart",
    MaxFailures: 3,
}
world.Add(entity, ecs.C[components.InterventionConfig](), intCfg)
```

### CodeConfig

Consolidates all code configurations.

**Fields:**
- **Configs** (map[string]*ColorCodeConfig) - Map of color to configuration

**Methods:**
- Copy() *CodeConfig - Creates a deep copy

**When to use:**
- Attached to entities that require alerting
- Supports multiple code colors per monitor

**Example:**
```go
codeConfig := &components.CodeConfig{
    Configs: map[string]*components.ColorCodeConfig{
        "red": {
            Notify:   "pagerduty",
            Dispatch: true,
        },
        "yellow": {
            Notify:   "slack",
            Dispatch: false,
        },
    },
}
world.Add(entity, ecs.C[components.CodeConfig](), codeConfig)
```

### ColorCodeConfig

Configuration for a specific code color.

**Fields:**
- **Config** (schema.CodeNotification) - Notification configuration
- **Notify** (string) - Notification target
- **MaxFailures** (int) - Max failures before escalation
- **Dispatch** (bool) - Whether to dispatch immediately

**Methods:**
- Copy() *ColorCodeConfig - Creates a deep copy

**When to use:**
- Used within CodeConfig map

### CodeStatus

Consolidates all code status.

**Fields:**
- **Status** (map[string]*ColorCodeStatus) - Map of color to status

**Methods:**
- Copy() *CodeStatus - Creates a deep copy

**When to use:**
- Tracks status of code notifications per color

### ColorCodeStatus

Status for a specific code color.

**Fields:**
- **LastAlertTime** (time.Time) - Time of last alert
- **LastSuccessTime** (time.Time) - Time of last successful notification
- **LastError** (error) - Last error encountered
- **LastStatus** (string) - Last status ("success" or "failed")
- **ConsecutiveFailures** (int) - Consecutive notification failures

**Methods:**
- SetSuccess(t time.Time)
- SetFailure(err error)
- Copy() *ColorCodeStatus - Creates a deep copy

**When to use:**
- Used within CodeStatus map

### JobStorage

Consolidates all job storage.

**Fields:**
- **PulseJob** (jobs.Job) - Pulse job
- **InterventionJob** (jobs.Job) - Intervention job
- **CodeJobs** (map[string]jobs.Job) - Code jobs by color

**Methods:**
- Copy() *JobStorage - Creates a deep copy

**When to use:**
- Stores pre-created jobs for an entity
- Added before jobs are enqueued

### PulseResult

Result component for pulse jobs.

**Fields:**
- **Result** (jobs.Result) - Job result

**Methods:**
- None

**When to use:**
- Added by worker pool result router
- Removed by BatchPulseResultSystem after processing

### InterventionResult

Result component for intervention jobs.

**Fields:**
- **Result** (jobs.Result) - Job result

**Methods:**
- None

**When to use:**
- Added by worker pool result router
- Removed by BatchInterventionResultSystem after processing

### CodeResult

Result component for code notification jobs.

**Fields:**
- **Result** (jobs.Result) - Job result

**Methods:**
- None

**When to use:**
- Added by worker pool result router
- Removed by BatchCodeResultSystem after processing

---

## Package: internal/loader/streaming

### StreamingLoader

Orchestrates the streaming loading process.

**Fields:**
- All fields are unexported

**Methods:**
- Load(ctx context.Context) (*LoadingStats, error)

**When to use:**
- When loading monitor configurations from large files
- Supports streaming for memory-efficient loading

**Example:**
```go
config := streaming.DefaultStreamingConfig()
loader := streaming.NewStreamingLoader("monitors.yaml", world, config)
stats, err := loader.Load(context.Background())
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Loaded %d monitors\n", stats.TotalEntities)
```

### StreamingConfig

Configuration for streaming loader.

**Fields:**
- **ParseBatchSize** (int) - Batch size for parsing
- **ParseBufferSize** (int) - Buffer size for file reading
- **MaxParseMemory** (int64) - Maximum memory for parsing
- **EntityBatchSize** (int) - Batch size for entity creation
- **PreAllocateCount** (int) - Number of entities to pre-allocate
- **MaxWorkers** (int) - Maximum concurrent workers
- **ProgressInterval** (time.Duration) - Progress reporting interval
- **GCInterval** (time.Duration) - GC interval during loading
- **MemoryLimit** (int64) - Memory limit
- **StrictUnknownFields** (bool) - Error on unknown fields
- **JSONUseNumber** (bool) - Use number type for JSON numbers

**Methods:**
- None (data struct)

**When to use:**
- Configuring StreamingLoader behavior

**Example:**
```go
config := streaming.StreamingConfig{
    ParseBatchSize:   20000,
    EntityBatchSize:  10000,
    PreAllocateCount: 1000000,
    ProgressInterval: 1 * time.Second,
}
```

### LoadingStats

Comprehensive loading statistics.

**Fields:**
- **TotalEntities** (int64) - Total entities loaded
- **LoadingTime** (time.Duration) - Total loading time
- **ParseRate** (float64) - Parse rate (entities/sec)
- **CreationRate** (float64) - Creation rate (entities/sec)
- **MemoryUsage** (int64) - Memory used during loading
- **GCCount** (int) - Number of GC cycles
- **PulseRate** (float64) - Expected pulse arrival rate (jobs/sec)

**Methods:**
- None (data struct)

**When to use:**
- Returned by StreamingLoader.Load()
- For analyzing loading performance

### StreamingEntityCreator

Handles batch entity creation for Ark ECS.

**Fields:**
- All fields are unexported

**Methods:**
- ProcessBatches(ctx context.Context, batchChan <-chan MonitorBatch, progressChan chan<- EntityProgress) error
- GetStats() (entitiesCreated int64, batchesProcessed int64, rate float64)
- PulseRate() float64

**When to use:**
- Used internally by StreamingLoader
- Can be used standalone for custom loading

### EntityCreationConfig

Configuration for entity creation.

**Fields:**
- **ProgressChan** (chan<- EntityProgress) - Channel for progress updates
- **BatchSize** (int) - Batch size
- **PreAllocate** (int) - Number of entities to pre-allocate

**Methods:**
- None (data struct)

**When to use:**
- Configuring StreamingEntityCreator

### MonitorBatch

Represents a batch of monitors read from a file.

**Fields:**
- **Monitors** ([]schema.Monitor) - Monitors in batch
- **BatchID** (int) - Batch identifier
- **Offset** (int64) - File offset

**Methods:**
- None (data struct)

**When to use:**
- Internal data structure for streaming

### ParseConfig

Configuration for streaming parsers.

**Fields:**
- **ProgressChan** (chan<- Progress) - Channel for progress updates
- **BatchSize** (int) - Parse batch size
- **BufferSize** (int) - File buffer size
- **MaxMemory** (int64) - Maximum memory for parsing
- **StrictUnknownFields** (bool) - Error on unknown fields
- **JSONUseNumber** (bool) - Use number type for JSON

**Methods:**
- None (data struct)

**When to use:**
- Configuring streaming parsers

### Progress

Represents parsing progress.

**Fields:**
- **EntitiesProcessed** (int64) - Entities processed
- **TotalBytes** (int64) - Total file bytes
- **ProcessedBytes** (int64) - Bytes processed
- **Percentage** (float64) - Completion percentage
- **Rate** (float64) - Processing rate (entities/sec)
- **EstimatedRemaining** (time.Duration) - Estimated remaining time

**Methods:**
- None (data struct)

**When to use:**
- Progress monitoring during loading

### EntityProgress

Represents entity creation progress.

**Fields:**
- **EntitiesCreated** (int64) - Entities created
- **BatchesProcessed** (int64) - Batches processed
- **Rate** (float64) - Creation rate (entities/sec)
- **MemoryUsage** (int64) - Current memory usage

**Methods:**
- None (data struct)

**When to use:**
- Progress monitoring during entity creation

---

## Package: internal/controller/systems

### Logger (Interface)

Interface for system loggers.

**Methods:**
- Info(format string, args ...interface{})
- Debug(format string, args ...interface{})
- Warn(format string, args ...interface{})
- Error(format string, args ...interface{})
- LogSystemPerformance(name string, duration time.Duration, count int)

**When to use:**
- Interface for injecting loggers into systems

### StateLogger

Tracks entity state transitions.

**Fields:**
- All fields are unexported

**Methods:**
- LogStateChange(entityID uint32, component string, action string)
- LogSystemMetrics(systemName string, processed int, duration time.Duration)

**When to use:**
- Debugging state transitions
- Performance analysis

### MemoryConfig

Configuration for memory-efficient system.

**Fields:**
- **MaxEntities** (int) - Maximum entities
- **PreAllocate** (bool) - Pre-allocate memory

**Methods:**
- None (data struct)

**When to use:**
- Configuring MemoryEfficientSystem

### MemoryStats

Memory statistics.

**Fields:**
- **AllocatedEntities** (int) - Allocated entities
- **ActiveEntities** (int) - Active entities
- **MemoryUsage** (int64) - Memory usage in bytes

**Methods:**
- None (data struct)

**When to use:**
- Returned by MemoryEfficientSystem for monitoring

### ErrNoPulseJob

Error when no pulse job is found for an entity.

**Fields:**
- **EntityID** (uint32) - Entity ID

**Methods:**
- Error() string

**When to use:**
- Error handling in pulse systems

### ErrPulseJobTimeout

Error when pulse job times out.

**Fields:**
- **EntityID** (uint32) - Entity ID
- **Timeout** (time.Duration) - Timeout duration

**Methods:**
- Error() string

**When to use:**
- Error handling in pulse systems
