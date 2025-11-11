package entities

import (
	"cpra/internal/controller/components"
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

	// Grouped mappers to minimize archetype moves during creation
	baseMapper *ecs.Map3[components.MonitorState, components.PulseConfig, components.JobStorage]
	codePair   *ecs.Map2[components.CodeConfig, components.CodeStatus]
	Disabled   *ecs.Map1[components.Disabled]
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
		baseMapper:         ecs.NewMap3[components.MonitorState, components.PulseConfig, components.JobStorage](world),
		codePair:           ecs.NewMap2[components.CodeConfig, components.CodeStatus](world),
		Disabled:           ecs.NewMap1[components.Disabled](world),
	}
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

	// Create consolidated MonitorState component
	monitorName := monitor.Name
	monitorState := &components.MonitorState{
		Name:            monitorName,
		LastCheckTime:   now,
		LastSuccessTime: now,
		NextCheckTime:   now,
	}

	// Set initial state flags or Disabled tag
	if monitor.Enabled {
		monitorState.SetPulseFirstCheck(true)
	}

	// Prepare pulse configuration (added during entity creation)
	// Map thresholds: prefer explicit unhealthy_threshold; loader maps legacy max_failures into it
	unhealthy := monitor.Pulse.UnhealthyThreshold
	pulseConfig := &components.PulseConfig{
		Type:               monitor.Pulse.Type,
		UnhealthyThreshold: unhealthy,
		HealthyThreshold:   monitor.Pulse.HealthyThreshold,
		Timeout:            monitor.Pulse.Timeout,
		Interval:           monitor.Pulse.Interval,
		// Assign schema config directly; ownership is at ECS component.
		// Future updates should replace the component (copy-on-write), not mutate in place.
		Config: monitor.Pulse.Config,
	}
	// Create consolidated job storage (empty at first; jobs filled after we have the entity ID)
	// Pre-size maps based on number of codes to minimize rehashing/resizes
	codeCount := len(monitor.Codes)
	jobStorage := &components.JobStorage{CodeJobs: make(map[string]jobs.Job, codeCount)}

	// Create entity with base components in a single archetype transition
	entity := e.baseMapper.NewEntity(monitorState, pulseConfig, jobStorage)
	if !world.Alive(entity) {
		return fmt.Errorf("failed to create valid entity")
	}

	// Add pulse job to existing JobStorage
	pulseJob, err := jobs.CreatePulseJob(monitor.Pulse, entity)
	if err != nil {
		return err
	}
	if js := e.JobStorage.Get(entity); js != nil {
		js.PulseJob = pulseJob
	}

	// Add intervention if configured
	if monitor.Intervention.Action != "" {
		maxFailures := 1
		if monitor.Intervention.MaxFailures > 0 {
			maxFailures = monitor.Intervention.MaxFailures
		}

		interventionConfig := &components.InterventionConfig{
			Action: monitor.Intervention.Action,
			// Assign schema target directly; updates should replace the component (COW).
			Target:      monitor.Intervention.Target,
			MaxFailures: maxFailures,
		}
		e.InterventionConfig.Add(entity, interventionConfig)

		// Add intervention job
		interventionJob, err := jobs.CreateInterventionJob(monitor.Intervention, entity)
		if err != nil {
			return err
		}
		if js := e.JobStorage.Get(entity); js != nil {
			js.InterventionJob = interventionJob
		}
	}

	// Add consolidated code configuration instead of separate color components
	if codeCount > 0 {
		codeConfig := &components.CodeConfig{Configs: make(map[string]*components.ColorCodeConfig, codeCount)}
		codeStatus := &components.CodeStatus{Status: make(map[string]*components.ColorCodeStatus, codeCount)}

		for color, config := range monitor.Codes {
			// Single consolidated entry instead of separate components
			codeConfig.Configs[color] = &components.ColorCodeConfig{
				Dispatch: config.Dispatch,
				Notify:   config.Notify,
				// Assign schema notification config directly; updates should replace (COW).
				Config: config.Config,
			}

			codeStatus.Status[color] = &components.ColorCodeStatus{
				LastAlertTime: now,
			}

			// Add code job to consolidated storage
			// Reuse cloned monitor name for all code jobs
			codeJob, err := jobs.CreateCodeJob(monitorName, config, entity, color)
			if err != nil {
				return err
			}
			if js := e.JobStorage.Get(entity); js != nil {
				js.CodeJobs[color] = codeJob
			}
		}

		// Add both code components in a single step to reduce archetype moves
		e.codePair.Add(entity, codeConfig, codeStatus)
	}

	// Apply Disabled tag after base creation if monitor is disabled
	if !monitor.Enabled {
		e.Disabled.Add(entity, &components.Disabled{})
	}

	return nil
}

// CreateEntitiesFromMonitors creates entities in batch using Ark's Map3.NewBatchFn to minimize
// archetype transitions and reduce per-entity overhead. It mirrors CreateEntityFromMonitor logic
// for each monitor without changing behavior. Any job creation error is recorded and returned
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

	// We use a captured index to provide per-monitor data to the batch callback.
	i := 0
	var firstErr error

	e.baseMapper.NewBatchFn(len(monitors), func(entity ecs.Entity, monitorState *components.MonitorState, pulseConfig *components.PulseConfig, jobStorage *components.JobStorage) {
		// If an error was already encountered, skip heavy work but still leave components initialized.
		if firstErr != nil {
			return
		}

		monitor := monitors[i]
		i++

		// Monitor name & times
		monitorName := monitor.Name
		monitorState.Name = monitorName
		monitorState.LastCheckTime = now
		monitorState.LastSuccessTime = now
		monitorState.NextCheckTime = now

		// Initial state flags or Disabled tag
		if monitor.Enabled {
			monitorState.SetPulseFirstCheck(true)
		}

		// Pulse configuration: prefer explicit unhealthy_threshold; loader fills from legacy max_failures if provided
		pulseConfig.Type = monitor.Pulse.Type
		pulseConfig.UnhealthyThreshold = monitor.Pulse.UnhealthyThreshold
		pulseConfig.HealthyThreshold = monitor.Pulse.HealthyThreshold
		pulseConfig.Timeout = monitor.Pulse.Timeout
		pulseConfig.Interval = monitor.Pulse.Interval
		if monitor.Pulse.Config != nil {
			// Assign schema config directly; future changes should replace component (COW).
			pulseConfig.Config = monitor.Pulse.Config
		}

		// Job storage: pre-size code jobs map based on number of configured colors
		codeCount := len(monitor.Codes)
		if jobStorage.CodeJobs == nil {
			jobStorage.CodeJobs = make(map[string]jobs.Job, codeCount)
		}

		// Create pulse job and attach to JobStorage
		if pj, err := jobs.CreatePulseJob(monitor.Pulse, entity); err != nil {
			firstErr = err
			return
		} else {
			jobStorage.PulseJob = pj
		}

		// Intervention configuration (optional)
		if monitor.Intervention.Action != "" {
			maxFailures := 1
			if monitor.Intervention.MaxFailures > 0 {
				maxFailures = monitor.Intervention.MaxFailures
			}
			interventionConfig := &components.InterventionConfig{
				Action:      monitor.Intervention.Action,
				Target:      nil,
				MaxFailures: maxFailures,
			}
			if monitor.Intervention.Target != nil {
				// Assign schema target directly; future changes should replace component (COW).
				interventionConfig.Target = monitor.Intervention.Target
			}
			e.InterventionConfig.Add(entity, interventionConfig)

			// Create intervention job and attach
			if ij, err := jobs.CreateInterventionJob(monitor.Intervention, entity); err != nil {
				firstErr = err
				return
			} else {
				jobStorage.InterventionJob = ij
			}
		}

		// Consolidated code configuration & status
		if codeCount > 0 {
			codeConfig := &components.CodeConfig{Configs: make(map[string]*components.ColorCodeConfig, codeCount)}
			codeStatus := &components.CodeStatus{Status: make(map[string]*components.ColorCodeStatus, codeCount)}

			for color, cfg := range monitor.Codes {
				// Per-color config
				cc := &components.ColorCodeConfig{
					Dispatch: cfg.Dispatch,
					Notify:   cfg.Notify,
				}
				if cfg.Config != nil {
					// Assign schema notification config directly; updates should replace (COW).
					cc.Config = cfg.Config
				}
				codeConfig.Configs[color] = cc

				// Per-color status
				codeStatus.Status[color] = &components.ColorCodeStatus{LastAlertTime: now}

				// Create code job and attach to JobStorage
				if cj, err := jobs.CreateCodeJob(monitorName, cfg, entity, color); err != nil {
					firstErr = err
					return
				} else {
					jobStorage.CodeJobs[color] = cj
				}
			}
			// Add both code components in a single step to reduce archetype moves
			e.codePair.Add(entity, codeConfig, codeStatus)
		}

		// Apply Disabled tag after base creation if monitor is disabled
		if !monitor.Enabled {
			e.Disabled.Add(entity, &components.Disabled{})
		}
	})

	return firstErr
}

// EnableMonitor enables a monitor using consolidated state flags
func (e *EntityManager) EnableMonitor(entity ecs.Entity) {
	// Remove Disabled tag if present and schedule first check
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
