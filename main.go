package main

import (
	"cpra/internal/controller"
	"cpra/internal/controller/systems"
	"cpra/internal/jobs"
	"cpra/internal/loader/loader"
	"log"
	"time"
)

func main() {
	//file, err := os.Open("heap.prof")
	//f, err := os.Create("heap.prof")
	//if err != nil {
	//	log.Fatalf("could not create heap profile: %v", err)
	//}
	//// Make sure we close it on exit
	//defer f.Close()
	//
	//// Launch a background goroutine to dump the heap every N microseconds
	//go func() {
	//	ticker := time.NewTicker(500 * time.Microsecond)
	//	defer ticker.Stop()
	//	for range ticker.C {
	//		// Force a heap GC so the profile is fresh
	//		runtime.GC()
	//		if err := pprof.WriteHeapProfile(f); err != nil {
	//			log.Printf("could not write heap profile: %v", err)
	//			return
	//		}
	//		// Seek back to start so the file doesn't grow unbounded
	//		if _, err := f.Seek(0, 0); err != nil {
	//			log.Printf("could not rewind profile: %v", err)
	//			return
	//		}
	//	}
	//}()
	l := loader.NewLoader("yaml", "internal/loader/test.yaml")
	l.Load()
	m := l.GetManifest()
	//fmt.Printf("%#v", m)
	c, err := controller.NewCPRaWorld(&m)
	if err != nil {
		log.Fatal(err)
	}
	//x := generic.NewFilter4[components.DisabledMonitor, components.Name, components.PulseConfig, components.PulseStatus]()
	//query := x.Query(c.Mappers.World)
	//
	//for query.Next() {
	//	_, n, c, _ := query.Get()
	//	fmt.Printf("the following monitor is disabled %v -- %s\n", *n, c.Type)
	//}
	jobChan := make(chan jobs.Job, len(m.Monitors))
	resultChan := make(chan jobs.Result, len(m.Monitors))
	ijobChan := make(chan jobs.Job, len(m.Monitors))
	iresultChan := make(chan jobs.Result, len(m.Monitors))
	cjobChan := make(chan jobs.Job, len(m.Monitors))
	cresultChan := make(chan jobs.Result, len(m.Monitors))

	// 2) build your Scheduler
	sched := systems.Scheduler{
		World:       c,
		JobChan:     jobChan,
		ResultChan:  resultChan,
		IJobChan:    ijobChan,
		IResultChan: iresultChan,
		CJobChan:    cjobChan,
		CResultChan: cresultChan,
		Done:        make(chan struct{}),
	}

	// 3) register all your systems
	sched.AddSystem(&systems.PulseScheduleSystem{})
	sched.AddSystem(&systems.PulseDispatchSystem{JobChan: jobChan})
	sched.AddSystem(&systems.PulseResultSystem{ResultChan: resultChan})
	sched.AddSystem(&systems.InterventionDispatchSystem{JobChan: ijobChan})
	sched.AddSystem(&systems.InterventionResultSystem{ResultChan: iresultChan})
	sched.AddSystem(&systems.CodeDispatchSystem{JobChan: cjobChan})
	sched.AddSystem(&systems.CodeResultSystem{ResultChan: cresultChan})

	// 4) run — this will internally drain all job/result channels,
	//    Execute() each job, then call Update() on every system, in one loop.
	go sched.Run(10 * time.Millisecond)

	// 5) shut down when you like
	time.Sleep(24 * time.Hour)
	close(sched.Done)

}
