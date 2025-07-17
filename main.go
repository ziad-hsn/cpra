package main

import (
	"cpra/internal/workers/workerspool"
	"fmt"
	"log"
	"sync"
	"time"

	"cpra/internal/controller"
	"cpra/internal/controller/systems"
	"cpra/internal/loader/loader"
)

func main() {
	l := loader.NewLoader("yaml", "internal/loader/test.yaml")
	l.Load()
	manifest := l.GetManifest()

	world, err := controller.NewCPRaWorld(&manifest)
	if err != nil {
		log.Fatal(err)
	}

	// make channels
	//jobCh := make(chan jobs.Job, len(manifest.Monitors))
	//resCh := make(chan jobs.Result, len(manifest.Monitors))
	//ijobCh := make(chan jobs.Job, len(manifest.Monitors))
	//iresCh := make(chan jobs.Result, len(manifest.Monitors))
	//cjobCh := make(chan jobs.Job, len(manifest.Monitors))
	//cresCh := make(chan jobs.Result, len(manifest.Monitors))

	// start workers pools
	pools := workerspool.NewPoolsManager()
	pools.NewPool("pulse", 10, 10, 10)
	pools.NewPool("intervention", 10, 10, 10)
	pools.NewPool("code", 10, 10, 10)
	pulseJobChan, err := pools.GetJobChannel("pulse")
	if err != nil {
		log.Fatal(err)
	}
	interventionJobChan, err := pools.GetJobChannel("intervention")
	if err != nil {
		log.Fatal(err)
	}
	CodeJobChan, err := pools.GetJobChannel("code")
	if err != nil {
		log.Fatal(err)
	}
	pulseResultChan, err := pools.GetResultChannel("pulse")
	if err != nil {
		log.Fatal(err)
	}
	interventionResultChan, err := pools.GetResultChannel("intervention")
	if err != nil {
		log.Fatal(err)
	}
	codeResultChan, err := pools.GetResultChannel("code")
	if err != nil {
		log.Fatal(err)
	}
	pools.StartAll()
	// build scheduler
	wg := &sync.WaitGroup{}
	sched := systems.NewScheduler(world, wg, 10*time.Millisecond)

	// Phase 1
	sched.AddSchedule(&systems.PulseScheduleSystem{})

	// Phase 2
	sched.AddDispatch(&systems.PulseDispatchSystem{JobChan: pulseJobChan})
	sched.AddDispatch(&systems.InterventionDispatchSystem{JobChan: interventionJobChan})
	sched.AddDispatch(&systems.CodeDispatchSystem{JobChan: CodeJobChan})

	// Phase 3
	sched.AddResult(&systems.PulseResultSystem{ResultChan: pulseResultChan})
	sched.AddResult(&systems.InterventionResultSystem{ResultChan: interventionResultChan})
	sched.AddResult(&systems.CodeResultSystem{ResultChan: codeResultChan})

	wg.Add(1)
	go sched.Run()
	timeout := time.After(24 * time.Second)
	// worker‐loop
	for {
		select {
		//case job := <-pulseJobChan:
		//	pulseResultChan <- job.Execute()
		//case job := <-interventionJobChan:
		//	interventionResultChan <- job.Execute()
		//case job := <-CodeJobChan:
		//	codeResultChan <- job.Execute()
		case <-timeout:
			fmt.Println("timeout")
			close(sched.Done)
			wg.Wait()
			return
		}
	}
}
