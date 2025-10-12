package entities

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"fmt"
	"strings"
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

	entity := world.NewEntity()
	if !world.Alive(entity) {
		return fmt.Errorf("failed to create valid entity")
	}

	// Create consolidated MonitorState component
	monitorState := &components.MonitorState{
		Name:            strings.Clone(monitor.Name),
		LastCheckTime:   time.Now(),
		LastSuccessTime: time.Now(),
		NextCheckTime:   time.Now(),
	}

	// Set initial state flags
	if monitor.Enabled {
		monitorState.SetPulseFirstCheck(true) // Equivalent to adding PulseFirstCheck component
	} else {
		monitorState.SetDisabled(true) // Equivalent to adding DisabledMonitor component
	}

	e.MonitorState.Add(entity, monitorState)

	// Add pulse configuration
    // Map thresholds: unhealthy from either explicit unhealthy_threshold or legacy max_failures
    unhealthy := monitor.Pulse.UnhealthyThreshold
    if unhealthy <= 0 {
        unhealthy = monitor.Pulse.MaxFailures
    }
    pulseConfig := &components.PulseConfig{
        Type:               strings.Clone(monitor.Pulse.Type),
        UnhealthyThreshold: unhealthy,
        HealthyThreshold:   monitor.Pulse.HealthyThreshold,
        Timeout:            monitor.Pulse.Timeout,
        Interval:           monitor.Pulse.Interval,
        Config:             monitor.Pulse.Config.Copy(),
    }
	e.PulseConfig.Add(entity, pulseConfig)

	// Create consolidated job storage
	jobStorage := &components.JobStorage{
		CodeJobs: make(map[string]jobs.Job),
	}

	// Add pulse job
	pulseJob, err := jobs.CreatePulseJob(monitor.Pulse, entity)
	if err != nil {
		return err
	}
	jobStorage.PulseJob = pulseJob

	// Add intervention if configured
	if monitor.Intervention.Action != "" {
		maxFailures := 1
		if monitor.Intervention.MaxFailures > 0 {
			maxFailures = monitor.Intervention.MaxFailures
		}

		interventionConfig := &components.InterventionConfig{
			Action:      strings.Clone(monitor.Intervention.Action),
			Target:      monitor.Intervention.Target.Copy(),
			MaxFailures: maxFailures,
		}
		e.InterventionConfig.Add(entity, interventionConfig)

		// Add intervention job
		interventionJob, err := jobs.CreateInterventionJob(monitor.Intervention, entity)
		if err != nil {
			return err
		}
		jobStorage.InterventionJob = interventionJob
	}

	// Add consolidated code configuration instead of separate color components
	if len(monitor.Codes) > 0 {
		codeConfig := &components.CodeConfig{
			Configs: make(map[string]*components.ColorCodeConfig),
		}
		codeStatus := &components.CodeStatus{
			Status: make(map[string]*components.ColorCodeStatus),
		}

		for color, config := range monitor.Codes {
			// Single consolidated entry instead of separate components
			codeConfig.Configs[color] = &components.ColorCodeConfig{
				Dispatch: config.Dispatch,
				Notify:   strings.Clone(config.Notify),
				Config:   config.Config.Copy(),
			}

			codeStatus.Status[color] = &components.ColorCodeStatus{
				LastAlertTime: time.Now(),
			}

			// Add code job to consolidated storage
			codeJob, err := jobs.CreateCodeJob(strings.Clone(monitor.Name), config, entity, color)
			if err != nil {
				return err
			}
			jobStorage.CodeJobs[color] = codeJob
		}

		e.CodeConfig.Add(entity, codeConfig)
		e.CodeStatus.Add(entity, codeStatus)
	}

	e.JobStorage.Add(entity, jobStorage)

	return nil
}

// EnableMonitor enables a monitor using consolidated state flags
func (e *EntityManager) EnableMonitor(entity ecs.Entity) {
	if state := e.MonitorState.Get(entity); state != nil {
		state.SetDisabled(false)
		state.SetPulseFirstCheck(true)
	}
}

// DisableMonitor disables a monitor using consolidated state flags
func (e *EntityManager) DisableMonitor(entity ecs.Entity) {
	if state := e.MonitorState.Get(entity); state != nil {
		state.SetDisabled(true)
		state.SetPulsePending(false)
		state.SetInterventionPending(false)
		state.SetCodePending(false)
	}
}

// GetMonitorState provides easy access to consolidated state
func (e *EntityManager) GetMonitorState(entity ecs.Entity) *components.MonitorState {
	return e.MonitorState.Get(entity)
}
