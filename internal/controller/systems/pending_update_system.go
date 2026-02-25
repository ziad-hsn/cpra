package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"

	"github.com/mlange-42/ark/ecs"
)

// PendingUpdateSystem applies deferred monitor updates once it is safe.
type PendingUpdateSystem struct {
	world  *ecs.World
	mapper *entities.EntityManager
	logger Logger
	filter *ecs.Filter2[components.MonitorState, components.PendingUpdate]
}

// NewPendingUpdateSystem creates a PendingUpdateSystem.
func NewPendingUpdateSystem(world *ecs.World, mapper *entities.EntityManager, logger Logger) *PendingUpdateSystem {
	return &PendingUpdateSystem{
		world:  world,
		mapper: mapper,
		logger: logger,
		filter: ecs.NewFilter2[components.MonitorState, components.PendingUpdate](world),
	}
}

func (s *PendingUpdateSystem) Initialize(_ *ecs.World) {
	if s.filter != nil {
		s.filter.Register()
	}
}

func (s *PendingUpdateSystem) Update(_ *ecs.World) {
	if s.filter == nil || s.mapper == nil {
		return
	}
	query := s.filter.Query()
	for query.Next() {
		entity := query.Entity()
		state, pending := query.Get()
		if state == nil || pending == nil || pending.Spec == nil {
			if s.mapper.PendingUpdate.HasAll(entity) {
				s.mapper.PendingUpdate.Remove(entity)
			}
			continue
		}

		if state.Flags&(components.StatePulsePending|components.StateInterventionPending|components.StateCodePending) != 0 {
			continue
		}

		if _, err := s.mapper.ApplyMonitorSpec(entity, pending.Spec); err != nil {
			if s.mapper.PendingUpdate.HasAll(entity) {
				s.mapper.PendingUpdate.Remove(entity)
			}
			if s.logger != nil {
				s.logger.Warnw("Failed to apply deferred monitor update", "entity_id", entity.ID(), "error", err)
			}
			continue
		}
		if s.logger != nil {
			s.logger.Infow("Applied deferred monitor update", "entity_id", entity.ID(), "monitor_name", state.Name)
		}
	}
}

// Finalize is a no-op for this system.
func (s *PendingUpdateSystem) Finalize(_ *ecs.World) {}
