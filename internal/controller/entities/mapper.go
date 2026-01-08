package entities

import (
	"cpra/internal/controller/components"
	"cpra/internal/interning"
	"cpra/internal/runtime/jobs"
	"cpra/internal/platform/loader/schema"
	"fmt"
	"sort"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// EntityManager uses the new consolidated component design.
// This dramatically reduces the number of archetypes and improves performance.
type EntityManager struct {
	world *ecs.World
	// Core consolidated components - only a few archetypes instead of dozens.
	MonitorState       *ecs.Map1[components.MonitorState]
	PulseConfig        *ecs.Map1[components.PulseConfig]
	InterventionConfig *ecs.Map1[components.InterventionConfig]
	CodeConfig         *ecs.Map1[components.CodeConfig]
	CodeStatus         *ecs.Map1[components.CodeStatus]
	JobStorage         *ecs.Map1[components.JobStorage]
	Shard              *ecs.Map1[components.Shard]
	PendingUpdate      *ecs.Map1[components.PendingUpdate]

	// Grouped mappers to minimize archetype moves during creation
	baseMapper *ecs.Map4[components.MonitorState, components.PulseConfig, components.JobStorage, components.Shard]
	codePair   *ecs.Map2[components.CodeConfig, components.CodeStatus]
	Disabled   *ecs.Map1[components.Disabled]

	// nextShard tracks round-robin shard assignment across entity creations.
	nextShard uint32
	// shardSlots determines the modulus for shard assignment.
	shardSlots uint32

	idIndex   map[string]ecs.Entity
	slugIndex map[string]ecs.Entity
	nameIndex map[string]ecs.Entity
}

// NewEntityManager creates a new consolidated entity manager.
func NewEntityManager(world *ecs.World) *EntityManager {
	return &EntityManager{
		world:              world,
		MonitorState:       ecs.NewMap1[components.MonitorState](world),
		PulseConfig:        ecs.NewMap1[components.PulseConfig](world),
		InterventionConfig: ecs.NewMap1[components.InterventionConfig](world),
		CodeConfig:         ecs.NewMap1[components.CodeConfig](world),
		CodeStatus:         ecs.NewMap1[components.CodeStatus](world),
		JobStorage:         ecs.NewMap1[components.JobStorage](world),
		Shard:              ecs.NewMap1[components.Shard](world),
		PendingUpdate:      ecs.NewMap1[components.PendingUpdate](world),
		baseMapper:         ecs.NewMap4[components.MonitorState, components.PulseConfig, components.JobStorage, components.Shard](world),
		codePair:           ecs.NewMap2[components.CodeConfig, components.CodeStatus](world),
		Disabled:           ecs.NewMap1[components.Disabled](world),
		shardSlots:         components.DefaultShardSlots,
		idIndex:            make(map[string]ecs.Entity),
		slugIndex:          make(map[string]ecs.Entity),
		nameIndex:          make(map[string]ecs.Entity),
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
	monitorState.ID = monitor.ID
	monitorState.Name = monitorName
	monitorState.Slug = interning.Intern(monitor.Slug)
	monitorState.Metadata = monitor.Metadata
	monitorState.ConfigVersion = 1
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
	pulseConfig.Groups = append([]string(nil), monitor.Pulse.Groups...)
	pulseConfig.UnhealthyThreshold = monitor.Pulse.UnhealthyThreshold
	pulseConfig.HealthyThreshold = monitor.Pulse.HealthyThreshold
	pulseConfig.Timeout = monitor.Pulse.Timeout
	pulseConfig.Interval = monitor.Pulse.Interval
	pulseConfig.Retries = pulseConfigRetries(monitor.Pulse.Config)
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
	e.registerIdentifiers(entity, monitorState)
	PutMonitorState(monitorState)
	PutPulseConfig(pulseConfig)
	PutJobStorage(jobStorage)

	// Add a pulse job to existing JobStorage
	pulseJob, err := jobs.CreatePulseJob(monitor.Pulse, entity)
	if err != nil {
		return err
	}
	if e.JobStorage.HasAll(entity) {
		js := e.JobStorage.Get(entity)
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
		interventionConfig.Retries = monitor.Intervention.Retries
		interventionConfig.MaxFailures = maxFailures
		e.InterventionConfig.Add(entity, interventionConfig)
		// Return to the pool after Ark copies the value
		PutInterventionConfig(interventionConfig)

		// Add an intervention job
		interventionJob, err := jobs.CreateInterventionJob(monitor.Intervention, entity)
		if err != nil {
			return err
		}
		if e.JobStorage.HasAll(entity) {
			js := e.JobStorage.Get(entity)
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
		monitorState.ID = monitor.ID
		monitorState.Name = monitorName
		monitorState.Slug = interning.Intern(monitor.Slug)
		monitorState.Metadata = monitor.Metadata
		monitorState.ConfigVersion = 1
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
		pulseConfig.Groups = append([]string(nil), monitor.Pulse.Groups...)
		pulseConfig.UnhealthyThreshold = monitor.Pulse.UnhealthyThreshold
		pulseConfig.HealthyThreshold = monitor.Pulse.HealthyThreshold
		pulseConfig.Timeout = monitor.Pulse.Timeout
		pulseConfig.Interval = monitor.Pulse.Interval
		pulseConfig.Retries = pulseConfigRetries(monitor.Pulse.Config)
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
			interventionConfig.Retries = monitor.Intervention.Retries
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

		e.registerIdentifiers(entity, monitorState)
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
	if e.MonitorState.HasAll(entity) {
		state := e.MonitorState.Get(entity)
		state.SetPulseFirstCheck(true)
	}
}

// DisableMonitor disables a monitor using consolidated state flags
func (e *EntityManager) DisableMonitor(entity ecs.Entity) {
	// Add Disabled tag and clear pending flags
	e.Disabled.Add(entity, &components.Disabled{})
	if e.MonitorState.HasAll(entity) {
		state := e.MonitorState.Get(entity)
		state.SetPulsePending(false)
		state.SetInterventionPending(false)
		state.SetCodePending(false)
		state.SetPulseNeeded(false)
		state.SetInterventionNeeded(false)
		state.SetCodeNeeded(false)
		state.PendingColor = components.ColorNone
	}
}

// GetMonitorState provides easy access to consolidated state
func (e *EntityManager) GetMonitorState(entity ecs.Entity) *components.MonitorState {
	return e.MonitorState.Get(entity)
}

// ResolveEntity finds an entity by ID, slug, or name (in that order).
func (e *EntityManager) ResolveEntity(identifier string) (ecs.Entity, bool) {
	if identifier == "" || e == nil {
		return ecs.Entity{}, false
	}
	if ent, ok := e.idIndex[identifier]; ok {
		return ent, true
	}
	if ent, ok := e.slugIndex[identifier]; ok {
		return ent, true
	}
	if ent, ok := e.nameIndex[identifier]; ok {
		return ent, true
	}
	return ecs.Entity{}, false
}

// MonitorFromEntity reconstructs a schema.Monitor from ECS components.
func (e *EntityManager) MonitorFromEntity(entity ecs.Entity) (*schema.Monitor, error) {
	if entity.IsZero() {
		return nil, fmt.Errorf("MonitorFromEntity called with zero entity")
	}
	if !e.world.Alive(entity) {
		return nil, fmt.Errorf("MonitorFromEntity: entity %d (gen %d) is not alive", entity.ID(), entity.Gen())
	}
	// Use HasAll() before Get() - ark's Get does unsafe pointer arithmetic
	// and may return invalid pointers for entities without the component.
	if !e.MonitorState.HasAll(entity) {
		return nil, fmt.Errorf("monitor not found for entity: %d", entity.ID())
	}
	state := e.MonitorState.Get(entity)
	if !e.PulseConfig.HasAll(entity) {
		return nil, fmt.Errorf("monitor missing pulse config: %s", state.Name)
	}
	pulseConfig := e.PulseConfig.Get(entity)

	monitor := &schema.Monitor{
		ID:       state.ID,
		Name:     state.Name,
		Slug:     state.Slug,
		Enabled:  !e.Disabled.HasAll(entity),
		Metadata: state.Metadata,
	}

	monitor.Pulse = schema.Pulse{
		Type:               pulseConfig.Type,
		Interval:           pulseConfig.Interval,
		Timeout:            pulseConfig.Timeout,
		MaxFailures:        pulseConfig.UnhealthyThreshold,
		UnhealthyThreshold: pulseConfig.UnhealthyThreshold,
		HealthyThreshold:   pulseConfig.HealthyThreshold,
		Groups:             append([]string(nil), pulseConfig.Groups...),
	}
	if pulseConfig.Config != nil {
		monitor.Pulse.Config = pulseConfig.Config.Copy()
	}

	// Use HasAll() before Get() - ark's Get does unsafe pointer arithmetic
	// and may return invalid pointers for entities without the component.
	if e.InterventionConfig.HasAll(entity) {
		intervention := e.InterventionConfig.Get(entity)
		monitor.Intervention = schema.Intervention{
			Action:      intervention.Action,
			Retries:     intervention.Retries,
			MaxFailures: intervention.MaxFailures,
		}
		if intervention.Target != nil {
			monitor.Intervention.Target = intervention.Target.Copy()
		}
	}

	if e.codePair.HasAll(entity) {
		codeConfig, _ := e.codePair.Get(entity)
		reg := components.DefaultConfigRegistry()
		codes := make(schema.Codes)
		for color := components.ColorCode(0); color < components.MaxColors; color++ {
			cfg, ok := reg.Lookup(codeConfig.Configs[color])
			if !ok || cfg.Notify == "" {
				continue
			}
			code := schema.CodeConfig{
				Notify:   cfg.Notify,
				Dispatch: cfg.Dispatch,
			}
			if cfg.Config != nil {
				code.Config = cfg.Config.Copy()
			}
			codes[color.String()] = code
		}
		if len(codes) > 0 {
			monitor.Codes = codes
		}
	}

	return monitor, nil
}

// SetPendingUpdate stores a deferred monitor update for later application.
func (e *EntityManager) SetPendingUpdate(entity ecs.Entity, spec *schema.Monitor) {
	if e == nil || spec == nil {
		return
	}
	pending := &components.PendingUpdate{
		Spec:        spec,
		RequestedAt: time.Now(),
	}
	if e.PendingUpdate.HasAll(entity) {
		e.PendingUpdate.Set(entity, pending)
	} else {
		e.PendingUpdate.Add(entity, pending)
	}
}

// ApplyMonitorSpec updates ECS components from a monitor spec.
// Callers must ensure the entity has no in-flight jobs before applying.
func (e *EntityManager) ApplyMonitorSpec(entity ecs.Entity, spec *schema.Monitor) (*schema.Monitor, error) {
	if e == nil {
		return nil, fmt.Errorf("EntityManager cannot be nil")
	}
	if spec == nil {
		return nil, fmt.Errorf("monitor spec cannot be nil")
	}

	state := e.MonitorState.Get(entity)
	if state == nil {
		return nil, fmt.Errorf("monitor not found: %s", spec.Name)
	}

	newPulseJob, err := jobs.CreatePulseJob(spec.Pulse, entity)
	if err != nil {
		return nil, err
	}

	var newInterventionJob jobs.Job
	if spec.Intervention.Action != "" {
		newInterventionJob, err = jobs.CreateInterventionJob(spec.Intervention, entity)
		if err != nil {
			jobs.ReleasePulseJob(newPulseJob)
			return nil, err
		}
	}

	var codeConfig *components.CodeConfig
	var codeStatus *components.CodeStatus
	if len(spec.Codes) > 0 {
		reg := components.DefaultConfigRegistry()
		codeConfig = GetCodeConfig(len(spec.Codes))
		codeStatus = GetCodeStatus(len(spec.Codes))
		now := time.Now()
		for color, cfg := range spec.Codes {
			colorKey := interning.Intern(color)
			idx := components.ColorToIndex(colorKey)
			if idx == components.ColorNone {
				continue
			}
			cc := components.ColorCodeConfig{
				Dispatch: cfg.Dispatch,
				Notify:   interning.Intern(cfg.Notify),
				Config:   cfg.Config,
			}
			codeConfig.Configs[idx] = reg.GetOrAdd(cc)
			codeStatus.Status[idx] = components.ColorCodeStatus{
				LastAlertTime: now.Unix(),
			}
		}
	}

	jobStorage := e.JobStorage.Get(entity)
	if jobStorage == nil {
		jobs.ReleasePulseJob(newPulseJob)
		if newInterventionJob != nil {
			jobs.ReleaseInterventionJob(newInterventionJob)
		}
		if codeConfig != nil {
			PutCodeConfig(codeConfig)
		}
		if codeStatus != nil {
			PutCodeStatus(codeStatus)
		}
		return nil, fmt.Errorf("monitor missing job storage: %s", state.Name)
	}

	// Update identifiers and indexes
	e.unregisterIdentifiers(entity, state)
	state.ID = spec.ID
	state.Name = interning.Intern(spec.Name)
	state.Slug = interning.Intern(spec.Slug)
	state.Metadata = spec.Metadata
	state.ConfigVersion++
	e.registerIdentifiers(entity, state)

	// Pulse configuration and job
	pulseConfig := &components.PulseConfig{
		Type:               interning.Intern(spec.Pulse.Type),
		Groups:             append([]string(nil), spec.Pulse.Groups...),
		Timeout:            spec.Pulse.Timeout,
		Interval:           spec.Pulse.Interval,
		Retries:            pulseConfigRetries(spec.Pulse.Config),
		UnhealthyThreshold: spec.Pulse.UnhealthyThreshold,
		HealthyThreshold:   spec.Pulse.HealthyThreshold,
	}
	if spec.Pulse.Config != nil {
		pulseConfig.Config = spec.Pulse.Config.Copy()
	}
	if e.PulseConfig.HasAll(entity) {
		e.PulseConfig.Set(entity, pulseConfig)
	} else {
		e.PulseConfig.Add(entity, pulseConfig)
	}

	oldPulseJob := jobStorage.PulseJob
	jobStorage.PulseJob = newPulseJob
	if oldPulseJob != nil {
		jobs.ReleasePulseJob(oldPulseJob)
	}

	state.SetPulseFirstCheck(true)
	state.NextCheckTime = time.Now()

	// Intervention configuration and job
	if spec.Intervention.Action == "" {
		if e.InterventionConfig.HasAll(entity) {
			e.InterventionConfig.Remove(entity)
		}
		if jobStorage.InterventionJob != nil {
			jobs.ReleaseInterventionJob(jobStorage.InterventionJob)
			jobStorage.InterventionJob = nil
		}
		state.SetInterventionNeeded(false)
		state.SetInterventionPending(false)
	} else {
		maxFailures := 1
		if spec.Intervention.MaxFailures > 0 {
			maxFailures = spec.Intervention.MaxFailures
		}
		interventionConfig := &components.InterventionConfig{
			Action:      interning.Intern(spec.Intervention.Action),
			Target:      spec.Intervention.Target,
			Retries:     spec.Intervention.Retries,
			MaxFailures: maxFailures,
		}
		if e.InterventionConfig.HasAll(entity) {
			e.InterventionConfig.Set(entity, interventionConfig)
		} else {
			e.InterventionConfig.Add(entity, interventionConfig)
		}
		oldInterventionJob := jobStorage.InterventionJob
		jobStorage.InterventionJob = newInterventionJob
		if oldInterventionJob != nil {
			jobs.ReleaseInterventionJob(oldInterventionJob)
		}
	}

	// Code configuration
	if len(spec.Codes) == 0 {
		if e.codePair.HasAll(entity) {
			e.codePair.Remove(entity)
		}
		state.SetCodeNeeded(false)
		state.SetCodePending(false)
		state.PendingColor = components.ColorNone
	} else {
		if codeConfig == nil || codeStatus == nil {
			return nil, fmt.Errorf("missing code config for monitor: %s", state.Name)
		}
		if e.codePair.HasAll(entity) {
			e.codePair.Set(entity, codeConfig, codeStatus)
		} else {
			e.codePair.Add(entity, codeConfig, codeStatus)
		}
		PutCodeConfig(codeConfig)
		PutCodeStatus(codeStatus)
	}

	// Enabled/disabled state
	if spec.Enabled {
		e.EnableMonitor(entity)
	} else {
		e.DisableMonitor(entity)
	}

	if e.PendingUpdate.HasAll(entity) {
		e.PendingUpdate.Remove(entity)
	}

	return e.MonitorFromEntity(entity)
}

// GetMonitorByID retrieves a monitor by its ID, slug, or name.
func (e *EntityManager) GetMonitorByID(id string) (*schema.Monitor, error) {
	entity, ok := e.ResolveEntity(id)
	if !ok {
		return nil, fmt.Errorf("monitor not found: %s", id)
	}
	monitor, err := e.MonitorFromEntity(entity)
	if err != nil {
		return nil, err
	}
	return monitor, nil
}

// ListMonitors retrieves all monitors with optional pagination and filters.
// Returns a slice of monitors and total count for pagination.
func (e *EntityManager) ListMonitors(pageSize, offset int, enabled *bool, pulseType string) ([]*schema.Monitor, int) {
	if e == nil || e.world == nil {
		return nil, 0
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	if offset < 0 {
		offset = 0
	}

	filter := ecs.NewFilter2[components.MonitorState, components.PulseConfig](e.world)
	query := filter.Query()
	entries := make([]monitorEntry, 0)

	for query.Next() {
		entity := query.Entity()
		state, pulseConfig := query.Get()
		if state == nil || pulseConfig == nil {
			continue
		}

		isEnabled := !e.Disabled.HasAll(entity)
		if enabled != nil && *enabled != isEnabled {
			continue
		}
		if pulseType != "" && pulseConfig.Type != pulseType {
			continue
		}

		entries = append(entries, monitorEntry{
			entity: entity,
			name:   state.Name,
			id:     state.ID,
		})
	}

	total := len(entries)
	if total == 0 {
		return nil, 0
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].name == entries[j].name {
			return entries[i].id < entries[j].id
		}
		return entries[i].name < entries[j].name
	})

	if offset >= total {
		return nil, total
	}
	end := offset + pageSize
	if end > total {
		end = total
	}

	monitors := make([]*schema.Monitor, 0, end-offset)
	for _, entry := range entries[offset:end] {
		monitor, err := e.MonitorFromEntity(entry.entity)
		if err != nil {
			continue
		}
		monitors = append(monitors, monitor)
	}
	return monitors, total
}

type monitorEntry struct {
	entity ecs.Entity
	name   string
	id     string
}

func (e *EntityManager) registerIdentifiers(entity ecs.Entity, state *components.MonitorState) {
	if state == nil {
		return
	}
	if state.ID != "" {
		e.idIndex[state.ID] = entity
	}
	if state.Slug != "" {
		e.slugIndex[state.Slug] = entity
	}
	if state.Name != "" {
		e.nameIndex[state.Name] = entity
	}
}

func (e *EntityManager) unregisterIdentifiers(entity ecs.Entity, state *components.MonitorState) {
	if state == nil {
		return
	}
	if state.ID != "" {
		if existing, ok := e.idIndex[state.ID]; ok && existing == entity {
			delete(e.idIndex, state.ID)
		}
	}
	if state.Slug != "" {
		if existing, ok := e.slugIndex[state.Slug]; ok && existing == entity {
			delete(e.slugIndex, state.Slug)
		}
	}
	if state.Name != "" {
		if existing, ok := e.nameIndex[state.Name]; ok && existing == entity {
			delete(e.nameIndex, state.Name)
		}
	}
}

// UnregisterEntity removes identifier indexes for an entity if present.
func (e *EntityManager) UnregisterEntity(entity ecs.Entity) {
	if e == nil {
		return
	}
	state := e.MonitorState.Get(entity)
	if state == nil {
		return
	}
	e.unregisterIdentifiers(entity, state)
}

func pulseConfigRetries(cfg schema.PulseConfig) int {
	switch typed := cfg.(type) {
	case *schema.PulseHTTPConfig:
		return typed.Retries
	case *schema.PulseTCPConfig:
		return typed.Retries
	case *schema.PulseICMPConfig:
		return typed.Retries
	default:
		return 0
	}
}
