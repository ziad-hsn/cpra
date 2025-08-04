package controller

import (
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"github.com/mlange-42/arche/ecs"
	"time"
)

// MonitorAdapter provides a clean, domain-specific API for a single monitor entity.
// It acts as a facade, hiding the underlying ECS component interactions.
type MonitorAdapter struct {
	entity  ecs.Entity
	world   *ecs.World
	mappers *entities.EntityManager
}

// NewMonitorAdapter creates a new adapter for a given entity.
func NewMonitorAdapter(w *CPRaWorld, entity ecs.Entity) MonitorAdapter {
	return MonitorAdapter{
		entity:  entity,
		world:   w.Mappers.World,
		mappers: w.Mappers,
	}
}

// IsAlive checks if the underlying entity still exists.
func (m *MonitorAdapter) IsAlive() bool {
	if m.world.Stats().Entities.Total < int(m.entity.ID()) {
		return false
	}
	return m.world.Alive(m.entity)

}

// Name returns the monitor's name.
func (m *MonitorAdapter) Name() string {
	if !m.IsAlive() {
		return ""
	}
	// The Get method returns a pointer, so we dereference it.
	return string(*m.mappers.Name.Get(m.entity))
}

// Status returns the current pulse status of the monitor.
func (m *MonitorAdapter) Status() (components.PulseStatus, bool) {
	if !m.IsAlive() {
		return components.PulseStatus{}, false
	}
	status := *m.mappers.PulseStatus.Get(m.entity).Copy()
	if status == (components.PulseStatus{}) {
		return components.PulseStatus{}, false
	}
	return status, true
}

// SetPulseStatusAsFailed updates the monitor's pulse status to failed.
// This shows how data modifications are simplified.
func (m *MonitorAdapter) SetPulseStatusAsFailed(err error) {
	if status := m.mappers.PulseStatus.Get(m.entity).Copy(); status != nil {
		status.LastStatus = "failed"
		status.LastError = err
		status.ConsecutiveFailures++
	}
	if monitorStatus := m.mappers.MonitorStatus.Get(m.entity).Copy(); monitorStatus != nil {
		monitorStatus.Status = "failed"
	}
}

// SetPulseStatusAsSuccess updates the monitor's pulse status to success.
func (m *MonitorAdapter) SetPulseStatusAsSuccess() {
	// ... implementation similar to SetPulseStatusAsFailed ...
	if status := m.mappers.PulseStatus.Get(m.entity).Copy(); status != nil {
		status.LastStatus = "success"
		status.LastError = nil
		status.ConsecutiveFailures = 0
		status.LastSuccessTime = time.Now()
	}
	if monitorStatus := m.mappers.MonitorStatus.Get(m.entity).Copy(); monitorStatus != nil {
		monitorStatus.Status = "success"
	}
}

func (m *MonitorAdapter) SetInterventionStatusAsFailed(err error) {
	if status := m.mappers.InterventionStatus.Get(m.entity).Copy(); status != nil {
		status.LastStatus = "failed"
		status.LastError = err
		status.ConsecutiveFailures++
	}
	if monitorStatus := m.mappers.MonitorStatus.Get(m.entity).Copy(); monitorStatus != nil {
		monitorStatus.Status = "failed"
	}
}

// SetPulseStatusAsSuccess updates the monitor's pulse status to success.
func (m *MonitorAdapter) SetInterventionStatusAsSuccess() string {
	// ... implementation similar to SetPulseStatusAsFailed ...
	var lastStatus string
	if status := m.mappers.InterventionStatus.Get(m.entity).Copy(); status != nil {
		lastStatus = status.LastStatus
		status.LastStatus = "success"
		status.LastError = nil
		status.ConsecutiveFailures = 0
		status.LastSuccessTime = time.Now()
	}
	if monitorStatus := m.mappers.MonitorStatus.Get(m.entity).Copy(); monitorStatus != nil {
		monitorStatus.Status = "success"
	}
	return lastStatus
}

// ScheduleCode sends a request to trigger a code notification.
// This encapsulates the logic of adding the CodeNeeded component.
func (m *MonitorAdapter) ScheduleCode(color string) {
	// Avoid scheduling if one is already pending.
	if m.mappers.World.Has(m.entity, ecs.ComponentID[components.CodeNeeded](m.world)) {
		return
	}
	m.mappers.CodeNeeded.Assign(m.entity, &components.CodeNeeded{Color: color})
}

// ScheduleIntervention sends a request to trigger an intervention.
func (m *MonitorAdapter) ScheduleIntervention() {
	if !m.world.Has(m.entity, ecs.ComponentID[components.InterventionNeeded](m.world)) {
		m.mappers.InterventionNeeded.Assign(m.entity, &components.InterventionNeeded{})
	}
}

// HasIntervention checks if the monitor is configured for interventions.
func (m *MonitorAdapter) HasIntervention() bool {
	return m.world.Has(m.entity, ecs.ComponentID[components.InterventionConfig](m.world))
}

// RemovePendingPulse removes the PulsePending component. A simple structural change.
func (m *MonitorAdapter) RemovePendingPulse() {
	if m.world.Has(m.entity, ecs.ComponentID[components.PulsePending](m.world)) {
		m.mappers.PulsePending.Remove(m.entity)
	}
}

func (m *MonitorAdapter) RemovePendingIntervention() {
	if m.world.Has(m.entity, ecs.ComponentID[components.InterventionPending](m.world)) {
		m.mappers.InterventionPending.Remove(m.entity)
	}
}

func (m *MonitorAdapter) RemovePendingCode() {
	if m.world.Has(m.entity, ecs.ComponentID[components.CodePending](m.world)) {
		m.mappers.CodePending.Remove(m.entity)
	}
}
