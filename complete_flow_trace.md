# Complete ECS Flow Trace: The Real Problem

## Entity Creation Flow (Working Correctly)

**File**: `mapper.go:140-170` (CreateEntityFromMonitor)
```go
entity := e.World.NewEntity()  // Creates entity ID: 123

// Assigns components in this order:
1. Name: "monitor-1"
2. MonitorStatus: {LastCheckTime: now}
3. PulseConfig: {Interval: 30s, MaxFailures: 3, ...}
4. PulseStatus: {LastCheckTime: now}
5. PulseJob: {Job: HTTPJob{URL: "https://example.com"}}
6. PulseFirstCheck: {} (if enabled)
```

**Initial Entity State**:
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob, PulseFirstCheck]
```

## The Broken State Machine Flow

### Cycle 1: Schedule Phase ✅ (Works)

**File**: `sys_pulse.go:35-50` (collectWork)
```go
// Filter matches entity 123 because:
// - Has PulseConfig ✅
// - Has PulseStatus ✅  
// - Does NOT have PulsePending ✅
// - Does NOT have PulseNeeded ✅ (yet)

query := s.PulseFilter.Query(w.Mappers.World)
for query.Next() {
    ent := query.Entity()  // ent = 123
    
    // First check branch triggers
    if w.Mappers.World.Has(ent, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
        toCheck = append(toCheck, ent)  // Entity 123 added
    }
}
// Result: toCheck = [123]
```

**File**: `sys_pulse.go:52-58` (applyWork)
```go
for _, ent := range entities {  // ent = 123
    if w.IsAlive(ent) && !w.Mappers.World.Has(ent, ecs.ComponentID[components.PulseNeeded](w.Mappers.World)) {
        commandBuffer.schedulePulse(ent)  // Queues: opAssignPulseNeeded for 123
    }
}
```

**Command Buffer State**: `[{opAssignPulseNeeded, entity: 123}]`

### Cycle 1: Dispatch Phase ❌ (BROKEN - Timing Issue)

**File**: `sys_pulse.go:75-88` (collectWork)
```go
// PROBLEM: PulseNeeded component hasn't been applied yet!
query := s.PulseNeeded.Query(w.Mappers.World)
for query.Next() {
    // NO ENTITIES FOUND because command buffer hasn't run yet
}
// Result: out = {} (empty)
```

**🚨 CRITICAL TIMING FLAW**: Dispatch runs BEFORE command buffer playback!

### Cycle 1: Result Phase (No-op)
```go
// No jobs dispatched, so no results
results := s.collectResults()  // Empty map
```

### Cycle 1: Command Buffer Playback ⚡ (Finally Applies Changes)

**File**: `commandbuffer.go:95-180`
```go
for i := range s.ops {
    op := &s.ops[i]  // opAssignPulseNeeded for entity 123
    
    case opAssignPulseNeeded:
        if !has(e, s.pulseNeededID) && !has(e, s.pulsePendingID) {
            s.PulseNeeded.Assign(e, &components.PulseNeeded{})  // FINALLY adds PulseNeeded
        }
}
```

**Entity State After Cycle 1**:
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob, PulseFirstCheck, PulseNeeded]
```

### Cycle 2: Schedule Phase ❌ (BROKEN - Filter Exclusion)

**File**: `sys_pulse.go:18-25`
```go
// PROBLEM: Filter now EXCLUDES entity 123!
s.PulseFilter = generic.NewFilter2[components.PulseConfig, components.PulseStatus]().
    Without(generic.T[components.PulsePending]()).
    Without(generic.T[components.PulseNeeded]())  // ❌ Excludes entity 123!

// Entity 123 has PulseNeeded component, so it's filtered OUT
query := s.PulseFilter.Query(w.Mappers.World)
for query.Next() {
    // Entity 123 is NOT returned by this query
}
// Result: toCheck = [] (empty)
```

**🚨 CRITICAL FILTER FLAW**: Once entity gets PulseNeeded, it's permanently excluded from scheduling!

### Cycle 2: Dispatch Phase ✅ (Finally Works)

**File**: `sys_pulse.go:75-88`
```go
// NOW the dispatch can see entity 123
query := s.PulseNeeded.Query(w.Mappers.World)
for query.Next() {
    ent := query.Entity()  // ent = 123
    job := w.Mappers.PulseJob.Get(ent).Job  // ✅ PulseJob exists (created during entity creation)
    
    stCopy := *w.Mappers.PulseStatus.Get(ent)
    stCopy.LastCheckTime = time.Now()
    
    out[ent] = dispatchablePulse{Job: job, Status: stCopy}
}
// Result: out = {123: {Job: HTTPJob, Status: updated}}
```

**File**: `sys_pulse.go:90-110` (applyWork)
```go
for e, item := range list {  // e = 123
    select {
    case s.JobChan <- item.Job:  // ✅ Job sent to worker
        commandBuffer.SetPulseStatus(e, item.Status)     // Queued
        commandBuffer.removeFirstCheck(e)                // Queued  
        commandBuffer.MarkPulsePending(e)                // Queued: PulseNeeded -> PulsePending
    default:
        // Job channel full - entity gets stuck!
    }
}
```

**Command Buffer State**: 
```
[
  {opSetPulseStatus, entity: 123, pulseStatus: {LastCheckTime: now}},
  {opRemoveFirstCheck, entity: 123},
  {opExchangePulsePending, entity: 123}  // PulseNeeded -> PulsePending
]
```

### Cycle 2: Result Phase (No results yet)
```go
// Job is still processing in worker, no results yet
```

### Cycle 2: Command Buffer Playback ⚡

**File**: `commandbuffer.go:95-180`
```go
// Applies the 3 operations:
1. Updates PulseStatus.LastCheckTime
2. Removes PulseFirstCheck component  
3. Exchanges PulseNeeded -> PulsePending
```

**Entity State After Cycle 2**:
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob, PulsePending]
```

### Cycle 3: Schedule Phase ❌ (Still Excluded)

```go
// Entity 123 still excluded because it has PulsePending
s.PulseFilter = generic.NewFilter2[components.PulseConfig, components.PulseStatus]().
    Without(generic.T[components.PulsePending]())  // ❌ Excludes entity 123!
```

### Cycle 3: Dispatch Phase (No-op)
```go
// No entities with PulseNeeded (entity 123 now has PulsePending)
```

### Cycle 3: Result Phase ✅ (Job Completes)

**File**: `sys_pulse.go:125-140` (collectResults)
```go
// Worker completed the job, result available
select {
case res := <-s.ResultChan:  // res = {Entity: 123, Error: nil, Status: "success"}
    out[res.Entity()] = res
}
// Result: out = {123: {Entity: 123, Error: nil}}
```

**File**: `sys_pulse.go:142-200` (processResultsAndQueueStructuralChanges)
```go
for _, res := range results {  // res for entity 123
    entity := res.Entity()  // 123
    
    if !w.IsAlive(entity) || !w.Mappers.World.Has(entity, ecs.ComponentID[components.PulsePending](w.Mappers.World)) {
        continue  // ✅ Entity 123 is alive and has PulsePending
    }
    
    if res.Error() != nil {
        // Handle failure...
    } else {
        // ✅ SUCCESS path
        statusCopy := *w.Mappers.PulseStatus.Get(entity)
        statusCopy.LastStatus = "success"
        statusCopy.ConsecutiveFailures = 0
        statusCopy.LastSuccessTime = time.Now()
        
        commandBuffer.SetPulseStatus(entity, statusCopy)
        commandBuffer.setMonitorStatus(entity, monitorCopy)
    }
    
    // ✅ Always remove PulsePending
    commandBuffer.RemovePulsePending(entity)
}
```

### Cycle 3: Command Buffer Playback ❌ (BROKEN REMOVAL)

**File**: `commandbuffer.go:425-435` (RemovePulsePending)
```go
func (s *CommandBufferSystem) RemovePulsePending(entity ecs.Entity) {
    // BROKEN CODE:
    s.Add(cbOp{k: opRemoveCodePending, e: entity}) // ❌ WRONG OPERATION TYPE!
    // Should be: opRemovePulsePending (which doesn't exist)
}
```

**🚨 CRITICAL BUG**: RemovePulsePending uses wrong operation type! Entity stays stuck in PulsePending state forever!

**Entity State After Cycle 3**:
```
Entity 123: [Name, MonitorStatus, PulseConfig, PulseStatus, PulseJob, PulsePending]  // STUCK!
```

### Cycle 4+: Deadlock State

```go
// Entity 123 is permanently stuck:
// - Has PulsePending component (never removed due to bug)
// - Excluded from Schedule phase (Without PulsePending filter)
// - Excluded from Dispatch phase (no PulseNeeded component)
// - No more results to process

// Entity 123 will NEVER be processed again!
```

## The Complete Problem Analysis

### 1. **Timing Desynchronization**
```
Schedule → Dispatch → Result → CommandBuffer
   ↓         ↓         ↓         ↓
 Queues    Can't see  No jobs   Finally
changes   changes    sent      applies
```

**Should be**:
```
Schedule → CommandBuffer → Dispatch → CommandBuffer → Result → CommandBuffer
   ↓            ↓            ↓            ↓           ↓            ↓
 Queues      Applies      Sees        Applies     Processes   Applies
changes     changes      changes     changes     results     changes
```

### 2. **Broken State Removal**
```go
// commandbuffer.go:425-435 - COMPLETELY BROKEN
func (s *CommandBufferSystem) RemovePulsePending(entity ecs.Entity) {
    s.Add(cbOp{k: opRemoveCodePending, e: entity}) // ❌ Wrong operation!
}
```

### 3. **Missing Operation Types**
```go
const (
    opSetPulseStatus opKind = iota
    // ... existing ops
    // MISSING: opRemovePulsePending  ❌
    // MISSING: opRemovePulseNeeded   ❌
)
```

### 4. **Filter Deadlock Design**
```go
// Once entity gets PulseNeeded, it's excluded from scheduling forever
Without(generic.T[components.PulseNeeded]())
```

## The Real Fix Required

### 1. **Fix Command Buffer Operations**
```go
const (
    // ... existing ops
    opRemovePulsePending opKind = iota + 100  // Add missing operation
)

func (s *CommandBufferSystem) RemovePulsePending(entity ecs.Entity) {
    s.Add(cbOp{k: opRemovePulsePending, e: entity})  // Use correct operation
}

// In PlayBack():
case opRemovePulsePending:
    if has(e, s.pulsePendingID) {
        s.PulsePending.Remove(e)
        setHas(e, s.pulsePendingID, false)
    }
```

### 2. **Fix System Execution Order**
```go
// scheduler.go - Apply changes between phases
for _, sys := range s.ScheduleSystems {
    sys.Update(s.World, s.CommandBuffer)
}
s.CommandBuffer.PlayBack()  // ✅ Apply immediately

for _, sys := range s.DispatchSystems {
    sys.Update(s.World, s.CommandBuffer)
}
s.CommandBuffer.PlayBack()  // ✅ Apply immediately

for _, sys := range s.ResultSystems {
    sys.Update(s.World, s.CommandBuffer)
}
s.CommandBuffer.PlayBack()  // ✅ Apply immediately
```

### 3. **Fix Filter Logic**
```go
// Remove the problematic Without() clauses
s.PulseFilter = generic.NewFilter2[components.PulseConfig, components.PulseStatus]().
    Without(generic.T[components.DisabledMonitor]()).
    Without(generic.T[components.InterventionNeeded]()).
    Without(generic.T[components.InterventionPending]()).
    Without(generic.T[components.CodeNeeded]()).
    Without(generic.T[components.CodePending]())
    // REMOVED: Without(generic.T[components.PulseNeeded]())
    // REMOVED: Without(generic.T[components.PulsePending]())
```

## Summary

The ECS flow is broken due to:

1. **Timing Issues**: 1-cycle delay between state changes and visibility
2. **Broken Removal**: RemovePulsePending function uses wrong operation type
3. **Missing Operations**: Critical operation types not implemented
4. **Filter Deadlock**: Entities get permanently excluded from processing
5. **Wrong Execution Order**: Systems can't see each other's changes

These issues cause entities to get stuck in intermediate states, creating a non-smooth flow where monitors stop being processed after their first job completes.

