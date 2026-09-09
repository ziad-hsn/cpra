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
// State changes use flags without adding or removing component types.
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

	// Notification context for resolving notify_group references to endpoints.
	Endpoints          map[string]schema.Endpoint
	NotificationGroups schema.NotificationGroups
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

	windows, err := schema.CompileMaintenance(monitor.Maintenance)
	if err != nil {
		return err
	}

	// Single time snapshot reused to avoid multiple now() calls
	now := time.Now()

	// Create consolidated MonitorState component
	monitorName := monitor.Name
	monitorState := &components.MonitorState{
		Name:            monitorName,
		Maintenance:     windows,
		LastCheckTime:   time.Time{},
		LastSuccessTime: time.Time{},
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
	jobStorage := &components.JobStorage{CodeJobs: make(map[string][]jobs.Job, codeCount)}

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

			// Add code jobs to consolidated storage (one per delivery endpoint)
			codeJobs, err := jobs.CreateCodeJobs(monitorName, config, entity, color, e.Endpoints, e.NotificationGroups)
			if err != nil {
				return err
			}
			if js := e.JobStorage.Get(entity); js != nil {
				js.CodeJobs[color] = codeJobs
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

// CreateEntitiesFromMonitors creates monitors sequentially. Ark holds a world
// lock inside batch initializers, which cannot add optional component types.
func (e *EntityManager) CreateEntitiesFromMonitors(world *ecs.World, monitors []schema.Monitor) error {
	if world == nil {
		return fmt.Errorf("world cannot be nil")
	}
	if e == nil {
		return fmt.Errorf("EntityManager cannot be nil")
	}
	for i := range monitors {
		if err := e.CreateEntityFromMonitor(&monitors[i], world); err != nil {
			return err
		}
	}
	return nil
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
