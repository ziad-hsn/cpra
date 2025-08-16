package systems

import (
	"cpra/internal/loader/schema"
	"log"
	"sync"
	"time"

	"cpra/internal/controller"

	"github.com/mlange-42/ark/ecs"
)

type PhaseSystem interface {
	Initialize(w *controller.CPRaWorld)
	// Update returns the list of deferred structural-change operations.
	Update(w *controller.CPRaWorld, cb *CommandBufferSystem)
}

type Scheduler struct {
	World           *controller.CPRaWorld
	ScheduleSystems []PhaseSystem
	DispatchSystems []PhaseSystem
	ResultSystems   []PhaseSystem
	WG              *sync.WaitGroup
	Done            chan struct{}
	Tick            time.Duration
	CommandBuffer   *CommandBufferSystem
}

func NewScheduler(manifest *schema.Manifest, wg *sync.WaitGroup, tick time.Duration, world *ecs.World) *Scheduler {
	w, err := controller.NewCPRaWorld(manifest, world)
	if err != nil {
		log.Fatal(err)
	}
	return &Scheduler{World: w, WG: wg, Tick: tick, Done: make(chan struct{})}
}

func (s *Scheduler) AddSchedule(sys PhaseSystem) { s.ScheduleSystems = append(s.ScheduleSystems, sys) }
func (s *Scheduler) AddDispatch(sys PhaseSystem) { s.DispatchSystems = append(s.DispatchSystems, sys) }
func (s *Scheduler) AddResult(sys PhaseSystem)   { s.ResultSystems = append(s.ResultSystems, sys) }

func (s *Scheduler) Run() {
	log.Printf("scheduler started with %v tick\n", s.Tick)
	ticker := time.NewTicker(s.Tick)
	defer ticker.Stop()
	//defer func() {
	//	if r := recover(); r != nil {
	//		log.Println("Recovered in Scheduler:", r)
	//	}
	//}()
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

	s.CommandBuffer.Init()

	for {
		select {
		case <-ticker.C:
			// Collect all deferred operations
			for _, sys := range s.ScheduleSystems {
				sys.Update(s.World, s.CommandBuffer)
			}
			for _, sys := range s.DispatchSystems {
				sys.Update(s.World, s.CommandBuffer)
			}
			for _, sys := range s.ResultSystems {
				sys.Update(s.World, s.CommandBuffer)
			}
			s.CommandBuffer.PlayBack()

			s.CommandBuffer.Clear()

			//runtime.GC()

		case <-s.Done:
			log.Printf("scheduler exited\n")
			s.WG.Done()
			return
		}
	}
}
