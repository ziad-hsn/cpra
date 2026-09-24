package entities

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

// EntityManager uses the new consolidated component design.
// State changes use flags without adding or removing component types.
type EntityManager struct {
	world      *ecs.World
	identities map[string]ecs.Entity
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
	Endpoints          map[string]manifest.Endpoint
	NotificationGroups manifest.NotificationGroups
}

// NewEntityManager creates a new consolidated entity manager.
func NewEntityManager(world *ecs.World) *EntityManager {
	return &EntityManager{
		world:              world,
		identities:         make(map[string]ecs.Entity),
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

// Install transfers one private preparation into this manager's world. The owner
// must close every Ark query before calling it. Rejected preparations remain
// reusable until explicitly closed; a successful transfer can never be reused.
func (e *EntityManager) Install(p *PreparedMonitor, world *ecs.World) (ecs.Entity, error) {
	if err := e.checkWorld(world); err != nil {
		return ecs.Entity{}, err
	}
	if p == nil {
		return ecs.Entity{}, fmt.Errorf("prepared monitor is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bundle == nil {
		return ecs.Entity{}, fmt.Errorf("prepared monitor is closed or already installed")
	}
	if _, exists := e.identities[p.id]; exists {
		return ecs.Entity{}, fmt.Errorf("duplicate effective monitor id %q", p.id)
	}
	b := p.bundle
	now := time.Now()
	state := &components.MonitorState{Name: b.name, MonitorID: p.id, Revision: p.revision, Maintenance: b.maintenance, NextCheckTime: now}
	state.SetPulseFirstCheck(b.enabled)
	entity := e.baseMapper.NewEntity(state, &b.pulse, &components.JobStorage{})
	if b.intervention != nil {
		e.InterventionConfig.Add(entity, b.intervention)
	}
	if b.codes != nil {
		e.codePair.Add(entity, b.codes, initialCodeStatus(b.codes, nil, now))
	}
	if !b.enabled {
		e.Disabled.Add(entity, &components.Disabled{})
	}
	// All structural changes are finished before obtaining any component pointer.
	storage := bindJobs(b.jobs, entity)
	e.JobStorage.Set(entity, &storage)
	e.identities[p.id] = entity
	p.bundle = nil
	return entity, nil
}

// Replace updates the same live incarnation without resetting incident state,
// generations, cooldowns or the Disabled control tag. The owner must fence and
// cancel old scheduled work before replacement and project authoritative durable
// state before admitting new work. No provider execution occurs here.
func (e *EntityManager) Replace(expected ecs.Entity, p *PreparedMonitor, world *ecs.World) error {
	if err := e.checkEntity(expected, world); err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("prepared monitor is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bundle == nil {
		return fmt.Errorf("prepared monitor is closed or already installed")
	}
	if e.MonitorState.Get(expected).MonitorID != p.id {
		return fmt.Errorf("replacement must preserve monitor identity")
	}
	b := p.bundle
	// Copy dynamic state before any archetype moves invalidate component pointers.
	state := *e.MonitorState.Get(expected)
	state.Name, state.Revision, state.Maintenance = b.name, p.revision, b.maintenance
	var status *components.CodeStatus
	if b.codes != nil {
		status = initialCodeStatus(b.codes, e.CodeStatus.Get(expected), time.Now())
	}
	if b.intervention != nil {
		if e.InterventionConfig.HasAll(expected) {
			e.InterventionConfig.Set(expected, b.intervention)
		} else {
			e.InterventionConfig.Add(expected, b.intervention)
		}
	} else if e.InterventionConfig.HasAll(expected) {
		e.InterventionConfig.Remove(expected)
	}
	if b.codes != nil {
		if e.codePair.HasAll(expected) {
			e.codePair.Set(expected, b.codes, status)
		} else {
			e.codePair.Add(expected, b.codes, status)
		}
	} else if e.codePair.HasAll(expected) {
		e.codePair.Remove(expected)
	}
	storage := bindJobs(b.jobs, expected)
	e.MonitorState.Set(expected, &state)
	e.PulseConfig.Set(expected, &b.pulse)
	e.JobStorage.Set(expected, &storage)
	p.bundle = nil
	return nil
}

// Remove unregisters the full entity incarnation. The owner is responsible for
// canceling its accepted work before removal; stale results must still be fenced.
func (e *EntityManager) Remove(expected ecs.Entity, world *ecs.World) error {
	if err := e.checkEntity(expected, world); err != nil {
		return err
	}
	id := e.MonitorState.Get(expected).MonitorID
	world.RemoveEntity(expected)
	delete(e.identities, id)
	return nil
}

// Lookup returns the current full Ark entity, including its incarnation. It is
// an owner-loop operation and must not run concurrently with world mutation.
func (e *EntityManager) Lookup(id string) (ecs.Entity, bool) {
	if e == nil || e.world == nil {
		return ecs.Entity{}, false
	}
	entity, ok := e.identities[id]
	return entity, ok && e.world.Alive(entity)
}

func (e *EntityManager) checkWorld(world *ecs.World) error {
	if e == nil {
		return fmt.Errorf("EntityManager cannot be nil")
	}
	if world == nil {
		return fmt.Errorf("world cannot be nil")
	}
	if e.world != world {
		return fmt.Errorf("world does not belong to EntityManager")
	}
	if world.IsLocked() {
		return fmt.Errorf("close all Ark queries before changing monitor entities")
	}
	return nil
}
func (e *EntityManager) checkEntity(entity ecs.Entity, world *ecs.World) error {
	if err := e.checkWorld(world); err != nil {
		return err
	}
	if !world.Alive(entity) {
		return fmt.Errorf("monitor entity is no longer alive")
	}
	state := e.MonitorState.Get(entity)
	if state == nil {
		return fmt.Errorf("entity is not a managed monitor")
	}
	if current, ok := e.identities[state.MonitorID]; !ok || current != entity {
		return fmt.Errorf("monitor entity identity does not match its registration")
	}
	return nil
}

// CreateEntityFromMonitor preserves the legacy loading entry point while ensuring
// a constructor failure cannot allocate an entity or reserve a monitor identity.
func (e *EntityManager) CreateEntityFromMonitor(monitor *manifest.Monitor, world *ecs.World) error {
	if err := e.checkWorld(world); err != nil {
		return err
	}
	if monitor == nil {
		return fmt.Errorf("monitor cannot be nil")
	}
	p, err := PrepareMonitor(*monitor, e.Endpoints, e.NotificationGroups)
	if err != nil {
		return err
	}
	defer p.Close()
	_, err = e.Install(p, world)
	return err
}

// CreateEntitiesFromMonitors creates monitors sequentially. Ark holds a world
// lock inside batch initializers, which cannot add optional component types.
func (e *EntityManager) CreateEntitiesFromMonitors(world *ecs.World, monitors []manifest.Monitor) error {
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
	if e.Disabled.HasAll(entity) {
		e.Disabled.Remove(entity)
	}
	if state := e.MonitorState.Get(entity); state != nil {
		state.SetPulseFirstCheck(true)
	}
}

// DisableMonitor disables a monitor using consolidated state flags
func (e *EntityManager) DisableMonitor(entity ecs.Entity) {
	// Add Disabled tag and clear pending flags
	if !e.Disabled.HasAll(entity) {
		e.Disabled.Add(entity, &components.Disabled{})
	}
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
