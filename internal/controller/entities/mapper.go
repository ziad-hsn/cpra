package entities

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"time"
)

type EntityManager struct {
	World           *ecs.World
	Name            generic.Map1[components.Name]
	Disabled        generic.Map1[components.DisabledMonitor]
	MonitorStatus   generic.Map1[components.MonitorStatus]
	Pulse           generic.Map2[components.PulseConfig, components.PulseStatus]
	PulseConfig     generic.Map1[components.PulseConfig]
	PulseStatus     generic.Map1[components.PulseStatus]
	PulseNeeded     generic.Map1[components.PulseNeeded]
	PulsePending    generic.Map1[components.PulsePending]
	PulseFailed     generic.Map1[components.PulseFailed]
	PulseSuccess    generic.Map1[components.PulseSuccess]
	PulseFirstCheck generic.Map1[components.PulseFirstCheck]
	PulseJob        generic.Map1[components.PulseJob]
	// ... other mappers
	Intervention        generic.Map2[components.InterventionConfig, components.InterventionStatus]
	InterventionPending generic.Map1[components.InterventionPending]
	InterventionFailed  generic.Map1[components.InterventionFailed]
	InterventionSuccess generic.Map1[components.InterventionSuccess]
	InterventionNeeded  generic.Map1[components.InterventionNeeded]
	InterventionJob     generic.Map1[components.InterventionJob]
	Code                generic.Map2[components.CodeConfig, components.CodeStatus]
	CodeNeeded          generic.Map1[components.CodeNeeded]
	CodePending         generic.Map1[components.CodePending]
	CodeJob             generic.Map1[components.CodeJob]
	RedCode             generic.Map1[components.RedCode]
	RedCodeJob          generic.Map1[components.RedCodeJob]
	RedCodeConfig       generic.Map2[components.RedCodeConfig, components.RedCodeStatus]
	CyanCode            generic.Map1[components.CyanCode]
	CyanCodeJob         generic.Map1[components.CyanCodeJob]
	CyanCodeConfig      generic.Map2[components.CyanCodeConfig, components.CyanCodeStatus]
	GreenCode           generic.Map1[components.GreenCode]
	GreenCodeJob        generic.Map1[components.GreenCodeJob]
	GreenCodeConfig     generic.Map2[components.GreenCodeConfig, components.GreenCodeStatus]
	YellowCode          generic.Map1[components.YellowCode]
	YellowCodeJob       generic.Map1[components.YellowCodeJob]
	YellowCodeConfig    generic.Map2[components.YellowCodeConfig, components.YellowCodeStatus]
	GrayCode            generic.Map1[components.GrayCode]
	GrayCodeJob         generic.Map1[components.GrayCodeJob]
	GrayCodeConfig      generic.Map2[components.GrayCodeConfig, components.GrayCodeStatus]
}

// InitializeMappers creates and returns a EntityManager for a given world.
// It no longer creates the world itself.
func InitializeMappers(world *ecs.World) EntityManager {
	return EntityManager{
		World:               world,
		Name:                generic.NewMap1[components.Name](world),
		Disabled:            generic.NewMap1[components.DisabledMonitor](world),
		MonitorStatus:       generic.NewMap1[components.MonitorStatus](world),
		Pulse:               generic.NewMap2[components.PulseConfig, components.PulseStatus](world),
		PulseConfig:         generic.NewMap1[components.PulseConfig](world),
		PulseStatus:         generic.NewMap1[components.PulseStatus](world),
		PulseNeeded:         generic.NewMap1[components.PulseNeeded](world),
		PulsePending:        generic.NewMap1[components.PulsePending](world),
		PulseFailed:         generic.NewMap1[components.PulseFailed](world),
		PulseSuccess:        generic.NewMap1[components.PulseSuccess](world),
		PulseFirstCheck:     generic.NewMap1[components.PulseFirstCheck](world),
		PulseJob:            generic.NewMap1[components.PulseJob](world),
		Intervention:        generic.NewMap2[components.InterventionConfig, components.InterventionStatus](world),
		InterventionPending: generic.NewMap1[components.InterventionPending](world),
		InterventionFailed:  generic.NewMap1[components.InterventionFailed](world),
		InterventionSuccess: generic.NewMap1[components.InterventionSuccess](world),
		InterventionNeeded:  generic.NewMap1[components.InterventionNeeded](world),
		InterventionJob:     generic.NewMap1[components.InterventionJob](world),
		Code:                generic.NewMap2[components.CodeConfig, components.CodeStatus](world),
		CodeNeeded:          generic.NewMap1[components.CodeNeeded](world),
		CodePending:         generic.NewMap1[components.CodePending](world),
		CodeJob:             generic.NewMap1[components.CodeJob](world),
		RedCode:             generic.NewMap1[components.RedCode](world),
		RedCodeJob:          generic.NewMap1[components.RedCodeJob](world),
		RedCodeConfig:       generic.NewMap2[components.RedCodeConfig, components.RedCodeStatus](world),
		CyanCode:            generic.NewMap1[components.CyanCode](world),
		CyanCodeJob:         generic.NewMap1[components.CyanCodeJob](world),
		CyanCodeConfig:      generic.NewMap2[components.CyanCodeConfig, components.CyanCodeStatus](world),
		GreenCode:           generic.NewMap1[components.GreenCode](world),
		GreenCodeJob:        generic.NewMap1[components.GreenCodeJob](world),
		GreenCodeConfig:     generic.NewMap2[components.GreenCodeConfig, components.GreenCodeStatus](world),
		YellowCode:          generic.NewMap1[components.YellowCode](world),
		YellowCodeJob:       generic.NewMap1[components.YellowCodeJob](world),
		YellowCodeConfig:    generic.NewMap2[components.YellowCodeConfig, components.YellowCodeStatus](world),
		GrayCode:            generic.NewMap1[components.GrayCode](world),
		GrayCodeJob:         generic.NewMap1[components.GrayCodeJob](world),
		GrayCodeConfig:      generic.NewMap2[components.GrayCodeConfig, components.GrayCodeStatus](world),
	}
}

// MarkAsPending sets the entity to a 'pending' state for a pulse check.
// It ensures other conflicting status components are removed.
func (e *EntityManager) MarkAsPending(entity ecs.Entity) error {
	if !e.World.Alive(entity) {
		return fmt.Errorf("Entity is not found in the world: %v.\n", entity)
	}

	if e.PulsePending.Get(entity) == nil {
		e.PulsePending.Add(entity)
	}

	if e.PulseSuccess.Get(entity) != nil { // Check if component exists by trying to Get it
		e.PulseSuccess.Remove(entity)
	}
	if e.PulseFailed.Get(entity) != nil {
		e.PulseFailed.Remove(entity)
	}

	// Optionally update InterventionStatus component
	if _, status := e.Pulse.Get(entity); status != nil { // status is *components.InterventionStatus
		status.LastStatus = "Pending"
		status.LastCheckTime = time.Now()
		status.LastError = nil
	}
	return nil
}

// EnableMonitor ensures a monitor is active.
func (e *EntityManager) EnableMonitor(entity ecs.Entity) {

	if e.Disabled.Get(entity) != nil {
		e.Disabled.Remove(entity)
	}
	if e.PulseFirstCheck.Get(entity) == nil {
		e.PulseFirstCheck.Add(entity)
	}
	// Optionally, if enabling a monitor should trigger an immediate check:
	// c.Mappers.PulseFirstCheck.GetOrAdd(entity) // Or c.Mappers.PulseFirstCheck.Add(entity)
	// You might also want to reset its InterventionStatus here or clear pending/failed/success states.
	// For instance, when re-enabling, it might go into a "Pending" state for its first check.
	// c.MarkAsPending(entity) // If re-enabling implies it needs an immediate check.
}

// DisableMonitor deactivates a monitor.
func (e *EntityManager) DisableMonitor(entity ecs.Entity) {
	if e.Disabled.Get(entity) == nil {
		e.Disabled.Add(entity)
	}
	if e.Disabled.Get(entity) == nil {
		e.Disabled.Add(entity)
	}
	// When disabling, you might want to clear any transient pulse states:

	if e.PulsePending.Get(entity) != nil {
		e.PulsePending.Remove(entity)
	}

	if e.PulseFailed.Get(entity) != nil {
		e.PulseFailed.Remove(entity)
	}

	if e.PulseSuccess.Get(entity) != nil {
		e.PulseSuccess.Remove(entity)
	}

	// Or update InterventionStatus to indicate it's disabled.
	if _, status := e.Pulse.Get(entity); status != nil {
		status.LastStatus = "Disabled"
	}
}

// CreateEntityFromMonitor remains the same
func (e *EntityManager) CreateEntityFromMonitor(
	monitor *schema.Monitor,
) error {
	entity := e.World.NewEntity()
	// ... (entity creation logic as before) ...
	nameComponent := components.Name(monitor.Name)
	e.Name.Assign(entity, &nameComponent)

	if monitor.Enabled {
		e.EnableMonitor(entity)
	} else {
		e.DisableMonitor(entity)
	}
	e.MonitorStatus.Assign(entity, &components.MonitorStatus{})
	pulseCfg := components.PulseConfig{
		Type:        monitor.Pulse.Type,
		MaxFailures: monitor.Pulse.MaxFailures,
		Timeout:     monitor.Pulse.Timeout,
		Interval:    monitor.Pulse.Interval,
	}
	pulseStatus := components.PulseStatus{}

	e.Pulse.Assign(entity, &pulseCfg, &pulseStatus)

	j, err := jobs.CreatePulseJob(monitor.Pulse, entity)
	if err != nil {
		return err
	}
	e.PulseJob.Assign(entity, &components.PulseJob{Job: j})

	if monitor.Intervention.Action != "" {
		interventionCfg := &components.InterventionConfig{
			Action: monitor.Intervention.Action,
		}
		InterventionStatus := &components.InterventionStatus{}
		e.Intervention.Assign(entity, interventionCfg, InterventionStatus)
		j, err = jobs.CreateInterventionJob(monitor.Intervention, entity)
		if err != nil {
			return err
		}
		e.InterventionJob.Assign(entity, &components.InterventionJob{Job: j})
	}

	for color, config := range monitor.Codes {
		switch color {

		case "red":
			e.RedCode.Assign(entity, &components.RedCode{})
			CodeConfig := &components.RedCodeConfig{Dispatch: config.Dispatch, Notify: config.Notify, Config: config.Config}

			CodeStatus := &components.RedCodeStatus{}
			e.RedCodeConfig.Assign(entity, CodeConfig, CodeStatus)
			j, err = jobs.CreateCodeJob(monitor.Name, config, entity)
			if err != nil {
				return err
			}
			e.RedCodeJob.Assign(entity, &components.RedCodeJob{Job: j})
		case "green":
			e.GreenCode.Assign(entity, &components.GreenCode{})
			CodeConfig := &components.GreenCodeConfig{Dispatch: config.Dispatch, Notify: config.Notify, Config: config.Config}

			CodeStatus := &components.GreenCodeStatus{}
			e.GreenCodeConfig.Assign(entity, CodeConfig, CodeStatus)
			j, err = jobs.CreateCodeJob(monitor.Name, config, entity)
			if err != nil {
				return err
			}
			e.GreenCodeJob.Assign(entity, &components.GreenCodeJob{Job: j})
		case "yellow":
			e.YellowCode.Assign(entity, &components.YellowCode{})
			CodeConfig := &components.YellowCodeConfig{Dispatch: config.Dispatch, Notify: config.Notify, Config: config.Config}

			CodeStatus := &components.YellowCodeStatus{}
			e.YellowCodeConfig.Assign(entity, CodeConfig, CodeStatus)
			j, err = jobs.CreateCodeJob(monitor.Name, config, entity)
			if err != nil {
				return err
			}
			e.YellowCodeJob.Assign(entity, &components.YellowCodeJob{Job: j})
		case "cyan":
			e.CyanCode.Assign(entity, &components.CyanCode{})
			CodeConfig := &components.CyanCodeConfig{Dispatch: config.Dispatch, Notify: config.Notify, Config: config.Config}

			CodeStatus := &components.CyanCodeStatus{}
			e.CyanCodeConfig.Assign(entity, CodeConfig, CodeStatus)
			j, err = jobs.CreateCodeJob(monitor.Name, config, entity)
			if err != nil {
				return err
			}
			e.CyanCodeJob.Assign(entity, &components.CyanCodeJob{Job: j})
		case "gray":
			e.GrayCode.Assign(entity, &components.GrayCode{})
			CodeConfig := &components.GrayCodeConfig{Dispatch: config.Dispatch, Notify: config.Notify, Config: config.Config}

			CodeStatus := &components.GrayCodeStatus{}
			e.GrayCodeConfig.Assign(entity, CodeConfig, CodeStatus)
			j, err = jobs.CreateCodeJob(monitor.Name, config, entity)
			if err != nil {
				return err
			}
			e.GrayCodeJob.Assign(entity, &components.GrayCodeJob{Job: j})
		}
	}

	return nil
}
