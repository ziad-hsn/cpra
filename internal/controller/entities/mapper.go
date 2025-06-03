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
	Pulse           generic.Map2[components.PulseConfig, components.PulseStatus]
	PulsePending    generic.Map1[components.PulsePending]
	PulseFailed     generic.Map1[components.PulseFailed]
	PulseSuccess    generic.Map1[components.PulseSuccess]
	PulseFirstCheck generic.Map1[components.PulseFirstCheck]
	PulseJob        generic.Map1[components.PulseJob]
	// ... other mappers
}

// InitializeMappers creates and returns a EntityManager for a given world.
// It no longer creates the world itself.
func InitializeMappers(world *ecs.World) EntityManager {
	return EntityManager{
		World:           world,
		Name:            generic.NewMap1[components.Name](world),
		Disabled:        generic.NewMap1[components.DisabledMonitor](world),
		Pulse:           generic.NewMap2[components.PulseConfig, components.PulseStatus](world),
		PulsePending:    generic.NewMap1[components.PulsePending](world),
		PulseFailed:     generic.NewMap1[components.PulseFailed](world),
		PulseSuccess:    generic.NewMap1[components.PulseSuccess](world),
		PulseFirstCheck: generic.NewMap1[components.PulseFirstCheck](world),
		PulseJob:        generic.NewMap1[components.PulseJob](world),
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

	// Optionally update PulseStatus component
	if _, status := e.Pulse.Get(entity); status != nil { // status is *components.PulseStatus
		status.LastStatus = "Pending"
		status.LastCheckTime = time.Now()
		status.LastError = nil
	}
	return nil
}

// MarkAsSuccess records a successful pulse check.
func (e *EntityManager) MarkAsSuccess(entity ecs.Entity, resultDetails string) error { // resultDetails could be richer, e.g. *jobs.PulseResult
	if !e.World.Alive(entity) {
		return fmt.Errorf("Entity is not found in the world: %v.\n", entity)
	}

	if e.PulseSuccess.Get(entity) == nil {
		e.PulseSuccess.Add(entity)
	}
	if e.PulsePending.Get(entity) != nil {
		e.PulsePending.Remove(entity)
	}
	if e.PulseFailed.Get(entity) != nil {
		e.PulseFailed.Remove(entity)
	}

	if _, status := e.Pulse.Get(entity); status != nil {
		status.LastStatus = "Success"
		status.LastSuccessTime = time.Now()
		status.LastCheckTime = time.Now()
		status.ConsecutiveFailures = 0
		status.LastError = nil // Clear last error
		// You might store parts of resultDetails in PulseStatus
	}
	return nil
}

// MarkAsFailed records a failed pulse check.
func (e *EntityManager) MarkAsFailed(entity ecs.Entity, errMessage error) error { // errMessage could be richer, e.g. error type
	if !e.World.Alive(entity) {
		return fmt.Errorf("Entity is not found in the world: %v.\n", entity)
	}

	if e.PulseFailed.Get(entity) == nil {
		e.PulseFailed.Add(entity)
	}
	if e.PulsePending.Get(entity) != nil {
		e.PulsePending.Remove(entity)
	}

	if e.PulseSuccess.Get(entity) != nil {
		e.PulseSuccess.Remove(entity)
	}
	if _, status := e.Pulse.Get(entity); status != nil {
		status.LastStatus = "Failed"
		status.LastCheckTime = time.Now()
		status.ConsecutiveFailures++
		status.LastError = errMessage
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
	// You might also want to reset its PulseStatus here or clear pending/failed/success states.
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

	// Or update PulseStatus to indicate it's disabled.
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

	pulseCfg := components.PulseConfig{
		Type:        monitor.Pulse.Type,
		MaxFailures: monitor.Pulse.MaxFailures,
		Timeout:     monitor.Pulse.Timeout,
	}
	pulseStatus := components.PulseStatus{}

	e.Pulse.Assign(entity, &pulseCfg, &pulseStatus)

	j, err := jobs.CreatePulseJob(monitor.Pulse, entity.ID())
	if err != nil {
		return err
	}
	e.PulseJob.Assign(entity, &components.PulseJob{Job: j})
	return nil
}
