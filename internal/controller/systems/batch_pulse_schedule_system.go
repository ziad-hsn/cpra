package systems

import (
	"sync/atomic"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mlange-42/ark/generic"

	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
)

// BatchPulseScheduleSystem schedules pulse checks using proper Ark batch operations
type BatchPulseScheduleSystem struct {
	world  *ecs.World
	Mapper *entities.EntityManager
	logger Logger

	// Cached filter for optimal Ark performance
	scheduleFilter *generic.Filter2[components.PulseConfig, components.PulseStatus]

	// Performance tracking
	entitiesScheduled int64
	lastScheduleTime  time.Time
}

// NewBatchPulseScheduleSystem creates the scheduling system using Ark best practices
func NewBatchPulseScheduleSystem(world *ecs.World, mapper *entities.EntityManager, batchSize int, logger Logger) *BatchPulseScheduleSystem {
	system := &BatchPulseScheduleSystem{
		world:            world,
		Mapper:           mapper,
		logger:           logger,
		lastScheduleTime: time.Now(),
	}

	system.Initialize(world)
	return system
}

// Initialize creates and registers cached filters for optimal performance
func (s *BatchPulseScheduleSystem) Initialize(w *ecs.World) {
	// Create cached filter and register it (Ark best practice)
	s.scheduleFilter = generic.NewFilter2[components.PulseConfig, components.PulseStatus](w).
		Without(generic.T[components.DisabledMonitor]()).
		Without(generic.T[components.PulseNeeded]()).
		Without(generic.T[components.PulsePending]()).
		Without(generic.T[components.InterventionNeeded]()).
		Without(generic.T[components.InterventionPending]()).
		Register()
}

// Update schedules pulse checks using Ark's efficient batch operations
func (s *BatchPulseScheduleSystem) Update(w *ecs.World) {
	start := time.Now()
	
	// Collect entities that need scheduling
	entitiesToSchedule := s.collectWork(w)
	if len(entitiesToSchedule) == 0 {
		return
	}
	
	// Apply scheduling using Ark batch operations
	s.applyScheduling(entitiesToSchedule)
	
	// Update performance metrics
	atomic.AddInt64(&s.entitiesScheduled, int64(len(entitiesToSchedule)))
	s.lastScheduleTime = time.Now()
	
	s.logger.LogSystemPerformance("BatchPulseSchedule", time.Since(start), len(entitiesToSchedule))
}

// collectWork identifies entities that need to be scheduled for pulse checks
func (s *BatchPulseScheduleSystem) collectWork(w *ecs.World) []ecs.Entity {
	start := time.Now()
	now := time.Now()
	var entitiesToSchedule []ecs.Entity
	
	// Use cached filter for optimal performance
	query := s.scheduleFilter.Query()
	defer query.Close()

	for query.Next() {
		entity := query.Entity()
		pulseConfig, pulseStatus := query.Get()
		
		// Check if it's time for the next pulse check
		if s.shouldSchedulePulse(entity, pulseConfig, pulseStatus, now) {
			entitiesToSchedule = append(entitiesToSchedule, entity)
		}
	}

	s.logger.LogSystemPerformance("BatchPulseScheduleCollect", time.Since(start), len(entitiesToSchedule))
	return entitiesToSchedule
}

// shouldSchedulePulse determines if an entity should be scheduled for pulse check
func (s *BatchPulseScheduleSystem) shouldSchedulePulse(entity ecs.Entity, config *components.PulseConfig, status *components.PulseStatus, now time.Time) bool {
	// First check - schedule immediately
	if status.LastCheckTime.IsZero() || s.Mapper.PulseFirstCheck.HasAll(entity) {
		return true
	}
	
	// Regular interval check
	nextCheckTime := status.LastCheckTime.Add(config.Interval)
	return now.After(nextCheckTime) || now.Equal(nextCheckTime)
}

// applyScheduling adds PulseNeeded components using Ark's efficient batch operations
func (s *BatchPulseScheduleSystem) applyScheduling(entities []ecs.Entity) {
	if len(entities) == 0 {
		return
	}
	
	// Filter out entities that are no longer valid
	validEntities := make([]ecs.Entity, 0, len(entities))
	for _, entity := range entities {
		if s.world.Alive(entity) && !s.Mapper.PulseNeeded.HasAll(entity) {
			validEntities = append(validEntities, entity)
		}
	}
	
	if len(validEntities) == 0 {
		return
	}
	
	// Use Ark's efficient batch operation to add PulseNeeded components
	pulseNeededComponent := &components.PulseNeeded{
		ScheduledTime: time.Now(),
	}
	s.Mapper.PulseNeeded.AddBatch(validEntities, pulseNeededComponent)
	
	// Remove PulseFirstCheck if present (using batch operation)
	firstCheckEntities := make([]ecs.Entity, 0, len(validEntities))
	for _, entity := range validEntities {
		if s.Mapper.PulseFirstCheck.HasAll(entity) {
			firstCheckEntities = append(firstCheckEntities, entity)
		}
	}
	
	if len(firstCheckEntities) > 0 {
		s.Mapper.PulseFirstCheck.RemoveBatch(firstCheckEntities, nil)
	}
	
	// Log scheduling for monitoring
	for _, entity := range validEntities {
		namePtr := s.Mapper.Name.Get(entity)
		if namePtr != nil {
			pulseConfig := s.Mapper.PulseConfig.Get(entity)
			pulseStatus := s.Mapper.PulseStatus.Get(entity)
			
			if pulseConfig != nil && pulseStatus != nil {
				if pulseStatus.LastCheckTime.IsZero() {
					s.logger.Info("PULSE SCHEDULED: %s (interval: %v, FIRST CHECK)", 
						*namePtr, pulseConfig.Interval)
				} else {
					timeSinceLastCheck := time.Since(pulseStatus.LastCheckTime)
					s.logger.Debug("PULSE SCHEDULED: %s (interval: %v, time since last: %v)", 
						*namePtr, pulseConfig.Interval, timeSinceLastCheck)
				}
			}
		}
	}
}

// GetStats returns performance statistics
func (s *BatchPulseScheduleSystem) GetStats() BatchPulseScheduleStats {
	return BatchPulseScheduleStats{
		EntitiesScheduled: atomic.LoadInt64(&s.entitiesScheduled),
		LastScheduleTime:  s.lastScheduleTime,
	}
}

// BatchPulseScheduleStats provides performance metrics
type BatchPulseScheduleStats struct {
	EntitiesScheduled int64     `json:"entities_scheduled"`
	LastScheduleTime  time.Time `json:"last_schedule_time"`
}

// Reset resets performance counters
func (s *BatchPulseScheduleSystem) Reset() {
	atomic.StoreInt64(&s.entitiesScheduled, 0)
	s.lastScheduleTime = time.Now()
}

