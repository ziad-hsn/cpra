package systems

import (
	"cpra/internal/loader/schema"
	"log"
	"sync"
	"time"

	"cpra/internal/controller"
)

type PhaseSystem interface {
	Initialize(w *controller.CPRaWorld)
	// Update returns the list of deferred structural-change operations.
	Update(w *controller.CPRaWorld) []func()
}

type Scheduler struct {
	World           *controller.CPRaWorld
	ScheduleSystems []PhaseSystem
	DispatchSystems []PhaseSystem
	ResultSystems   []PhaseSystem
	WG              *sync.WaitGroup
	Done            chan struct{}
	Tick            time.Duration
}

func NewScheduler(manifest *schema.Manifest, wg *sync.WaitGroup, tick time.Duration) *Scheduler {
	world, err := controller.NewCPRaWorld(manifest)
	if err != nil {
		log.Fatal(err)
	}

	return &Scheduler{World: world, WG: wg, Tick: tick, Done: make(chan struct{})}
}

func (s *Scheduler) AddSchedule(sys PhaseSystem) { s.ScheduleSystems = append(s.ScheduleSystems, sys) }
func (s *Scheduler) AddDispatch(sys PhaseSystem) { s.DispatchSystems = append(s.DispatchSystems, sys) }
func (s *Scheduler) AddResult(sys PhaseSystem)   { s.ResultSystems = append(s.ResultSystems, sys) }

func (s *Scheduler) Run() {
	log.Printf("scheduler started with %v tick\n", s.Tick)
	ticker := time.NewTicker(s.Tick)
	defer ticker.Stop()
	defer func() {
		if r := recover(); r != nil {
			log.Println("Recovered in Scheduler:", r)
		}
	}()
	// Initialize all systems
	for _, sys := range s.ScheduleSystems {
		sys.Initialize(s.World)
	}
	for _, sys := range s.DispatchSystems {
		sys.Initialize(s.World)
	}
	for _, sys := range s.ResultSystems {
		sys.Initialize(s.World)
	}

	for {
		select {
		case <-ticker.C:
			//x := debug.GCStats{}
			//debug.ReadGCStats(&x)
			//fmt.Println(x)
			// Phase 1: schedule
			var allDeferredOps []func()
			// Collect all deferred operations
			for _, sys := range s.ResultSystems {
				ops := sys.Update(s.World)
				allDeferredOps = append(allDeferredOps, ops...)
			}
			for _, sys := range s.ScheduleSystems {
				ops := sys.Update(s.World)
				allDeferredOps = append(allDeferredOps, ops...)
			}
			for _, sys := range s.DispatchSystems {
				ops := sys.Update(s.World)
				allDeferredOps = append(allDeferredOps, ops...)
			}
			//log.Println(allDeferredOps)
			// Apply all deferred operations at once
			for _, op := range allDeferredOps {
				s.World.SafeAccess(op)
				//time.Sleep(100 * time.Millisecond)
			}

		case <-s.Done:
			log.Printf("scheduler exited\n")
			s.WG.Done()
			return
		}
	}
}
