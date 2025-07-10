package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"sync"
	"time"
)

//func FirstPulseSystem(world *controller.CPRaWorld) {
//
//	filter := generic.NewFilter2[components.PulseFirstCheck, components.PulseJob]().Without(generic.T[components.DisabledMonitor]())
//	query := filter.Query(world.Mappers.World)
//
//	exchange := generic.NewExchange(world.Mappers.World).
//		Adds(generic.T[components.PulsePulse]()).
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

// Create a bridge system to attach either PulseResults or PulseResults depending on a Pulse type; you can use ID to update entity directly

//func ResultsBridgeSystem(world *controller.CPRaWorld, results <-chan jobs.PulseResult)

type System interface {
	Initialize(w controller.CPRaWorld, lock sync.Locker)
	Update(w controller.CPRaWorld)
}

type Scheduler struct {
	World      controller.CPRaWorld
	Systems    []System
	WG         *sync.WaitGroup
	JobChan    chan jobs.Job    // channel for jobs
	ResultChan chan jobs.Result // channel for results
	Done       chan struct{}
	Lock       sync.RWMutex
}

func (s *Scheduler) AddSystem(sys System) {
	s.Systems = append(s.Systems, sys)
}

func (s *Scheduler) Run(tick time.Duration) {
	start := time.Now()
	fmt.Printf("scheduler started at %v with %v tick\n", start, tick)
	t := time.Tick(tick)
	for _, sys := range s.Systems {
		sys.Initialize(s.World, &s.Lock)
	}
	for {
		select {
		case <-t:
			for _, sys := range s.Systems {
				s.Lock.Lock()
				sys.Update(s.World)
				s.Lock.Unlock()
			}
		case _, ok := <-s.Done:
			if !ok {
				close(s.JobChan)
				close(s.ResultChan)
				fmt.Printf("scheduler exitied after %v\n", time.Since(start))
				s.WG.Done()
				return
			}
		}
	}
	//close(s.ResultChan)

}

type PulseScheduleSystem struct {
	PulseFilter generic.Filter2[components.PulseConfig, components.PulseStatus]
	lock        sync.Locker
}

func (s *PulseScheduleSystem) Initialize(w controller.CPRaWorld, lock sync.Locker) {
	s.PulseFilter = *generic.NewFilter2[components.PulseConfig, components.PulseStatus]().Without(generic.T[components.DisabledMonitor]()).Without(generic.T[components.PulsePending]()).Without(generic.T[components.InterventionNeeded]()).Without(generic.T[components.InterventionPending]()).Without(generic.T[components.CodeNeeded]()).Without(generic.T[components.CodePending]())
	s.lock = lock
	w.Mappers.World.IsLocked()
}
func (s *PulseScheduleSystem) findPulseEntities(w controller.CPRaWorld) []ecs.Entity {
	toCheck := make([]ecs.Entity, 0)

	query := s.PulseFilter.Query(w.Mappers.World)
	for query.Next() {
		config := (*components.PulseConfig)(query.Query.Get(ecs.ComponentID[components.PulseConfig](w.Mappers.World)))
		status := (*components.PulseStatus)(query.Query.Get(ecs.ComponentID[components.PulseStatus](w.Mappers.World)))

		// Check for first-time pulse
		if w.Mappers.World.Has(query.Entity(), ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
			toCheck = append(toCheck, query.Entity())
			continue
		}

		// Check for scheduled interval
		if time.Since(status.LastCheckTime) >= config.Interval {
			fmt.Printf("%v --> %v\n", time.Since(status.LastCheckTime), config.Interval)
			toCheck = append(toCheck, query.Entity())
		}
	}
	// The 'query' variable is destroyed when this function returns.
	return toCheck
}

func (s *PulseScheduleSystem) Update(w controller.CPRaWorld) {
	// Phase 1: Read from the world. The query is created and destroyed
	// entirely within this function call.
	toCheck := s.findPulseEntities(w)

	// Phase 2: Write to the world. This is now safe.
	for _, entity := range toCheck {
		w.Mappers.PulseNeeded.Assign(entity, &components.PulseNeeded{})
	}
}

// PulseDispatchSystem --- Dispatch System ---
type dispatchablePulse struct {
	Entity ecs.Entity
	Job    jobs.Job
}
type PulseDispatchSystem struct {
	JobChan     chan<- jobs.Job
	PulseNeeded generic.Filter3[components.PulseJob, components.PulseStatus, components.PulseNeeded]
	lock        sync.Locker
}

func (s *PulseDispatchSystem) Initialize(w controller.CPRaWorld, lock sync.Locker) {
	s.PulseNeeded = *generic.NewFilter3[components.PulseJob, components.PulseStatus, components.PulseNeeded]()
	s.lock = lock
	w.Mappers.World.IsLocked()
}

func (s *PulseDispatchSystem) findDispatchablePulses(w controller.CPRaWorld) []dispatchablePulse {
	toDispatch := make([]dispatchablePulse, 0)
	query := s.PulseNeeded.Query(w.Mappers.World)
	for query.Next() {
		job := (*components.PulseJob)(query.Query.Get(ecs.ComponentID[components.PulseJob](w.Mappers.World)))
		status := (*components.PulseStatus)(query.Query.Get(ecs.ComponentID[components.PulseStatus](w.Mappers.World)))

		status.LastCheckTime = time.Now() // This is a data-only update, not structural. It's safe.

		toDispatch = append(toDispatch, dispatchablePulse{
			Entity: query.Entity(),
			Job:    job.Job,
		})
	}
	return toDispatch
}

func (s *PulseDispatchSystem) Update(w controller.CPRaWorld) {
	// Phase 1: Read from the world.
	dispatchList := s.findDispatchablePulses(w)

	// Phase 2: Write to the world and channels.
	for _, item := range dispatchList {
		select {
		case s.JobChan <- item.Job.Copy():
			fmt.Println("sent job")
			if w.Mappers.World.Has(item.Entity, ecs.ComponentID[components.PulseFirstCheck](w.Mappers.World)) {
				w.Mappers.PulseFirstCheck.Remove(item.Entity)
			}
			w.Mappers.World.Exchange(item.Entity, []ecs.ID{ecs.ComponentID[components.PulsePending](w.Mappers.World)}, []ecs.ID{ecs.ComponentID[components.PulseNeeded](w.Mappers.World)})
		default:
			// handle worker pool full, maybe log or retry
		}
	}
}

type resultEntry struct {
	entity ecs.Entity
	result jobs.Result
}

// PulseResultSystem --- RESULT PROCESS SYSTEM ---
type PulseResultSystem struct {
	PendingPulseFilter generic.Filter4[components.PulseConfig, components.PulseStatus, components.PulseJob, components.PulsePending]
	ResultChan         <-chan jobs.Result
	lock               sync.Locker
}

func (s *PulseResultSystem) Initialize(w controller.CPRaWorld, lock sync.Locker) {
	s.lock = lock
	w.Mappers.World.IsLocked()
}

func (s *PulseResultSystem) Update(w controller.CPRaWorld) {
	// Collect results to process
	toProcess := make([]resultEntry, 0)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			toProcess = append(toProcess, resultEntry{entity: res.Entity(), result: res})
		default:
			break loop // Exit loop when no more results
		}
	}

	// Process collected results
	for _, entry := range toProcess {
		entity := entry.entity
		res := entry.result

		// Get all component pointers before structural changes
		config := (*components.PulseConfig)(w.Mappers.World.Get(entity, ecs.ComponentID[components.PulseConfig](w.Mappers.World)))
		status := (*components.PulseStatus)(w.Mappers.World.Get(entity, ecs.ComponentID[components.PulseStatus](w.Mappers.World)))
		name := (*components.Name)(w.Mappers.World.Get(entity, ecs.ComponentID[components.Name](w.Mappers.World)))

		// Update data
		if res.Error() != nil {
			status.LastStatus = "failed"
			status.LastError = res.Error()
			status.ConsecutiveFailures++
			if status.ConsecutiveFailures == 1 {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.YellowCode](w.Mappers.World)) {
					w.Mappers.CodeNeeded.Assign(entity, &components.CodeNeeded{Color: "yellow"})
				}
			}
			if config.MaxFailures <= status.ConsecutiveFailures {
				monitorStatus := (*components.MonitorStatus)(w.Mappers.World.Get(entity, ecs.ComponentID[components.MonitorStatus](w.Mappers.World)))
				monitorStatus.Status = "failed"
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.InterventionConfig](w.Mappers.World)) {
					fmt.Printf("Monitor %s failed %d times and needs intervention\n", *name, status.ConsecutiveFailures)
					w.Mappers.InterventionNeeded.Assign(entity, &components.InterventionNeeded{})
				}
			}
		} else {
			status.LastStatus = "success"
			status.LastError = nil
			status.ConsecutiveFailures = 0
			status.LastSuccessTime = time.Now()
			monitorStatus := (*components.MonitorStatus)(w.Mappers.World.Get(entity, ecs.ComponentID[components.MonitorStatus](w.Mappers.World)))
			s := monitorStatus.Status
			monitorStatus.Status = "success"
			if s == "failed" {
				if w.Mappers.World.Has(entity, ecs.ComponentID[components.GreenCode](w.Mappers.World)) {
					w.Mappers.CodeNeeded.Assign(entity, &components.CodeNeeded{Color: "green"})
				}
			}
		}

		// Perform structural change last
		w.Mappers.PulsePending.Remove(entity)
	}
}
