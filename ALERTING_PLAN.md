# Alerting Architecture Plan

## Goals
- Add a composable alert layer enabling debounce/cooldown, suppression, and future features without modifying job transports.
- Keep ECS world mutations inside systems; avoid goroutines mutating ECS state.
- Allow flexible queue strategy for alerts (single vs per-color) with scale-based switching.

## Scope
- New `internal/alert` package containing a small `Policy` interface and a default `Manager` implementation.
- Optional ECS scheduler system for true debounce (delay-until-quiet), mirroring the existing pulse scheduler pattern.
- Integrations within existing systems to call the manager/policy for decisions and state stamping.
- Queue strategy for alerts with clear scale thresholds.

## Package Layout
- `internal/alert/policy.go`
  - `type Policy interface { ShouldDispatch(ent ecs.Entity, color string, now time.Time) (bool, string) }`
  - `DefaultPolicy` (uses cooldown window + recovery bypass).
- `internal/alert/manager.go`
  - `type Manager struct { policy Policy; world *ecs.World; mappers...; cooldown time.Duration }`
  - `func NewManager(world *ecs.World, p Policy) *Manager`
  - `func (m *Manager) Request(ent ecs.Entity, color string, now time.Time) (immediate bool, notBefore time.Time, reason string)`
  - `func (m *Manager) OnEnqueued(ent ecs.Entity, color string, now time.Time)`
  - `func (m *Manager) OnResult(ent ecs.Entity, color string, err error, now time.Time)`

Notes
- Systems depend only on the `Policy`-shaped surface; `Manager` implements it and adds helper methods used by systems.
- Avoid `alert.AlertManager` stutter: package name conveys the domain already.

## Data Model
- Extend `components.ColorCodeStatus` with (names tentative):
  - `NotBefore time.Time` — earliest time an alert of this color can be released (debounce/cooldown target).
  - `PendingRequestedAt time.Time` — when request was last made (optional for observability).
- Alternatively (simpler, coarser): a single `NextCodeNotBefore time.Time` in `components.MonitorState` if global per-entity cooldown is enough.

## Scheduling Strategy
- Cooldown-only (rate limit): handled synchronously by `Policy`/`Manager` — no scheduler needed.
- True debounce (delay until quiet): add a small ECS scheduler.
  - New `systems.BatchCodeScheduleSystem` scans entities with `state.PendingCode != ""` and when `now >= status[color].NotBefore` sets `StateCodeNeeded`, clears `PendingCode`.
  - This mirrors `systems.BatchPulseScheduleSystem` and keeps all world writes inside ECS ticks.

## Integration Points
- Trigger sites
  - `internal/controller/systems/batch_pulse_result_system.go: triggerCode`
  - `internal/controller/systems/batch_intervention_result_system.go: triggerCode`
  - Replace direct `StateCodeNeeded` with:
    - `imm, notBefore, _ := alertMgr.Request(ent, color, time.Now())`
    - If `imm`: set `PendingCode = color` and `StateCodeNeeded`.
    - Else: set `PendingCode = color` and store `NotBefore` in `CodeStatus[color]` (scheduler will release).
- Enqueue gate
  - `internal/controller/systems/batch_code_system.go`
  - Before batching, optional re-check `policy.ShouldDispatch(ent, color, now)` to skip stale/spurious alerts; after enqueue, call `alertMgr.OnEnqueued(...)`.
- Result handling
  - `internal/controller/systems/batch_code_result_system.go`
  - After success/failure, call `alertMgr.OnResult(ent, color, err, now)` to update `CodeStatus`.
- Controller wiring
  - Construct once: `am := alert.NewManager(world, defaultPolicy)`
  - Pass to systems that need it (code system and both trigger result systems).

## Queue Strategy (Alerts)
- We will provision a dedicated alert queue (separate from other job types) to isolate backpressure and scaling knobs.
- Open question: single queue vs per-color queues.
  - Single queue
    - Pros: simpler routing, fewer worker pools/routers, lower memory overhead.
    - Cons: head-of-line blocking across colors if a specific color spikes.
  - Per-color queues (e.g., red/yellow/green/cyan)
    - Pros: isolation, per-color scaling/latency targets, priority scheduling possibilities.
    - Cons: extra queues, routers, worker pools; higher memory footprint and coordination complexity.
- Memory/footprint considerations
  - Each additional queue implies an extra worker pool (ants pool), result channels, and router buffers.
  - Estimate pool buffers ~O(maxWorkers) and queue buffers ~O(capacity). For 4 colors, roughly 4× the steady memory and some CPU overhead.
- Scale-based queue implementation
  - Use Workiva queue implementation for < 100k entities (lower overhead, unbounded semantics via sentinel capacity).
  - Switch to Adaptive queue when >= 100k (bounded power-of-two ring with stats), similar to existing `switchToAdaptiveQueues` approach.
  - Decision can be based on entity count and/or sustained enqueue rate.

Action Item
- Decide: start with a single alert queue; revisit per-color queues after observing traffic patterns and contention in production.

## Config
- Add controller config for:
  - `AlertCooldown` (duration) and `RecoveryBypass` (bool).
  - Alert queue type and capacity; color-to-priority mapping if needed later.
  - Thresholds for switching Workiva→Adaptive (e.g., entityCount >= 100k).

## Metrics/Observability
- Queue stats: depth, rates, wait times for alert queue(s).
- Policy decisions: counters for debounced, dispatched, recovery overrides.
- Scheduler: time-to-release histograms; coalescing counts.

## Phased Plan
1) Introduce `internal/alert` (Policy + Manager, cooldown only).
2) Wire manager into trigger sites, code enqueue, and result handlers.
3) Add `NotBefore` field(s) to status; implement `BatchCodeScheduleSystem` (true debounce, optional feature flag).
4) Add dedicated alert queue and worker pool; route code jobs through it.
5) Implement queue switching logic (Workiva < 100k; Adaptive after), reusing pattern from controller.
6) Expose config + metrics; add basic unit tests for policy decisions and scheduler behavior.

## Acceptance Criteria
- Alerts are suppressed during cooldown and allow green recovery overrides.
- Optional debounce releases alerts only after `NotBefore` via scheduler.
- Alert queue operates independently; controller can switch between Workiva and Adaptive based on scale.
- No goroutines outside systems mutate ECS state.

## Open Questions
- Single vs per-color alert queues (start single; revisit after data).
- Priority handling across colors in a single queue (e.g., weighted dispatch or priority queue) if needed.
- Persistence of alert state across restarts (out of scope for now).

