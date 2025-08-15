package main

import (
	"cpra/internal/controller"
	"cpra/internal/controller/systems"
	"cpra/internal/loader/loader"
	"cpra/internal/workers/workerspool"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/mlange-42/ark-tools/app"
	ss "github.com/mlange-42/ark-tools/system"
)

func main() {

	//defer pprof.StopCPUProfile()
	//runtime.GOMAXPROCS(24)
	debug.SetGCPercent(20)
	//debug.SetMemoryLimit(1024 * 1024 * 1024 * 1024)

	f, err := os.OpenFile("crash-latest.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
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

	numWorkers := max(runtime.NumCPU()*2, len(manifest.Monitors)/1000) // e.g., 8, 16, or 24

	// start workers pools
	pools := workerspool.NewPoolsManager()
	pools.NewPool("pulse", numWorkers, 65536, 65536)
	pools.NewPool("intervention", numWorkers, 4096, 4096)
	pools.NewPool("code", numWorkers, 4096, 4096)

	pulseJobChan, err := pools.GetJobChannel("pulse")
	if err != nil {
		log.Fatal(err)
	}
	//interventionJobChan, err := pools.GetJobChannel("intervention")
	//if err != nil {
	//	log.Fatal(err)
	//}
	//CodeJobChan, err := pools.GetJobChannel("code")
	//if err != nil {
	//	log.Fatal(err)
	//}
	pulseResultChan, err := pools.GetResultChannel("pulse")
	if err != nil {
		log.Fatal(err)
	}
	//interventionResultChan, err := pools.GetResultChannel("intervention")
	//if err != nil {
	//	log.Fatal(err)
	//}
	//codeResultChan, err := pools.GetResultChannel("code")
	//if err != nil {
	//	log.Fatal(err)
	//}
	pools.StartAll()
	// build scheduler
	wg := &sync.WaitGroup{}
	// Create a new, seeded tool.
	tool := app.New(1024).Seed(123)
	// Limit simulation speed.
	tool.TPS = 30

	_, err = controller.NewCPRaWorld(&manifest, &tool.World)
	//_ := entities.InitializeMappers(&tool.World)
	tool.AddSystem(&systems.PulseResultSystem{
		ResultChan: pulseResultChan,
	})
	tool.AddSystem(&systems.PulseScheduleSystem{})
	tool.AddSystem(&systems.PulseDispatchSystem{
		JobChan: pulseJobChan,
	})
	tool.AddSystem(&ss.PerfTimer{
		UpdateInterval: int(10 * time.Microsecond),
	})
	// Add a termination system that ends the simulation.

	//scheduler := systems.NewScheduler(&manifest, wg, 100*time.Millisecond, &tool.World)
	//
	//// Phase 1
	////scheduler.AddSchedule(&systems.PulseScheduleSystem{})
	//
	//// Phase 2
	//scheduler.AddDispatch(&systems.PulseDispatchSystem{JobChan: pulseJobChan})
	//scheduler.AddDispatch(&systems.InterventionDispatchSystem{JobChan: interventionJobChan})
	//scheduler.AddDispatch(&systems.CodeDispatchSystem{JobChan: CodeJobChan})
	//
	//// Phase 3
	//scheduler.AddResult(&systems.PulseResultSystem{ResultChan: pulseResultChan})
	//scheduler.AddResult(&systems.InterventionResultSystem{ResultChan: interventionResultChan})
	//scheduler.AddResult(&systems.CodeResultSystem{ResultChan: codeResultChan})

	wg.Add(1)
	go tool.Run()
	timeout := time.After(24 * time.Hour)

	<-timeout
	fmt.Println("timeout")
	//close(scheduler.Done)
	tool.Finalize()
	wg.Wait()
	return

}
