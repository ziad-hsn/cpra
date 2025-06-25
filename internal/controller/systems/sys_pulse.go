package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/filter"
	"github.com/mlange-42/arche/generic"
	"os"
	"time"
)

//func FirstPulseSystem(world *controller.CPRaWorld) {
//
//	filter := generic.NewFilter2[components.PulseFirstCheck, components.PulseJob]().Without(generic.T[components.DisabledMonitor]())
//	query := filter.Query(world.Mappers.World)
//
//	exchange := generic.NewExchange(world.Mappers.World).
//		Adds(generic.T[components.PulsePending]()).
//		Removes(
//			generic.T[components.PulseFirstCheck](),
//		)
//	var toExchange []ecs.Entity
//
//	for query.Next() {
//		_, job := query.Get()
//		entity := query.Entity()
//
//		go job.Job.Execute()
//
//		toExchange = append(toExchange, entity)
//	}
//	for _, entity := range toExchange {
//		_, status := world.Mappers.Pulse.Get(entity)
//		exchange.Exchange(entity)
//
//		status.LastStatus = "Pending"
//		status.LastCheckTime = time.Now()
//		status.LastError = nil
//	}
//
//}

// Create a bridge system to attach either PulseResults or InterventionResults depending on a Pulse type; you can use ID to update entity directly

//func ResultsBridgeSystem(world *controller.CPRaWorld, results <-chan jobs.PulseResult)

type System interface {
	Initialize(w controller.CPRaWorld)
	Update(w controller.CPRaWorld)
}

type Scheduler struct {
	World      controller.CPRaWorld
	Systems    []System
	JobChan    chan jobs.Job    // channel for jobs
	ResultChan chan jobs.Result // channel for results
	Done       chan struct{}
}

func (s *Scheduler) AddSystem(sys System) {
	s.Systems = append(s.Systems, sys)
}

func (s *Scheduler) Run(tick time.Duration) {
	start := time.Now()
	fmt.Printf("scheduler started at %v with %v tick\n", start, tick)
	t := time.Tick(tick)
	for _, sys := range s.Systems {
		sys.Initialize(s.World)
	}
	for {
		select {
		case <-t:
			for _, sys := range s.Systems {
				sys.Update(s.World)
			}
		case _, ok := <-s.Done:
			if !ok {
				close(s.JobChan)
				fmt.Printf("scheduler exitied after %v\n", time.Since(start))
				return
			}
		}
	}
	//close(s.ResultChan)

}

// PulseDispatchSystem --- Dispatch System ---
type PulseDispatchSystem struct {
	JobChan           chan<- jobs.Job
	FirstCheckFilter  generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulseFirstCheck]
	FailedCheckFilter generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulseFailed]
}

func (s *PulseDispatchSystem) Initialize(w controller.CPRaWorld) {
	s.FirstCheckFilter = *generic.NewFilter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulseFirstCheck]()
	s.FailedCheckFilter = *generic.NewFilter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulseFailed]()
	w.Mappers.World.IsLocked()
}

func (s *PulseDispatchSystem) Update(w controller.CPRaWorld) {
	toDispatch := make(map[ecs.Entity]jobs.Job)
	f := filter.Or(s.FirstCheckFilter.Filter(w.Mappers.World), s.FailedCheckFilter.Filter(w.Mappers.World))
	query := w.Mappers.World.Query(f)

	for query.Next() {
		job := (*components.PulseJob)(query.Get(ecs.ComponentID[components.PulseJob](w.Mappers.World)))
		// if an interval elapsed and not pending, append to toDispatch
		// (add your logic)
		toDispatch[query.Entity()] = job.Job
	}
	for entity, job := range toDispatch {
		select {
		case s.JobChan <- job:
			fmt.Println("sent job")
			if w.Mappers.World.Has(entity, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
				w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.PulsePending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)})
			} else {
				w.Mappers.World.Exchange(entity, []ecs.ID{ecs.ComponentID[components.PulsePending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.PulseFailed](w.Mappers.World)})
			}
			// mark entity as pending (using Exchange after a query)
		default:
			// handle worker pool full, maybe log or retry
		}
	}
}

// PulseResultSystem --- RESULT PROCESS SYSTEM ---
type PulseResultSystem struct {
	PendingPulseFilter generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulsePending]
	ResultChan         <-chan jobs.Result
}

func (s *PulseResultSystem) Initialize(w controller.CPRaWorld) {
	w.Mappers.World.IsLocked()
}

func (s *PulseResultSystem) Update(w controller.CPRaWorld) {
	for {
		select {
		case res := <-s.ResultChan:
			entity := res.Entity()
			w.Mappers.PulsePending.Remove(entity)
			if w.Mappers.World.Has(entity, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
				w.Mappers.PulseFirstCheck.Remove(entity)
			}
			config, status := w.Mappers.Pulse.Get(entity)

			if res.Error() != nil {

				status.LastStatus = "failed"
				status.LastError = res.Error()
				status.ConsecutiveFailures++
				//w.Mappers.Pulse.Assign(entity, config, status)
				if config.MaxFailures <= status.ConsecutiveFailures {
					if w.Mappers.World.Has(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)) {
						//	Add intervention markers here
						name := w.Mappers.Name.Get(entity)
						fmt.Printf("Monitor %s failed and needs intervention\n", *name)
						os.Exit(1)
					}
				}
				w.Mappers.PulseFailed.Add(entity)

			} else {
				status.LastStatus = "success"
				status.LastError = nil
				status.ConsecutiveFailures = 0
				status.LastSuccessTime = time.Now()
				//w.Mappers.Pulse.Assign(entity, config, status)
				w.Mappers.PulseSuccess.Add(entity)
			}
			// update status/markers for res.Entity
			// (add PulseSuccess/PulseFailed etc. via Exchange)
		default:
			return
		}
	}
}
