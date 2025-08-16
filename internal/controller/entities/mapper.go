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

type EntityManager struct {
	Name            *ecs.Map1[components.Name]
	Disabled        *ecs.Map1[components.DisabledMonitor]
	MonitorStatus   *ecs.Map1[components.MonitorStatus]
	Pulse           *ecs.Map2[components.PulseConfig, components.PulseStatus]
	PulseConfig     *ecs.Map1[components.PulseConfig]
	PulseStatus     *ecs.Map1[components.PulseStatus]
	PulseNeeded     *ecs.Map1[components.PulseNeeded]
	PulsePending    *ecs.Map1[components.PulsePending]
	PulseFailed     *ecs.Map1[components.PulseFailed]
	PulseSuccess    *ecs.Map1[components.PulseSuccess]
	PulseFirstCheck *ecs.Map1[components.PulseFirstCheck]
	PulseJob        *ecs.Map1[components.PulseJob]
	// ... other mappers
	Intervention        *ecs.Map2[components.InterventionConfig, components.InterventionStatus]
	InterventionConfig  *ecs.Map1[components.InterventionConfig]
	InterventionStatus  *ecs.Map1[components.InterventionStatus]
	InterventionPending *ecs.Map1[components.InterventionPending]
	InterventionFailed  *ecs.Map1[components.InterventionFailed]
	InterventionSuccess *ecs.Map1[components.InterventionSuccess]
	InterventionNeeded  *ecs.Map1[components.InterventionNeeded]
	InterventionJob     *ecs.Map1[components.InterventionJob]
	//Code                ecs.Map2[components.CodeConfig, components.CodeStatus]
	CodeNeeded                  *ecs.Map1[components.CodeNeeded]
	CodePending                 *ecs.Map1[components.CodePending]
	CodeJob                     *ecs.Map1[components.CodeJob]
	RedCode                     *ecs.Map1[components.RedCode]
	RedCodeJob                  *ecs.Map1[components.RedCodeJob]
	RedCodeConfig               *ecs.Map1[components.RedCodeConfig]
	RedCodeStatus               *ecs.Map1[components.RedCodeStatus]
	CyanCode                    *ecs.Map1[components.CyanCode]
	CyanCodeJob                 *ecs.Map1[components.CyanCodeJob]
	CyanCodeConfig              *ecs.Map1[components.CyanCodeConfig]
	CyanCodeStatus              *ecs.Map1[components.CyanCodeStatus]
	GreenCode                   *ecs.Map1[components.GreenCode]
	GreenCodeJob                *ecs.Map1[components.GreenCodeJob]
	GreenCodeConfig             *ecs.Map1[components.GreenCodeConfig]
	GreenCodeStatus             *ecs.Map1[components.GreenCodeStatus]
	YellowCode                  *ecs.Map1[components.YellowCode]
	YellowCodeJob               *ecs.Map1[components.YellowCodeJob]
	YellowCodeConfig            *ecs.Map1[components.YellowCodeConfig]
	YellowCodeStatus            *ecs.Map1[components.YellowCodeStatus]
	GrayCode                    *ecs.Map1[components.GrayCode]
	GrayCodeJob                 *ecs.Map1[components.GrayCodeJob]
	GrayCodeConfig              *ecs.Map1[components.GrayCodeConfig]
	GrayCodeStatus              *ecs.Map1[components.GrayCodeStatus]
	PulsePendingExchange        *ecs.Exchange2[components.PulsePending, components.PulseNeeded]
	InterventionPendingExchange *ecs.Exchange2[components.InterventionPending, components.InterventionNeeded]
	CodePendingExchange         *ecs.Exchange2[components.CodePending, components.CodeNeeded]
}

// InitializeMappers creates and returns a EntityManager for a given world.
// It no longer creates the world itself.
func InitializeMappers(world *ecs.World) *EntityManager {
	return &EntityManager{
		Name:                ecs.NewMap1[components.Name](world),
		Disabled:            ecs.NewMap1[components.DisabledMonitor](world),
		MonitorStatus:       ecs.NewMap1[components.MonitorStatus](world),
		Pulse:               ecs.NewMap2[components.PulseConfig, components.PulseStatus](world),
		PulseConfig:         ecs.NewMap1[components.PulseConfig](world),
		PulseStatus:         ecs.NewMap1[components.PulseStatus](world),
		PulseNeeded:         ecs.NewMap1[components.PulseNeeded](world),
		PulsePending:        ecs.NewMap1[components.PulsePending](world),
		PulseFailed:         ecs.NewMap1[components.PulseFailed](world),
		PulseSuccess:        ecs.NewMap1[components.PulseSuccess](world),
		PulseFirstCheck:     ecs.NewMap1[components.PulseFirstCheck](world),
		PulseJob:            ecs.NewMap1[components.PulseJob](world),
		Intervention:        ecs.NewMap2[components.InterventionConfig, components.InterventionStatus](world),
		InterventionConfig:  ecs.NewMap1[components.InterventionConfig](world),
		InterventionStatus:  ecs.NewMap1[components.InterventionStatus](world),
		InterventionPending: ecs.NewMap1[components.InterventionPending](world),
		InterventionFailed:  ecs.NewMap1[components.InterventionFailed](world),
		InterventionSuccess: ecs.NewMap1[components.InterventionSuccess](world),
		InterventionNeeded:  ecs.NewMap1[components.InterventionNeeded](world),
		InterventionJob:     ecs.NewMap1[components.InterventionJob](world),
		//Code:                ecs.NewMap2[components.CodeConfig, components.CodeStatus](world),
		CodeNeeded:                  ecs.NewMap1[components.CodeNeeded](world),
		CodePending:                 ecs.NewMap1[components.CodePending](world),
		CodeJob:                     ecs.NewMap1[components.CodeJob](world),
		RedCode:                     ecs.NewMap1[components.RedCode](world),
		RedCodeJob:                  ecs.NewMap1[components.RedCodeJob](world),
		RedCodeConfig:               ecs.NewMap1[components.RedCodeConfig](world),
		RedCodeStatus:               ecs.NewMap1[components.RedCodeStatus](world),
		CyanCode:                    ecs.NewMap1[components.CyanCode](world),
		CyanCodeJob:                 ecs.NewMap1[components.CyanCodeJob](world),
		CyanCodeConfig:              ecs.NewMap1[components.CyanCodeConfig](world),
		CyanCodeStatus:              ecs.NewMap1[components.CyanCodeStatus](world),
		GreenCode:                   ecs.NewMap1[components.GreenCode](world),
		GreenCodeJob:                ecs.NewMap1[components.GreenCodeJob](world),
		GreenCodeConfig:             ecs.NewMap1[components.GreenCodeConfig](world),
		GreenCodeStatus:             ecs.NewMap1[components.GreenCodeStatus](world),
		YellowCode:                  ecs.NewMap1[components.YellowCode](world),
		YellowCodeJob:               ecs.NewMap1[components.YellowCodeJob](world),
		YellowCodeConfig:            ecs.NewMap1[components.YellowCodeConfig](world),
		YellowCodeStatus:            ecs.NewMap1[components.YellowCodeStatus](world),
		GrayCode:                    ecs.NewMap1[components.GrayCode](world),
		GrayCodeJob:                 ecs.NewMap1[components.GrayCodeJob](world),
		GrayCodeConfig:              ecs.NewMap1[components.GrayCodeConfig](world),
		GrayCodeStatus:              ecs.NewMap1[components.GrayCodeStatus](world),
		PulsePendingExchange:        ecs.NewExchange2[components.PulsePending, components.PulseNeeded](world),
		InterventionPendingExchange: ecs.NewExchange2[components.InterventionPending, components.InterventionNeeded](world),
		CodePendingExchange:         ecs.NewExchange2[components.CodePending, components.CodeNeeded](world),
	}
}

// EnableMonitor ensures a monitor is active.
func (e *EntityManager) EnableMonitor(entity ecs.Entity) {

	if e.Disabled.Get(entity) != nil {
		e.Disabled.Remove(entity)
	}
	if e.PulseFirstCheck.Get(entity) == nil {
		e.PulseFirstCheck.Add(entity, &components.PulseFirstCheck{})
	}
}

// DisableMonitor deactivates a monitor.
func (e *EntityManager) DisableMonitor(entity ecs.Entity) {
	if e.Disabled.Get(entity) == nil {
		e.Disabled.Add(entity, &components.DisabledMonitor{})
	}
	// When disabling, you might want to clear any transient pulse states:

	if e.PulsePending.HasAll(entity) {
		e.PulsePending.Remove(entity)
	}
}

// CreateEntityFromMonitor remains the same
func (e *EntityManager) CreateEntityFromMonitor(
	monitor schema.Monitor,
	world *ecs.World) error {
	entity := world.NewEntity()
	// ... (entity creation logic as before) ...
	nameComponent := components.Name(strings.Clone(monitor.Name))
	if &nameComponent == nil {
		return fmt.Errorf("Monitor has no name\n")
	}
	e.Name.Add(entity, &nameComponent)

	if monitor.Enabled {
		e.EnableMonitor(entity)
	} else {
		e.DisableMonitor(entity)
	}
	e.MonitorStatus.Add(entity, &components.MonitorStatus{
		LastCheckTime: time.Now(),
	})
	pulseCfg := components.PulseConfig{
		Type:        strings.Clone(monitor.Pulse.Type),
		MaxFailures: monitor.Pulse.MaxFailures,
		Timeout:     monitor.Pulse.Timeout,
		Interval:    monitor.Pulse.Interval,
		Config:      monitor.Pulse.Config.Copy(), // Use Copy method
	}
	pulseStatus := components.PulseStatus{
		LastCheckTime: time.Now(),
	}

	e.Pulse.Add(entity, &pulseCfg, &pulseStatus)

	j, err := jobs.CreatePulseJob(monitor.Pulse, entity)
	if err != nil {
		return err
	}
	e.PulseJob.Add(entity, &components.PulseJob{Job: j.Copy()})

	if monitor.Intervention.Action != "" {
		var maxFailures int = 1
		if monitor.Intervention.MaxFailures > 0 {
			maxFailures = monitor.Intervention.MaxFailures
		}

		interventionCfg := &components.InterventionConfig{
			Action:      strings.Clone(monitor.Intervention.Action),
			Target:      monitor.Intervention.Target.Copy(), // Use Copy method
			MaxFailures: maxFailures,
		}
		InterventionStatus := &components.InterventionStatus{
			LastInterventionTime: time.Now(),
		}
		e.Intervention.Add(entity, interventionCfg, InterventionStatus)
		j, err = jobs.CreateInterventionJob(monitor.Intervention, entity)
		if err != nil {
			return err
		}
		e.InterventionJob.Add(entity, &components.InterventionJob{Job: j.Copy()})
	}

	for color, config := range monitor.Codes {
		configCopy := config
		configCopy.Config = *new(schema.CodeNotification)
		configCopy.Config = config.Config.Copy() // Copy nested config
		switch color {

		case "red":
			e.RedCode.Add(entity, &components.RedCode{})
			CodeConfig := &components.RedCodeConfig{
				Dispatch: configCopy.Dispatch,
				Notify:   strings.Clone(configCopy.Notify),
				Config:   configCopy.Config, // Use copied config
			}

			CodeStatus := &components.RedCodeStatus{
				LastAlertTime: time.Now(),
			}
			e.RedCodeConfig.Add(entity, CodeConfig)
			e.RedCodeStatus.Add(entity, CodeStatus)
			j, err = jobs.CreateCodeJob(strings.Clone(monitor.Name), config, entity)
			if err != nil {
				return err
			}
			e.RedCodeJob.Add(entity, &components.RedCodeJob{Job: j.Copy()})
		case "green":
			e.GreenCode.Add(entity, &components.GreenCode{})
			CodeConfig := &components.GreenCodeConfig{
				Dispatch: configCopy.Dispatch,
				Notify:   strings.Clone(configCopy.Notify),
				Config:   configCopy.Config, // Use copied config
			}
			CodeStatus := &components.GreenCodeStatus{
				LastAlertTime: time.Now(),
			}
			e.GreenCodeConfig.Add(entity, CodeConfig)
			e.GreenCodeStatus.Add(entity, CodeStatus)
			j, err = jobs.CreateCodeJob(strings.Clone(monitor.Name), config, entity)
			if err != nil {
				return err
			}
			e.GreenCodeJob.Add(entity, &components.GreenCodeJob{Job: j.Copy()})
		case "yellow":
			e.YellowCode.Add(entity, &components.YellowCode{})
			CodeConfig := &components.YellowCodeConfig{
				Dispatch: configCopy.Dispatch,
				Notify:   strings.Clone(configCopy.Notify),
				Config:   configCopy.Config, // Use copied config
			}
			CodeStatus := &components.YellowCodeStatus{
				LastAlertTime: time.Now(),
			}
			e.YellowCodeConfig.Add(entity, CodeConfig)
			e.YellowCodeStatus.Add(entity, CodeStatus)
			j, err = jobs.CreateCodeJob(strings.Clone(monitor.Name), config, entity)
			if err != nil {
				return err
			}
			e.YellowCodeJob.Add(entity, &components.YellowCodeJob{Job: j.Copy()})
		case "cyan":
			e.CyanCode.Add(entity, &components.CyanCode{})
			CodeConfig := &components.CyanCodeConfig{
				Dispatch: configCopy.Dispatch,
				Notify:   strings.Clone(configCopy.Notify),
				Config:   configCopy.Config, // Use copied config
			}
			CodeStatus := &components.CyanCodeStatus{
				LastAlertTime: time.Now(),
			}
			e.CyanCodeConfig.Add(entity, CodeConfig)
			e.CyanCodeStatus.Add(entity, CodeStatus)
			j, err = jobs.CreateCodeJob(strings.Clone(monitor.Name), config, entity)
			if err != nil {
				return err
			}
			e.CyanCodeJob.Add(entity, &components.CyanCodeJob{Job: j.Copy()})
		case "gray":
			e.GrayCode.Add(entity, &components.GrayCode{})
			CodeConfig := &components.GrayCodeConfig{
				Dispatch: configCopy.Dispatch,
				Notify:   strings.Clone(configCopy.Notify),
				Config:   configCopy.Config, // Use copied config
			}
			CodeStatus := &components.GrayCodeStatus{
				LastAlertTime: time.Now(),
			}
			e.GrayCodeConfig.Add(entity, CodeConfig)
			e.GrayCodeStatus.Add(entity, CodeStatus)
			j, err = jobs.CreateCodeJob(strings.Clone(monitor.Name), config, entity)
			if err != nil {
				return err
			}
			e.GrayCodeJob.Add(entity, &components.GrayCodeJob{Job: j.Copy()})
		}
	}

	return nil
}
