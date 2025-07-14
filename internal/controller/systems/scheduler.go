package systems

import (
	"cpra/internal/controller"
	"cpra/internal/jobs"
	"log"
	"time"
)

type System interface {
	Initialize(w *controller.CPRaWorld)
	Update(w *controller.CPRaWorld)
}

type Scheduler struct {
	World       *controller.CPRaWorld
	Systems     []System
	JobChan     chan jobs.Job
	IJobChan    chan jobs.Job
	CJobChan    chan jobs.Job
	ResultChan  chan jobs.Result
	IResultChan chan jobs.Result
	CResultChan chan jobs.Result
	Done        chan struct{}
}

func (s *Scheduler) AddSystem(sys System) {
	s.Systems = append(s.Systems, sys)
}

func (s *Scheduler) Run(tick time.Duration) {
	// 1) One‐time init
	for _, sys := range s.Systems {
		sys.Initialize(s.World)
	}

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// —— Phase 0: Drain all back‐and‐forth channels in one go ——
			s.drainJobs()

			// —— Phase 1: Run every ECS system, now that all results are in ——
			for _, sys := range s.Systems {
				sys.Update(s.World)
			}

		case <-s.Done:
			log.Printf("shutting down scheduler")
			close(s.JobChan)
			close(s.IJobChan)
			close(s.CJobChan)
			close(s.ResultChan)
			close(s.IResultChan)
			close(s.CResultChan)
			return
		}
	}
}

func (s *Scheduler) drainJobs() {
	for {
		select {
		case job := <-s.JobChan:
			s.ResultChan <- job.Execute()
		case job := <-s.IJobChan:
			s.IResultChan <- job.Execute()
		case job := <-s.CJobChan:
			s.CResultChan <- job.Execute()
		default:
			return
		}
	}
}
