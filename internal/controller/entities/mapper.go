package entities

import (
	"cpra/internal/controller/components"
	"cpra/internal/interning"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// EntityManager uses the new consolidated component design.
// This dramatically reduces the number of archetypes and improves performance.
type EntityManager struct {
	// Core consolidated components - only a few archetypes instead of dozens.
	MonitorState       *ecs.Map1[components.MonitorState]
	PulseConfig        *ecs.Map1[components.PulseConfig]
	InterventionConfig *ecs.Map1[components.InterventionConfig]
	CodeConfig         *ecs.Map1[components.CodeConfig]
	CodeStatus         *ecs.Map1[components.CodeStatus]
	JobStorage         *ecs.Map1[components.JobStorage]
	Shard              *ecs.Map1[components.Shard]

	// Grouped mappers to minimize archetype moves during creation
	baseMapper *ecs.Map4[components.MonitorState, components.PulseConfig, components.JobStorage, components.Shard]
	codePair   *ecs.Map2[components.CodeConfig, components.CodeStatus]
	Disabled   *ecs.Map1[components.Disabled]

	// nextShard tracks round-robin shard assignment across entity creations.
	nextShard uint32
	// shardSlots determines the modulus for shard assignment.
	shardSlots uint32
}

// NewEntityManager creates a new consolidated entity manager.
func NewEntityManager(world *ecs.World) *EntityManager {
	return &EntityManager{
		MonitorState:       ecs.NewMap1[components.MonitorState](world),
		PulseConfig:        ecs.NewMap1[components.PulseConfig](world),
		InterventionConfig: ecs.NewMap1[components.InterventionConfig](world),
		CodeConfig:         ecs.NewMap1[components.CodeConfig](world),
		CodeStatus:         ecs.NewMap1[components.CodeStatus](world),
		JobStorage:         ecs.NewMap1[components.JobStorage](world),
		Shard:              ecs.NewMap1[components.Shard](world),
		baseMapper:         ecs.NewMap4[components.MonitorState, components.PulseConfig, components.JobStorage, components.Shard](world),
		codePair:           ecs.NewMap2[components.CodeConfig, components.CodeStatus](world),
		Disabled:           ecs.NewMap1[components.Disabled](world),
		shardSlots:         components.DefaultShardSlots,
	}
}

// SetShardSlots allows the controller to configure the number of shard slots dynamically.
// Values less than 1 fall back to DefaultShardSlots.
func (e *EntityManager) SetShardSlots(slots int) {
	if slots <= 0 {
		e.shardSlots = components.DefaultShardSlots
		return
	}
	e.shardSlots = uint32(slots)
}

// CreateEntityFromMonitor creates an entity using the consolidated design.
func (e *EntityManager) CreateEntityFromMonitor(
	monitor *schema.Monitor,
	world *ecs.World) error {

	// Validation
	if world == nil {
		return fmt.Errorf("world cannot be nil")
	}
	if e == nil {
		return fmt.Errorf("EntityManager cannot be nil")
	}
	if monitor.Name == "" {
		fmt.Println(monitor, "name cannot be empty")
		return fmt.Errorf("monitor name cannot be empty")
	}

	// Single time snapshot reused to avoid multiple now() calls
	now := time.Now()
	reg := components.DefaultConfigRegistry()

	// Create consolidated MonitorState component
	monitorName := interning.Intern(monitor.Name)
	monitorState := GetMonitorState()
	*monitorState = components.MonitorState{}
	monitorState.Name = monitorName
	monitorState.LastPulseCheckTime = now
	monitorState.LastEventTime = now
	monitorState.LastSuccessTime = now
	monitorState.NextCheckTime = now

	// Set initial state flags or Disabled tag
	if monitor.Enabled {
		monitorState.SetPulseFirstCheck(true)
	}

	// Prepare pulse configuration (added during entity creation)
	// Map thresholds: prefer explicit unhealthy_threshold; loader maps legacy max_failures into it
	pulseConfig := GetPulseConfig()
	*pulseConfig = components.PulseConfig{}
	pulseConfig.Type = interning.Intern(monitor.Pulse.Type)
	pulseConfig.UnhealthyThreshold = monitor.Pulse.UnhealthyThreshold
	pulseConfig.HealthyThreshold = monitor.Pulse.HealthyThreshold
	pulseConfig.Timeout = monitor.Pulse.Timeout
	pulseConfig.Interval = monitor.Pulse.Interval
	// Assign schema config directly; ownership is at an ECS component.
	// Future updates should replace the component (copy-on-write), not mutate in place.
	pulseConfig.Config = monitor.Pulse.Config
	// Create consolidated job storage (empty at first; jobs filled after we have the entity ID)
	// Pre-size maps based on number of codes to minimize rehashing/resizes
	codeCount := len(monitor.Codes)
	jobStorage := GetJobStorage()

	// Assign shard in round-robin fashion to spread workload across ticks.
	shardID := e.nextShard % e.shardSlots
	e.nextShard = (e.nextShard + 1) % e.shardSlots

	// Create an entity with base components in a single archetype transition
	shard := &components.Shard{ID: uint8(shardID)}
	entity := e.baseMapper.NewEntity(monitorState, pulseConfig, jobStorage, shard)
	if !world.Alive(entity) {
		// Return pooled components on error
		PutMonitorState(monitorState)
		PutPulseConfig(pulseConfig)
		PutJobStorage(jobStorage)
		return fmt.Errorf("failed to create valid entity")
	}

	// Return base components to pools immediately after Ark copies the values
	PutMonitorState(monitorState)
	PutPulseConfig(pulseConfig)
	PutJobStorage(jobStorage)

	// Add a pulse job to existing JobStorage
	pulseJob, err := jobs.CreatePulseJob(monitor.Pulse, entity)
	if err != nil {
		return err
	}
	if js := e.JobStorage.Get(entity); js != nil {
		js.PulseJob = pulseJob
	}

	// Add intervention if configured
	var interventionConfig *components.InterventionConfig
	if monitor.Intervention.Action != "" {
		maxFailures := 1
		if monitor.Intervention.MaxFailures > 0 {
			maxFailures = monitor.Intervention.MaxFailures
		}

		interventionConfig = GetInterventionConfig()
		*interventionConfig = components.InterventionConfig{}
		interventionConfig.Action = interning.Intern(monitor.Intervention.Action)
		// Assign a schema target directly; updates should replace the component (COW).
		interventionConfig.Target = monitor.Intervention.Target
		interventionConfig.MaxFailures = maxFailures
		e.InterventionConfig.Add(entity, interventionConfig)
		// Return to the pool after Ark copies the value
		PutInterventionConfig(interventionConfig)

		// Add an intervention job
		interventionJob, err := jobs.CreateInterventionJob(monitor.Intervention, entity)
		if err != nil {
			return err
		}
		if js := e.JobStorage.Get(entity); js != nil {
			js.InterventionJob = interventionJob
		}
	}

	// Add consolidated code configuration instead of separate color components
	var codeConfig *components.CodeConfig
	var codeStatus *components.CodeStatus
	if codeCount > 0 {
		codeConfig = GetCodeConfig(codeCount)
		codeStatus = GetCodeStatus(codeCount)

		for color, config := range monitor.Codes {
			colorKey := interning.Intern(color)
			idx := components.ColorToIndex(colorKey)
			if idx == components.ColorNone {
				continue
			}

			// Value type assignment
			cc := components.ColorCodeConfig{
				Dispatch: config.Dispatch,
				Notify:   interning.Intern(config.Notify),
				Config:   config.Config, // Copy interface/pointer
			}
			codeConfig.Configs[idx] = reg.GetOrAdd(cc)

			cs := components.ColorCodeStatus{
				LastAlertTime: now.Unix(),
			}
			codeStatus.Status[idx] = cs
		}

		// Add both code components in a single step
		e.codePair.Add(entity, codeConfig, codeStatus)
		// Pooling disabled, Put calls are no-ops but harmless
		PutCodeConfig(codeConfig)
		PutCodeStatus(codeStatus)
	}

	// Apply the Disabled tag after base creation if the monitor is disabled
	if !monitor.Enabled {
		e.Disabled.Add(entity, &components.Disabled{})
	}

	return nil
}

// pendingExtra holds components to be added after batch creation
type pendingExtra struct {
	InterventionConfig *components.InterventionConfig
	CodeConfig         *components.CodeConfig
	CodeStatus         *components.CodeStatus
	Entity             ecs.Entity
	Disabled           bool
}

// CreateEntitiesFromMonitors creates entities in a batch using Ark's Map3.NewBatchFn to minimize
// archetype transitions and reduce per-entity overhead. It mirrors CreateEntityFromMonitor logic
// for each monitor without changing behavior. Job creation error is recorded and returned
// after the batch completes; entities created before an error remain valid, identical to
// one-by-one creation semantics.
func (e *EntityManager) CreateEntitiesFromMonitors(world *ecs.World, monitors []schema.Monitor) error {
	// Validation
	if world == nil {
		return fmt.Errorf("world cannot be nil")
	}
	if e == nil {
		return fmt.Errorf("EntityManager cannot be nil")
	}
	if len(monitors) == 0 {
		return nil
	}

	// Single time snapshot reused to avoid multiple now() calls across the batch
	now := time.Now()
	reg := components.DefaultConfigRegistry()

	// Capture extras to add AFTER batch creation to avoid "locked world" panic
	pending := make([]pendingExtra, 0, min(len(monitors)/4, 4096))

	// We use a captured index to provide per-monitor data to the batch callback.
	i := 0
	var firstErr error
	shardCursor := e.nextShard

	e.baseMapper.NewBatchFn(len(monitors), func(entity ecs.Entity, monitorState *components.MonitorState, pulseConfig *components.PulseConfig, jobStorage *components.JobStorage, shard *components.Shard) {
		// If an error was already encountered, skip heavy work but still leave components initialized.
		if firstErr != nil {
			return
		}

		monitor := monitors[i]
		i++

		// Monitor name and times
		monitorName := interning.Intern(monitor.Name)
		monitorState.Name = monitorName
		monitorState.LastPulseCheckTime = now
		monitorState.LastEventTime = now
		monitorState.LastSuccessTime = now
		monitorState.NextCheckTime = now

		// Assign shard in round-robin order
		shardID := shardCursor % e.shardSlots
		shardCursor = (shardCursor + 1) % e.shardSlots
		if shard != nil {
			shard.ID = uint8(shardID)
		}

		// Initial state flags or Disabled tag
		if monitor.Enabled {
			monitorState.SetPulseFirstCheck(true)
		}

		// Pulse configuration: prefer explicit unhealthy_threshold; loader fills from legacy max_failures if provided
		pulseConfig.Type = interning.Intern(monitor.Pulse.Type)
		pulseConfig.UnhealthyThreshold = monitor.Pulse.UnhealthyThreshold
		pulseConfig.HealthyThreshold = monitor.Pulse.HealthyThreshold
		pulseConfig.Timeout = monitor.Pulse.Timeout
		pulseConfig.Interval = monitor.Pulse.Interval
		if monitor.Pulse.Config != nil {
			// Assign schema config directly; future changes should replace component (COW).
			pulseConfig.Config = monitor.Pulse.Config
		} else {
			pulseConfig.Config = nil
		}

		// Job storage: pre-size code jobs map based on the number of configured colors
		jobStorage.PulseJob = nil
		jobStorage.InterventionJob = nil

		// Create a pulse job and attach to JobStorage
		if pj, err := jobs.CreatePulseJob(monitor.Pulse, entity); err != nil {
			firstErr = err
			return
		} else {
			jobStorage.PulseJob = pj
		}

		// Prepare pending extra data
		extra := pendingExtra{Entity: entity}
		hasExtra := false

		// Intervention configuration (optional)
		if monitor.Intervention.Action != "" {
			maxFailures := 1
			if monitor.Intervention.MaxFailures > 0 {
				maxFailures = monitor.Intervention.MaxFailures
			}
			interventionConfig := GetInterventionConfig()
			*interventionConfig = components.InterventionConfig{}
			interventionConfig.Action = interning.Intern(monitor.Intervention.Action)
			// Assign a schema target directly; future changes should replace component (COW).
			interventionConfig.Target = monitor.Intervention.Target
			interventionConfig.MaxFailures = maxFailures

			extra.InterventionConfig = interventionConfig
			hasExtra = true

			// Create an intervention job and attach
			if ij, err := jobs.CreateInterventionJob(monitor.Intervention, entity); err != nil {
				firstErr = err
				// Note: we still might add intervention config if we don't return here,
				// but strict error handling says we should abort.
				// However, if we abort, we leak the pooled component from GetInterventionConfig above?
				// No, we haven't added it to a pending list yet. We should Put it back if we fail.
				PutInterventionConfig(interventionConfig)
				return
			} else {
				jobStorage.InterventionJob = ij
			}
		}

		// Consolidated code configuration & status
		codeCount := len(monitor.Codes)
		if codeCount > 0 {
			codeConfig := GetCodeConfig(codeCount)
			codeStatus := GetCodeStatus(codeCount)

			for color, cfg := range monitor.Codes {
				colorKey := interning.Intern(color)
				idx := components.ColorToIndex(colorKey)
				if idx == components.ColorNone {
					continue
				}

				// Per-color config
				cc := components.ColorCodeConfig{
					Dispatch: cfg.Dispatch,
					Notify:   interning.Intern(cfg.Notify),
					Config:   cfg.Config,
				}
				codeConfig.Configs[idx] = reg.GetOrAdd(cc)

				// Per-color status
				cs := components.ColorCodeStatus{
					LastAlertTime: now.Unix(),
				}
				codeStatus.Status[idx] = cs
			}

			extra.CodeConfig = codeConfig
			extra.CodeStatus = codeStatus
			hasExtra = true
		}

		// Apply the Disabled tag after base creation if the monitor is disabled
		if !monitor.Enabled {
			extra.Disabled = true
			hasExtra = true
		}

		if hasExtra {
			pending = append(pending, extra)
		}
	})

	// Apply pending components after batch creation is done (world unlocked)
	for _, p := range pending {
		if p.InterventionConfig != nil {
			e.InterventionConfig.Add(p.Entity, p.InterventionConfig)
			PutInterventionConfig(p.InterventionConfig)
		}
		if p.CodeConfig != nil && p.CodeStatus != nil {
			e.codePair.Add(p.Entity, p.CodeConfig, p.CodeStatus)
			PutCodeConfig(p.CodeConfig)
			PutCodeStatus(p.CodeStatus)
		}
		if p.Disabled {
			e.Disabled.Add(p.Entity, &components.Disabled{})
		}
	}

	// Persist shard cursor for later batches
	e.nextShard = shardCursor

	return firstErr
}

// EnableMonitor enables a monitor using consolidated state flags
func (e *EntityManager) EnableMonitor(entity ecs.Entity) {
	// Remove the Disabled tag if present and schedule the first check
	e.Disabled.Remove(entity)
	if state := e.MonitorState.Get(entity); state != nil {
		state.SetPulseFirstCheck(true)
	}
}

// DisableMonitor disables a monitor using consolidated state flags
func (e *EntityManager) DisableMonitor(entity ecs.Entity) {
	// Add Disabled tag and clear pending flags
	e.Disabled.Add(entity, &components.Disabled{})
	if state := e.MonitorState.Get(entity); state != nil {
		state.SetPulsePending(false)
		state.SetInterventionPending(false)
		state.SetCodePending(false)
	}
}

// GetMonitorState provides easy access to consolidated state
func (e *EntityManager) GetMonitorState(entity ecs.Entity) *components.MonitorState {
	return e.MonitorState.Get(entity)
}
