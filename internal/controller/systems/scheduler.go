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
	Update(w *controller.CPRaWorld)
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
			for _, sys := range s.Systems {
				sys.Update(s.World)
				time.Sleep(10 * time.Millisecond)
			}
		case _, ok := <-s.Done:
			if !ok {
				close(s.JobChan)
				close(s.ResultChan)
				log.Printf("scheduler exitied after %v\n", time.Since(start))
				s.WG.Done()
				return
			}
		}
	}
	//close(s.ResultChan)

}
