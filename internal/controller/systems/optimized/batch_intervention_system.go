package optimized

import (
	"context"
	"time"

	"github.com/mlange-42/ark/ecs"
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities" 
	"cpra/internal/jobs"
	"cpra/internal/queue/optimized"
)

type BatchInterventionSystem struct {
	world          *ecs.World
	batchCollector *optimized.BatchCollector
	config         SystemConfig
	
	// Use entity manager like original systems
	Mapper *entities.EntityManager
	
	// Component filter like original
	InterventionNeededFilter ecs.Filter1[components.InterventionNeeded]
	
	// Batching
	batchSize int
	
	// Logger interface
	logger Logger
}

func NewBatchInterventionSystem(world *ecs.World, batchCollector *optimized.BatchCollector, config SystemConfig, logger Logger) *BatchInterventionSystem {
	system := &BatchInterventionSystem{
		world:          world,
		batchCollector: batchCollector,
		config:         config,
		batchSize:      config.BatchSize,
		Mapper:         entities.InitializeMappers(world),
		logger:         logger,
	}
	
	system.Initialize(world)
	return system
}

func (s *BatchInterventionSystem) Initialize(w *ecs.World) {
	s.InterventionNeededFilter = *ecs.
		NewFilter1[components.InterventionNeeded](w).
		Without(ecs.C[components.InterventionPending]())
}

// collectWork - exactly like original
func (s *BatchInterventionSystem) collectWork(w *ecs.World) map[ecs.Entity]jobs.Job {
	start := time.Now()
	out := make(map[ecs.Entity]jobs.Job)
	query := s.InterventionNeededFilter.Query()

	for query.Next() {
		ent := query.Entity()
		job := s.Mapper.InterventionJob.Get(ent).Job
		if job != nil {
			out[ent] = job

			namePtr := s.Mapper.Name.Get(ent)
			if namePtr != nil {
				s.logger.Debug("Entity[%d] (%s) intervention job collected", ent.ID(), *namePtr)
			}
		} else {
			s.logger.Warn("Entity[%d] has no intervention job", ent.ID())
		}
	}

	s.logger.LogSystemPerformance("BatchInterventionDispatch", time.Since(start), len(out))
	return out
}

// applyWork - batch version of original
func (s *BatchInterventionSystem) applyWork(w *ecs.World, jobsMap map[ecs.Entity]jobs.Job) {
	// Convert to slice for batching
	entities := make([]ecs.Entity, 0, len(jobsMap))
	jobList := make([]jobs.Job, 0, len(jobsMap))
	
	for ent, job := range jobsMap {
		entities = append(entities, ent)
		jobList = append(jobList, job)
	}
	
	// Process in batches
	for i := 0; i < len(entities); i += s.batchSize {
		end := i + s.batchSize
		if end > len(entities) {
			end = len(entities)
		}
		
		// Process batch
		for j := i; j < end; j++ {
			ent := entities[j]
			job := jobList[j]
			
			if w.Alive(ent) {
				// Prevent component duplication - exactly like original
				if s.Mapper.InterventionPending.HasAll(ent) {
					namePtr := s.Mapper.Name.Get(ent)
					if namePtr != nil {
						s.logger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %d", *namePtr, ent.ID())
					}
					continue
				}

				// Submit job to batch collector instead of queue manager
				err := s.batchCollector.Add(job)
				if err != nil {
					s.logger.Warn("Failed to enqueue intervention for entity %d: %v", ent.ID(), err)
					continue
				}

				// Safe component transition - exactly like original
				if s.Mapper.InterventionNeeded.HasAll(ent) {
					s.Mapper.InterventionNeeded.Remove(ent)
					s.Mapper.InterventionPending.Add(ent, &components.InterventionPending{})

					namePtr := s.Mapper.Name.Get(ent)
					if namePtr != nil {
						s.logger.Debug("Dispatched %s job for entity: %d", *namePtr, ent.ID())
					}
					s.logger.LogComponentState(uint32(ent.ID()), "InterventionNeeded->InterventionPending", "transitioned")
				}
			}
		}
	}
}

func (s *BatchInterventionSystem) Update(ctx context.Context) error {
	toDispatch := s.collectWork(s.world)
	s.applyWork(s.world, toDispatch)
	return nil
}

// GetStats returns dummy stats to satisfy interface - batch systems track stats differently
func (s *BatchInterventionSystem) GetStats() InterventionStats {
	return InterventionStats{
		EntitiesProcessed: 0,
		BatchesCreated:    0,
		JobsDispatched:    0,
	}
}

// InterventionStats tracks intervention system performance  
type InterventionStats struct {
	EntitiesProcessed int64
	BatchesCreated    int64
	JobsDispatched    int64
}