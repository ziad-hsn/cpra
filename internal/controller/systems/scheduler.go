package systems

import (
	"cpra/internal/controller"
	"cpra/internal/jobs"
	"log"
	"sync"
	"time"
)

type System interface {
	Initialize(w *controller.CPRaWorld)
	Update(w *controller.CPRaWorld) []func()
}

type Scheduler struct {
	World      *controller.CPRaWorld
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
	log.Printf("scheduler started at %v with %v tick\n", start, tick)
	t := time.NewTicker(tick)
	defer t.Stop()
	for _, sys := range s.Systems {
		sys.Initialize(s.World)
	}
	for {
		select {
		case <-t.C:
			var allDeferredOps []func()
			for _, sys := range s.Systems {
				ops := sys.Update(s.World)
				allDeferredOps = append(allDeferredOps, ops...)
			}
			// Apply all deferred operations after all systems have been updated
			for _, op := range allDeferredOps {
				op()
			}
		case _, ok := <-s.Done:
			if !ok {
				//close(s.JobChan)
				//close(s.ResultChan)
				log.Printf("scheduler exitied after %v\n", time.Since(start))
				s.WG.Done()
				return
			}
		}
	}
}
