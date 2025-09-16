# Ark-Migration ECS Flow Trace: The Real Issues

## System Architecture Overview

The ark-migration branch uses a completely different architecture:

**Systems Execution Order** (from `optimized_controller.go:300-340`):
```go
1. BatchPulseScheduleSystem.Update()  // Marks entities as PulseNeeded
2. BatchPulseSystem.Update()          // Dispatches jobs, PulseNeeded -> PulsePending  
3. BatchInterventionSystem.Update()   // Handles interventions
4. BatchCodeSystem.Update()           // Handles code notifications
5. BatchPulseResultSystem.Update()    // Processes results, removes PulsePending
6. BatchInterventionResultSystem.Update()
7. BatchCodeResultSystem.Update()
```

**Key Difference**: No command buffer! Direct ECS operations within each system.

## Complete Flow Trace

### Initial Entity State
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob, PulseFirstCheck]
```

### Cycle 1: BatchPulseScheduleSystem.Update()

**File**: `batch_pulse_schedule_system.go:45-65` (collectWork)
```go
// Filter finds entities ready for scheduling
s.PulseFilter = ecs.NewFilter2[components.PulseConfig, components.PulseStatus](w).
    Without(ecs.C[components.DisabledMonitor]()).
    Without(ecs.C[components.PulseNeeded]()).      // ✅ Entity 123 doesn't have this yet
    Without(ecs.C[components.PulsePending]()).     // ✅ Entity 123 doesn't have this yet

query := s.PulseFilter.Query()
for query.Next() {
    ent := query.Entity()  // ent = 123
    config, status := query.Get()
    
    // First check logic
    if s.Mapper.PulseFirstCheck.HasAll(ent) {  // ✅ Entity 123 has PulseFirstCheck
        toCheck = append(toCheck, ent)  // Entity 123 added
    }
}
// Result: toCheck = [123]
```

**File**: `batch_pulse_schedule_system.go:75-85` (applyWork)
```go
for _, ent := range entities {  // ent = 123
    if w.Alive(ent) {
        // Add PulseNeeded component
        if !s.Mapper.PulseNeeded.HasAll(ent) {
            s.Mapper.PulseNeeded.Add(ent, &components.PulseNeeded{})  // ✅ IMMEDIATELY applied
        }
        
        // Remove FirstCheck
        if s.Mapper.PulseFirstCheck.HasAll(ent) {
            s.Mapper.PulseFirstCheck.Remove(ent)  // ✅ IMMEDIATELY applied
        }
    }
}
```

**Entity State After Schedule**:
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob, PulseNeeded]
```

### Cycle 1: BatchPulseSystem.Update() (Same Cycle!)

**File**: `batch_pulse_system.go:55-75` (collectWork)
```go
// Filter finds entities with PulseNeeded but not PulsePending
bps.PulseNeededFilter = ecs.NewFilter1[components.PulseNeeded](bps.world).
    Without(ecs.C[components.PulsePending]())

query := bps.PulseNeededFilter.Query()
for query.Next() {
    ent := query.Entity()  // ent = 123 ✅ Found immediately!
    pulseJobComp := bps.Mapper.PulseJob.Get(ent)  // ✅ PulseJob exists
    job := pulseJobComp.Job
    out[ent] = job  // Job collected
}
// Result: out = {123: HTTPJob}
```

**File**: `batch_pulse_system.go:175-210` (Update method)
```go
// Convert to slices for batch processing
e = [123]
j = [HTTPJob]

// Submit jobs to queue
bps.queue.EnqueueBatch(batchJobs)  // ✅ Job sent to workers

// Apply component transitions
bps.applyWork(bps.world, batchEntities, batchJobs)
```

**File**: `batch_pulse_system.go:115-135` (applyWork)
```go
// Use Ark's batch operations for component transitions
pulseNeededFilter := ecs.NewFilter1[components.PulseNeeded](w).
    Without(ecs.C[components.PulsePending]())

batch := pulseNeededFilter.Batch()

// ✅ CRITICAL: Uses Ark's batch operations!
bps.Mapper.PulseNeeded.RemoveBatch(batch, nil)           // Remove PulseNeeded
bps.Mapper.PulsePending.AddBatch(batch, &components.PulsePending{})  // Add PulsePending
```

**Entity State After Dispatch**:
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob, PulsePending]
```

### Cycle 1: BatchPulseResultSystem.Update() (Same Cycle!)

**File**: `batch_pulse_result_system.go:35-50` (collectPulseResults)
```go
// Check for results (job might still be processing)
select {
case res := <-bprs.ResultChan:
    out[res.Entity()] = res
default:
    break loop  // No results yet
}
// Result: out = {} (empty - job still processing)
```

**Entity State After Cycle 1**:
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob, PulsePending]
```

### Cycle 2: BatchPulseScheduleSystem.Update()

**File**: `batch_pulse_schedule_system.go:40-45` (Initialize)
```go
// Filter excludes entities with PulseNeeded or PulsePending
s.PulseFilter = ecs.NewFilter2[components.PulseConfig, components.PulseStatus](w).
    Without(ecs.C[components.PulseNeeded]()).      // ✅ Entity 123 doesn't have this
    Without(ecs.C[components.PulsePending]()).     // ❌ Entity 123 HAS this - EXCLUDED!

// Entity 123 is filtered out - no scheduling
```

### Cycle 2: BatchPulseSystem.Update()

```go
// Filter looks for PulseNeeded entities
// Entity 123 has PulsePending, not PulseNeeded - no dispatch
```

### Cycle 2: BatchPulseResultSystem.Update()

**File**: `batch_pulse_result_system.go:35-50` (collectPulseResults)
```go
// Job completes, result available
select {
case res := <-bprs.ResultChan:  // res = {Entity: 123, Error: nil}
    out[res.Entity()] = res
}
// Result: out = {123: SuccessResult}
```

**File**: `batch_pulse_result_system.go:55-90` (processPulseResultsAndQueueStructuralChanges)
```go
for _, res := range results {  // res for entity 123
    entity := res.Entity()  // 123
    
    if !w.Alive(entity) || !bprs.Mapper.PulsePending.HasAll(entity) {
        continue  // ✅ Entity 123 is alive and has PulsePending
    }
    
    if res.Error() != nil {
        // Handle failure...
    } else {
        // ✅ SUCCESS path
        statusCopy := bprs.Mapper.PulseStatus.Get(entity)
        statusCopy.LastStatus = "success"
        statusCopy.ConsecutiveFailures = 0
        statusCopy.LastSuccessTime = time.Now()
        statusCopy.LastCheckTime = time.Now()  // ✅ Updates LastCheckTime
    }
    
    // ✅ CRITICAL: Remove PulsePending directly
    bprs.Mapper.PulsePending.Remove(entity)
}
```

**Entity State After Cycle 2**:
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob]
```

### Cycle 3: BatchPulseScheduleSystem.Update()

**File**: `batch_pulse_schedule_system.go:55-70` (collectWork)
```go
// Filter now includes entity 123 again (no PulseNeeded/PulsePending)
query := s.PulseFilter.Query()
for query.Next() {
    ent := query.Entity()  // ent = 123
    config, status := query.Get()
    
    interval := config.Interval  // e.g., 30s
    timeSinceLast := now.Sub(status.LastCheckTime)  // ~10ms (just updated)
    
    // Check if enough time has passed
    if timeSinceLast >= interval {  // 10ms >= 30s? NO
        toCheck = append(toCheck, ent)
    }
}
// Result: toCheck = [] (empty - not enough time passed)
```

**Entity 123 waits for next interval...**

### Cycle N (After 30 seconds): Normal Operation

```go
// After 30 seconds, timeSinceLast >= interval
// Entity 123 gets scheduled again
// Flow repeats: Schedule -> Dispatch -> Result -> Schedule...
```

## Critical Issues Identified

### 1. **Batch Operation Inefficiency** 

**File**: `batch_pulse_system.go:115-135`
```go
// PROBLEM: Creates filter and batch for EVERY entity transition
pulseNeededFilter := ecs.NewFilter1[components.PulseNeeded](w).
    Without(ecs.C[components.PulsePending]())
batch := pulseNeededFilter.Batch()

// This creates a new filter and batch operation for each applyWork call
// Instead of batching multiple entities together
```

**Issue**: The "batch" operations are not actually batching multiple entities efficiently. Each call to `applyWork` creates its own filter and batch, defeating the purpose.

### 2. **Excessive Filter Creation**

**File**: `batch_pulse_system.go:115` 
```go
// PROBLEM: Creates new filter on every applyWork call
pulseNeededFilter := ecs.NewFilter1[components.PulseNeeded](w).
    Without(ecs.C[components.PulsePending]())
```

**Issue**: Filters should be created once and reused, not recreated on every operation.

### 3. **Small Batch Sizes**

**File**: `batch_pulse_system.go:185-195`
```go
// Process in batches
for i := 0; i < len(e); i += bps.batchSize {
    end := i + bps.batchSize  // batchSize is likely small (25-100)
    if end > len(e) {
        end = len(e)
    }
    
    batchEntities := e[i:end]  // Small batches
    batchJobs := j[i:end]
}
```

**Issue**: Based on the crash logs showing 5000 entity batches failing, the system is trying to process large numbers but the batch size is too small, creating overhead.

### 4. **Queue Bottleneck**

**File**: `batch_pulse_system.go:200-205`
```go
// Submit batch of jobs to queue
if err := bps.queue.EnqueueBatch(batchJobs); err != nil {
    // If queue full, apply component transitions anyway
    bps.logger.Warn("Failed to enqueue batch %d, queue full: %v", batchCount, err)
}
```

**Issue**: When queue is full, component transitions are applied anyway, creating entities stuck in `PulsePending` state with no corresponding jobs.

### 5. **Memory Allocation Pattern**

**File**: `batch_pulse_system.go:175-185`
```go
// Convert map to slices for batch processing
e := make([]ecs.Entity, 0, len(toDispatch))      // New allocation
j := make([]jobs.Job, 0, len(toDispatch))        // New allocation

for ent, job := range toDispatch {
    e = append(e, ent)
    j = append(j, job)
}
```

**Issue**: Creates new slices on every update instead of reusing pre-allocated buffers.

## The Real Performance Problems

### 1. **False Batching**
The system claims to use "batch operations" but actually processes entities in small groups with individual filter creation.

### 2. **Filter Overhead**
Creates new filters on every operation instead of reusing cached filters.

### 3. **Queue Saturation**
Queue becomes full, causing component state corruption (entities in `PulsePending` without jobs).

### 4. **Memory Churn**
Constant allocation/deallocation of slices and filters causes GC pressure.

### 5. **Suboptimal Batch Sizes**
Uses small batch sizes (25-100) instead of large batches (10K+) that Ark is optimized for.

## The Flow Actually Works But Is Inefficient

**Key Finding**: The ECS flow is NOT broken in ark-migration! The state transitions work correctly:

1. ✅ Schedule marks entities as `PulseNeeded`
2. ✅ Dispatch processes `PulseNeeded` → `PulsePending` + sends jobs
3. ✅ Results process completions and remove `PulsePending`
4. ✅ Entities return to schedulable state

**The Real Problem**: The implementation is extremely inefficient due to:
- False batching (small batches with individual filter creation)
- Queue saturation causing state corruption
- Memory allocation patterns causing GC pressure
- Suboptimal use of Ark's batch capabilities

The system works for small numbers but fails under load due to performance issues, not flow issues.

