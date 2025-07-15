package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"cpra/internal/controller"
	"cpra/internal/controller/systems"
	"cpra/internal/jobs"
	"cpra/internal/loader/loader"
)

func main() {
	loader := loader.NewLoader("yaml", "internal/loader/test.yaml")
	loader.Load()
	manifest := loader.GetManifest()

	world, err := controller.NewCPRaWorld(&manifest)
	if err != nil {
		log.Fatal(err)
	}

	// make channels
	jobCh := make(chan jobs.Job, len(manifest.Monitors))
	resCh := make(chan jobs.Result, len(manifest.Monitors))
	ijobCh := make(chan jobs.Job, len(manifest.Monitors))
	iresCh := make(chan jobs.Result, len(manifest.Monitors))
	cjobCh := make(chan jobs.Job, len(manifest.Monitors))
	cresCh := make(chan jobs.Result, len(manifest.Monitors))

	// build scheduler
	wg := &sync.WaitGroup{}
	sched := systems.NewScheduler(world, wg, 10*time.Millisecond)

	// Phase 1
	sched.AddSchedule(&systems.PulseScheduleSystem{})

	// Phase 2
	sched.AddDispatch(&systems.PulseDispatchSystem{JobChan: jobCh})
	sched.AddDispatch(&systems.InterventionDispatchSystem{JobChan: ijobCh})
	sched.AddDispatch(&systems.CodeDispatchSystem{JobChan: cjobCh})

	// Phase 3
	sched.AddResult(&systems.PulseResultSystem{ResultChan: resCh})
	sched.AddResult(&systems.InterventionResultSystem{ResultChan: iresCh})
	sched.AddResult(&systems.CodeResultSystem{ResultChan: cresCh})

	wg.Add(1)
	go sched.Run()
	timeout := time.After(24 * time.Second)
	// worker‐loop
	for {
		select {
		case job := <-jobCh:
			resCh <- job.Execute()
		case job := <-ijobCh:
			iresCh <- job.Execute()
		case job := <-cjobCh:
			cresCh <- job.Execute()
		case <-timeout:
			fmt.Println("timeout")
			close(sched.Done)
			wg.Wait()
			return
		}
	}
}
