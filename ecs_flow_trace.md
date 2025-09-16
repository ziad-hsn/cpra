# ECS Flow Tracing: Identifying Broken State Transitions

## Execution Flow Trace

Let me trace the exact execution path for a single monitor through the entire system:

### Initial State
```
Entity: 123
Components: [PulseConfig, PulseStatus, PulseFirstCheck]
PulseStatus.LastCheckTime: 0
PulseConfig.Interval: 30s
```

### Cycle 1: Schedule Phase

**File**: `sys_pulse.go:35-50` (PulseScheduleSystem.collectWork)
```go
// TRACE: Entity 123 enters collectWork
query := s.PulseFilter.Query(w.Mappers.World)
for query.Next() {
    ent := query.Entity()  // ent = 123
    interval := w.Mappers.PulseConfig.Get(ent).Interval  // 30s
    lastCheckTime := w.Mappers.PulseStatus.Get(ent).LastCheckTime  // 0
    
    // TRACE: First-time check branch
    if w.Mappers.World.Has(ent, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
        toCheck = append(toCheck, ent)  // Entity 123 added to toCheck
        log.Printf("%v --> %v\n", time.Since(lastCheckTime), interval)
        continue
    }
}
// RESULT: toCheck = [123]
```

**File**: `sys_pulse.go:52-58` (PulseScheduleSystem.applyWork)
```go
// TRACE: Processing toCheck = [123]
for _, ent := range entities {  // ent = 123
    if w.IsAlive(ent) && !w.Mappers.World.Has(ent, ecs.ComponentID[components.PulseNeeded](w.Mappers.World)) {
        commandBuffer.schedulePulse(ent)  // Queues opAssignPulseNeeded for entity 123
    }
}
// RESULT: CommandBuffer has 1 operation: opAssignPulseNeeded for entity 123
```

### Cycle 1: Dispatch Phase

**File**: `sys_pulse.go:75-88` (PulseDispatchSystem.collectWork)
```go
// TRACE: Entity 123 should have PulseNeeded component now, but...
query := s.PulseNeeded.Query(w.Mappers.World)
for query.Next() {
    // PROBLEM: Entity 123 is NOT found because CommandBuffer hasn't been played back yet!
    // The PulseNeeded component was only queued, not actually added
}
// RESULT: out = {} (empty map)
```

**🚨 CRITICAL FLAW IDENTIFIED**: The dispatch system runs BEFORE the command buffer playback, so it can't see the PulseNeeded components that were just scheduled!

### Cycle 1: Result Phase
```go
// TRACE: No jobs were dispatched, so no results to process
results := s.collectResults()  // Empty
// RESULT: results = {}
```

### Cycle 1: Command Buffer Playback

**File**: `commandbuffer.go:95-180` (PlayBack)
```go
// TRACE: Finally applying the scheduled operations
for i := range s.ops {
    op := &s.ops[i]  // opAssignPulseNeeded for entity 123
    e := op.e        // 123
    
    case opAssignPulseNeeded:
        if !has(e, s.pulseNeededID) && !has(e, s.pulsePendingID) {
            s.PulseNeeded.Assign(e, &components.PulseNeeded{})  // FINALLY adds PulseNeeded
            setHas(e, s.pulseNeededID, true)
        }
}
```

**State After Cycle 1**:
```
Entity: 123
Components: [PulseConfig, PulseStatus, PulseFirstCheck, PulseNeeded]  // PulseNeeded added
```

### Cycle 2: Schedule Phase
```go
// TRACE: Entity 123 now has PulseNeeded, so it's filtered OUT by the Without() clause
s.PulseFilter = generic.NewFilter2[components.PulseConfig, components.PulseStatus]().
    Without(generic.T[components.PulsePending]()).
    Without(generic.T[components.PulseNeeded]())  // ❌ Entity 123 excluded!

// RESULT: Entity 123 is completely ignored in schedule phase
```

### Cycle 2: Dispatch Phase
```go
// TRACE: Now PulseDispatchSystem can see entity 123
query := s.PulseNeeded.Query(w.Mappers.World)
for query.Next() {
    ent := query.Entity()  // ent = 123
    job := w.Mappers.PulseJob.Get(ent).Job  // ❌ CRASH! PulseJob component doesn't exist!
}
```

**🚨 CRITICAL FLAW IDENTIFIED**: The system assumes PulseJob component exists but it's never created!

Let me check the entity creation code...

**File**: Looking for entity creation - NOT FOUND in the provided files!

This reveals the core issue: **The entity creation process is incomplete and the state machine is broken.**

## State Transition Flow Analysis

### Expected Flow (What Should Happen)
```
1. Ready → Schedule → PulseNeeded
2. PulseNeeded → Dispatch → PulsePending + Job Sent
3. PulsePending → Result → Ready (success) or Failed (error)
```

### Actual Flow (What Actually Happens)
```
1. Ready → Schedule → PulseNeeded (queued)
2. PulseNeeded (not visible) → Dispatch → Nothing
3. CommandBuffer → PulseNeeded (finally added)
4. PulseNeeded → Dispatch → CRASH (missing PulseJob)
```

## Root Cause Analysis

### 1. **Timing Issue: Command Buffer Delay**
The command buffer creates a **1-cycle delay** between state changes:
- Cycle N: Schedule decides entity needs pulse → queues PulseNeeded
- Cycle N: Dispatch runs → can't see PulseNeeded (not applied yet)
- Cycle N: CommandBuffer applies → PulseNeeded finally added
- Cycle N+1: Dispatch can finally see PulseNeeded

### 2. **Missing Component: PulseJob**
The dispatch system expects `PulseJob` component but it's never created:
```go
job := w.Mappers.PulseJob.Get(ent).Job  // CRASH: component doesn't exist
```

### 3. **Broken Filter Logic**
The schedule filter excludes entities that have PulseNeeded:
```go
Without(generic.T[components.PulseNeeded]())  // Excludes entities waiting for dispatch
```

This means once an entity gets PulseNeeded, it's **permanently excluded** from scheduling until the component is removed.

### 4. **State Machine Deadlock**
```
Entity gets PulseNeeded → Excluded from scheduling → Never gets processed → Stuck forever
```

## Detailed Command Buffer Flow Issues

### CommandBuffer.RemovePulsePending Implementation
**File**: `commandbuffer.go:425-435`
```go
func (s *CommandBufferSystem) RemovePulsePending(entity ecs.Entity) {
    // NOTE: ^^^ ignore. The real removal op is below:
    s.Add(cbOp{k: opRemoveCodePending, e: entity}) // also ignore. final correct impl:
    // The lines above are leftovers from a previous draft; use this:
    s.Add(cbOp{k: opExchangePulsePending}) // wrong again.
    // Final, correct one:
    // s.Add(cbOp{k: opRemovePulsePending, e: entity})
    // Since we don't have opRemovePulsePending kind enumerated, we remove via mapper:
    // To avoid confusion, provide a direct helper instead:
    // ==> Use RemovePulsePendingIndirect in PlayBack (handled by dedicated case).
}
```

**🚨 CRITICAL FLAW**: The RemovePulsePending function is completely broken! It has commented-out code and wrong operation types. This means entities get stuck in PulsePending state forever.

### Missing Operation Types
The command buffer is missing critical operation types:
```go
const (
    opSetPulseStatus opKind = iota
    // ... other ops
    // MISSING: opRemovePulsePending
    // MISSING: opRemovePulseNeeded  
)
```

## Flow Correction Analysis

### The Real Problem: System Execution Order

**File**: `scheduler.go:60-75`
```go
// WRONG ORDER: All systems run, THEN command buffer applies
for _, sys := range s.ScheduleSystems {
    sys.Update(s.World, s.CommandBuffer)  // Queues operations
}
for _, sys := range s.DispatchSystems {
    sys.Update(s.World, s.CommandBuffer)  // Can't see queued changes!
}
for _, sys := range s.ResultSystems {
    sys.Update(s.World, s.CommandBuffer)  // Still can't see changes!
}
s.CommandBuffer.PlayBack()  // Finally applies changes for NEXT cycle
```

### Correct Flow Should Be:
```go
// CORRECT: Apply changes between phases
for _, sys := range s.ScheduleSystems {
    sys.Update(s.World, s.CommandBuffer)
}
s.CommandBuffer.PlayBack()  // Apply schedule changes immediately

for _, sys := range s.DispatchSystems {
    sys.Update(s.World, s.CommandBuffer)
}
s.CommandBuffer.PlayBack()  // Apply dispatch changes immediately

for _, sys := range s.ResultSystems {
    sys.Update(s.World, s.CommandBuffer)
}
s.CommandBuffer.PlayBack()  // Apply result changes immediately
```

## Entity Lifecycle Trace

Let me trace what happens to an entity through multiple cycles:

### Cycle 1
```
Start: [PulseConfig, PulseStatus, PulseFirstCheck]
Schedule: Queues PulseNeeded
Dispatch: Sees nothing (PulseNeeded not applied yet)
Result: Nothing to process
CommandBuffer: Adds PulseNeeded
End: [PulseConfig, PulseStatus, PulseFirstCheck, PulseNeeded]
```

### Cycle 2
```
Start: [PulseConfig, PulseStatus, PulseFirstCheck, PulseNeeded]
Schedule: Entity excluded by Without(PulseNeeded) filter
Dispatch: CRASH - tries to get non-existent PulseJob component
Result: Nothing to process
CommandBuffer: No operations
End: [PulseConfig, PulseStatus, PulseFirstCheck, PulseNeeded] (stuck)
```

### Cycle 3+
```
Entity remains stuck with PulseNeeded component forever
Never gets processed again
System effectively deadlocked for this entity
```

## Summary of Flow Issues

1. **Timing Desync**: 1-cycle delay between state changes and visibility
2. **Missing Components**: PulseJob component never created
3. **Broken Removal**: RemovePulsePending function is non-functional
4. **Filter Deadlock**: Entities get permanently excluded from processing
5. **Incomplete State Machine**: No proper transitions between states
6. **System Order**: Wrong execution order prevents smooth flow

The ECS flow is fundamentally broken due to these architectural issues. The state transitions are not smooth because entities get stuck in intermediate states and can't progress through the intended lifecycle.

