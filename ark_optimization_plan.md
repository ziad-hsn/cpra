# Ark Migration Optimization Plan

## Objective
Optimize the ark-migration branch to achieve high performance while maintaining the Ark ECS architecture. Focus on fixing batching logic, implementing efficient non-blocking queues, and preserving the original v0-draft logic patterns.

## Current Issues Analysis

### 1. Batching Problems
- **Batch size too large**: 5000 entities creates memory pressure and latency
- **All-or-nothing batching**: If one entity in batch fails, entire batch may be affected
- **Batch creation overhead**: Creating large batches every 100ms is inefficient
- **Memory allocation spikes**: Large batches cause GC pressure

### 2. Queue Architecture Issues
- **BoundedQueue blocks**: When full, causes state corruption
- **Complex abstraction layers**: BoundedQueue → BatchProcessor → DynamicWorkerPool
- **Backpressure handling**: Current implementation drops jobs but corrupts state
- **High latency**: Multiple queue layers add processing delays

### 3. State Management Issues
- **State corruption on queue full**: Entities stuck in PulsePending
- **No timeout recovery**: Entities can be stuck forever
- **Complex state transitions**: 7 systems with interdependencies

## Optimization Strategy

### Phase 1: Fix Critical Batching Logic

#### 1.1 Optimal Batch Sizing
```go
// Current problematic config
BatchSize: 5000 // Too large!

// Optimized config
BatchSize: 50-100 // Sweet spot for performance vs memory
```

**Rationale**: Smaller batches reduce memory pressure, improve responsiveness, and allow for better error isolation.

#### 1.2 Dynamic Batch Sizing
Implement adaptive batch sizing based on system load:
```go
type AdaptiveBatcher struct {
    minBatch int
    maxBatch int
    currentBatch int
    loadFactor float64
}

func (ab *AdaptiveBatcher) GetBatchSize() int {
    // Adjust batch size based on queue depth and processing time
    if ab.loadFactor > 0.8 {
        return ab.minBatch // Smaller batches under high load
    }
    return ab.currentBatch
}
```

#### 1.3 Streaming Batch Processing
Instead of collecting full batches before processing, implement streaming:
```go
func (bps *BatchPulseSystem) StreamingUpdate(ctx context.Context) error {
    batchJobs := make([]jobs.Job, 0, bps.BatchSize)
    batchEntities := make([]ecs.Entity, 0, bps.BatchSize)
    
    bps.Mapper.PulseNeeded.Map(func(entity ecs.Entity, comp *components.PulseNeeded) {
        // Add to current batch
        batchJobs = append(batchJobs, pulseJob)
        batchEntities = append(batchEntities, entity)
        
        // Process immediately when batch is ready
        if len(batchJobs) >= bps.BatchSize {
            bps.processBatch(batchJobs, batchEntities)
            // Reset batch
            batchJobs = batchJobs[:0]
            batchEntities = batchEntities[:0]
        }
    })
    
    // Process remaining items
    if len(batchJobs) > 0 {
        bps.processBatch(batchJobs, batchEntities)
    }
    
    return nil
}
```

### Phase 2: Implement High-Performance Non-Blocking Queue

#### 2.1 Lock-Free Ring Buffer Queue
Replace BoundedQueue with a lock-free ring buffer:
```go
type LockFreeQueue struct {
    buffer []jobs.Job
    head   uint64
    tail   uint64
    mask   uint64
}

func (q *LockFreeQueue) Enqueue(job jobs.Job) bool {
    tail := atomic.LoadUint64(&q.tail)
    head := atomic.LoadUint64(&q.head)
    
    // Check if queue is full
    if tail-head >= uint64(len(q.buffer)) {
        return false // Drop job, no blocking
    }
    
    // Store job
    q.buffer[tail&q.mask] = job
    atomic.StoreUint64(&q.tail, tail+1)
    return true
}

func (q *LockFreeQueue) Dequeue() (jobs.Job, bool) {
    head := atomic.LoadUint64(&q.head)
    tail := atomic.LoadUint64(&q.tail)
    
    if head >= tail {
        return jobs.Job{}, false // Empty
    }
    
    job := q.buffer[head&q.mask]
    atomic.StoreUint64(&q.head, head+1)
    return job, true
}
```

#### 2.2 Multiple Queue Channels
Use separate channels for different job types to reduce contention:
```go
type MultiChannelQueue struct {
    pulseJobs        chan jobs.Job
    interventionJobs chan jobs.Job
    codeJobs         chan jobs.Job
}

func (mcq *MultiChannelQueue) EnqueuePulse(job jobs.Job) bool {
    select {
    case mcq.pulseJobs <- job:
        return true
    default:
        return false // Non-blocking drop
    }
}
```

#### 2.3 Direct Worker Pool Integration
Eliminate BatchProcessor layer and connect directly to workers:
```go
type DirectWorkerPool struct {
    workers    []*Worker
    jobChannel chan jobs.Job
    resultChan chan jobs.Result
}

func (dwp *DirectWorkerPool) Start() {
    for i := range dwp.workers {
        go dwp.workers[i].Run(dwp.jobChannel, dwp.resultChan)
    }
}
```

### Phase 3: Optimize State Management

#### 3.1 Fix Queue Drop State Corruption
```go
func (bps *BatchPulseSystem) processBatch(batchJobs []jobs.Job, batchEntities []ecs.Entity) {
    // Try to enqueue batch
    if !bps.queue.EnqueueBatch(batchJobs) {
        // CRITICAL FIX: Don't transition state if enqueue fails
        bps.logger.Warn("Queue full, retrying batch later")
        // Keep entities in PulseNeeded state for retry
        return
    }
    
    // Only transition state after successful enqueue
    for _, entity := range batchEntities {
        bps.Mapper.PulseNeeded.Remove(entity)
        bps.Mapper.PulsePending.Add(entity, &components.PulsePending{
            StartTime: time.Now(),
        })
    }
}
```

#### 3.2 Implement Timeout Recovery System
```go
type TimeoutRecoverySystem struct {
    timeout time.Duration
}

func (trs *TimeoutRecoverySystem) Update(ctx context.Context) error {
    now := time.Now()
    
    trs.Mapper.PulsePending.Map(func(entity ecs.Entity, comp *components.PulsePending) {
        if now.Sub(comp.StartTime) > trs.timeout {
            // Entity stuck in pending, recover it
            trs.Mapper.PulsePending.Remove(entity)
            trs.Mapper.PulseNeeded.Add(entity, &components.PulseNeeded{})
            trs.logger.Warn("Recovered stuck entity %d", entity)
        }
    })
    
    return nil
}
```

### Phase 4: Performance Optimizations

#### 4.1 Reduce Update Interval
```go
// Current: 100ms (too slow)
UpdateInterval: 100 * time.Millisecond

// Optimized: 10ms (more responsive)
UpdateInterval: 10 * time.Millisecond
```

#### 4.2 Memory Pool for Batches
```go
type BatchPool struct {
    jobPool    sync.Pool
    entityPool sync.Pool
}

func (bp *BatchPool) GetJobBatch() []jobs.Job {
    if batch := bp.jobPool.Get(); batch != nil {
        return batch.([]jobs.Job)[:0]
    }
    return make([]jobs.Job, 0, 100)
}

func (bp *BatchPool) PutJobBatch(batch []jobs.Job) {
    bp.jobPool.Put(batch)
}
```

#### 4.3 Parallel Result Processing
```go
func (bprs *BatchPulseResultSystem) Update(world *ecs.World) {
    // Process results in parallel
    var wg sync.WaitGroup
    resultBatch := make([]jobs.Result, 0, 100)
    
    // Collect batch of results
    for len(resultBatch) < cap(resultBatch) {
        select {
        case result := <-bprs.ResultChan:
            resultBatch = append(resultBatch, result)
        default:
            break
        }
    }
    
    // Process in parallel chunks
    chunkSize := 25
    for i := 0; i < len(resultBatch); i += chunkSize {
        end := i + chunkSize
        if end > len(resultBatch) {
            end = len(resultBatch)
        }
        
        wg.Add(1)
        go func(chunk []jobs.Result) {
            defer wg.Done()
            bprs.processResultChunk(world, chunk)
        }(resultBatch[i:end])
    }
    
    wg.Wait()
}
```

## Implementation Priority

### Immediate (Week 1)
1. Fix queue drop state corruption bug
2. Reduce batch size from 5000 to 100
3. Implement timeout recovery system
4. Reduce update interval to 10ms

### Short-term (Week 2-3)
1. Replace BoundedQueue with lock-free ring buffer
2. Implement direct worker pool integration
3. Add memory pools for batch allocations
4. Implement adaptive batch sizing

### Medium-term (Week 4-6)
1. Implement parallel result processing
2. Add comprehensive metrics and monitoring
3. Optimize memory allocation patterns
4. Performance testing and tuning

## Expected Performance Improvements

### Throughput
- **Current**: 11.6 jobs/sec
- **Target**: 10,000+ jobs/sec (850x improvement)

### Latency
- **Current**: 77ms average
- **Target**: <5ms average (15x improvement)

### Memory
- **Current**: 145 MiB in 10 seconds
- **Target**: <50 MiB stable usage (3x improvement)

### Reliability
- **Current**: 75% failure rate
- **Target**: <1% failure rate

## Validation Plan

1. **Unit Tests**: Test each optimization component individually
2. **Integration Tests**: Test complete workflow with optimizations
3. **Load Tests**: Test with 1K, 10K, 100K, 1M monitors
4. **Stress Tests**: Test under high load and failure conditions
5. **Comparison Tests**: Compare with v0-draft performance

This optimization plan maintains the Ark ECS architecture while addressing all the performance bottlenecks through targeted improvements to batching, queueing, and state management.

