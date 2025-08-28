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

type BatchCodeSystem struct {
	world          *ecs.World
	batchCollector *optimized.BatchCollector
	config         SystemConfig
	
	// Use entity manager like original systems
	Mapper *entities.EntityManager
	
	// Component filter like original
	CodeNeededFilter *ecs.Filter1[components.CodeNeeded]
	
	// Batching
	batchSize int
	
	// Logger interface
	logger Logger
}

type Logger interface {
	Warn(format string, args ...interface{})
	Debug(format string, args ...interface{})
	LogSystemPerformance(name string, duration time.Duration, count int)
	LogComponentState(entityID uint32, component string, action string)
}

func NewBatchCodeSystem(world *ecs.World, batchCollector *optimized.BatchCollector, config SystemConfig, logger Logger) *BatchCodeSystem {
	system := &BatchCodeSystem{
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

func (s *BatchCodeSystem) Initialize(w *ecs.World) {
	s.CodeNeededFilter = ecs.NewFilter1[components.CodeNeeded](w).
		Without(ecs.C[components.CodePending]())
}

// collectWork - exactly like original but collect in batches
func (s *BatchCodeSystem) collectWork(w *ecs.World) map[ecs.Entity]dispatchableCodeJob {
	start := time.Now()
	out := make(map[ecs.Entity]dispatchableCodeJob)
	query := s.CodeNeededFilter.Query()

	for query.Next() {
		ent := query.Entity()
		color := query.Get().Color
		var job jobs.Job

		switch color {
		case "red":
			if s.Mapper.RedCode.HasAll(ent) {
				job = s.Mapper.RedCodeJob.Get(ent).Job
			}
		case "green":
			if s.Mapper.GreenCode.HasAll(ent) {
				job = s.Mapper.GreenCodeJob.Get(ent).Job
			}
		case "yellow":
			if s.Mapper.YellowCode.HasAll(ent) {
				job = s.Mapper.YellowCodeJob.Get(ent).Job
			}
		case "cyan":
			if s.Mapper.CyanCode.HasAll(ent) {
				job = s.Mapper.CyanCodeJob.Get(ent).Job
			}
		case "gray":
			if s.Mapper.GrayCode.HasAll(ent) {
				job = s.Mapper.GrayCodeJob.Get(ent).Job
			}
		default:
			s.logger.Warn("Unknown color %q for entity %v", color, ent)
		}

		if job != nil {
			out[ent] = dispatchableCodeJob{job: job, color: color}
		}
	}
	s.logger.LogSystemPerformance("BatchCodeDispatch", time.Since(start), len(out))
	return out
}

// applyWork - batch version of original
func (s *BatchCodeSystem) applyWork(w *ecs.World, list map[ecs.Entity]dispatchableCodeJob) {
	// Convert to slice for batching
	entities := make([]ecs.Entity, 0, len(list))
	jobs := make([]dispatchableCodeJob, 0, len(list))
	
	for e, job := range list {
		entities = append(entities, e)
		jobs = append(jobs, job)
	}
	
	// Process in batches
	for i := 0; i < len(entities); i += s.batchSize {
		end := i + s.batchSize
		if end > len(entities) {
			end = len(entities)
		}
		
		// Process batch
		for j := i; j < end; j++ {
			e := entities[j]
			item := jobs[j]
			
			if w.Alive(e) {
				// Prevent component duplication - exactly like original
				if s.Mapper.CodePending.HasAll(e) {
					namePtr := s.Mapper.Name.Get(e)
					if namePtr != nil {
						s.logger.Warn("Monitor %s already has pending component, skipping dispatch for entity: %v", *namePtr, e)
					}
					continue
				}

				// Submit job to batch collector instead of queue manager
				err := s.batchCollector.Add(item.job)
				if err != nil {
					s.logger.Warn("Failed to enqueue code for entity %d: %v", e, err)
					continue
				}

				// Safe component transition - exactly like original
				if s.Mapper.CodeNeeded.HasAll(e) {
					s.Mapper.CodeNeeded.Remove(e)
					s.Mapper.CodePending.Add(e, &components.CodePending{Color: item.color})

					namePtr := s.Mapper.Name.Get(e)
					if namePtr != nil {
						s.logger.Debug("Dispatched %s code job for entity: %d", item.color, e.ID())
					}
					s.logger.LogComponentState(uint32(e.ID()), "CodeNeeded->CodePending", "transitioned")
				}
			}
		}
	}
}

func (s *BatchCodeSystem) Update(ctx context.Context) error {
	toDispatch := s.collectWork(s.world)
	s.applyWork(s.world, toDispatch)
	return nil
}

// GetStats returns dummy stats to satisfy interface - batch systems track stats differently
func (s *BatchCodeSystem) GetStats() CodeStats {
	return CodeStats{
		EntitiesProcessed: 0,
		BatchesCreated:    0,
		JobsDispatched:    0,
	}
}

// CodeStats tracks code system performance
type CodeStats struct {
	EntitiesProcessed int64
	BatchesCreated    int64
	JobsDispatched    int64
}

// Copy the type from original
type dispatchableCodeJob struct {
	job   jobs.Job
	color string
}