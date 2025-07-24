package main

import (
	"cpra/internal/workers/workerspool"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"cpra/internal/controller/systems"
	"cpra/internal/loader/loader"
)

func main() {
	//f, err := os.Create("cpu.prof")
	//if err != nil {
	//	log.Fatal("could not create CPU profile: ", err)
	//}
	//defer f.Close()
	//if err := pprof.StartCPUProfile(f); err != nil {
	//	log.Fatal("could not start CPU profile: ", err)
	//}
	//defer pprof.StopCPUProfile()
	//runtime.GOMAXPROCS(24)
	defer func() {
		if r := recover(); r != nil {
			log.Println("Recovered in main:", r)
		}
	}()
	debug.SetGCPercent(1)
	//debug.SetMemoryLimit(1024 * 1024 * 1024 * 1024)

	f, err := os.OpenFile("crash.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatal(err)
	}
	err = debug.SetCrashOutput(f, debug.CrashOptions{})
	if err != nil {
		log.Fatal(err)
	}
	l := loader.NewLoader("yaml", "internal/loader/replicated_test.yaml")
	l.Load()
	manifest := l.GetManifest()

	// make channels
	//jobCh := make(chan jobs.Job, len(manifest.Monitors))
	//resCh := make(chan jobs.Result, len(manifest.Monitors))
	//ijobCh := make(chan jobs.Job, len(manifest.Monitors))
	//iresCh := make(chan jobs.Result, len(manifest.Monitors))
	//cjobCh := make(chan jobs.Job, len(manifest.Monitors))
	//cresCh := make(chan jobs.Result, len(manifest.Monitors))

	numWorkers := max(runtime.NumCPU()*2, len(manifest.Monitors)/100) // e.g., 8, 16, or 24

	// start workers pools
	pools := workerspool.NewPoolsManager()
	pools.NewPool("pulse", numWorkers, 1000000, 1000000)
	pools.NewPool("intervention", numWorkers, 1000000, 1000000)
	pools.NewPool("code", numWorkers, 1000000, 1000000)
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
	sched := systems.NewScheduler(&manifest, wg, 100*time.Millisecond)

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
	timeout := time.After(24 * time.Hour)
	//runtime.SetFinalizer(runtime.GC, func(x interface{}) {
	//	log.Println("Recovered in main")
	//})
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
