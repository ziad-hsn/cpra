package main

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/controller/systems"
	"cpra/internal/jobs"
	"cpra/internal/loader/loader"
	"fmt"
	"github.com/mlange-42/arche/generic"
	"log"
	"sync"
	"time"
)

func main() {

	l := loader.NewLoader("yaml", "internal/loader/test.yaml")
	l.Load()
	m := l.GetManifest()
	//fmt.Printf("%#v", m)
	c, err := controller.NewCPRaWorld(&m)
	if err != nil {
		log.Fatal(err)
	}
	x := generic.NewFilter4[components.DisabledMonitor, components.Name, components.PulseConfig, components.PulseStatus]()
	query := x.Query(c.Mappers.World)

	for query.Next() {
		_, n, c, _ := query.Get()
		fmt.Printf("the following monitor is disabled %v -- %s\n", *n, c.Type)
	}
	jobChan := make(chan jobs.Job, len(m.Monitors))
	resultChan := make(chan jobs.Result, len(m.Monitors))
	ijobChan := make(chan jobs.Job, len(m.Monitors))
	iresultChan := make(chan jobs.Result, len(m.Monitors))
	cjobChan := make(chan jobs.Job, len(m.Monitors))
	cresultChan := make(chan jobs.Result, len(m.Monitors))
	schedulerWG := &sync.WaitGroup{}
	s := systems.Scheduler{Systems: make([]systems.System, 0), WG: schedulerWG, JobChan: jobChan, ResultChan: resultChan, World: *c, Done: make(chan struct{})}
	s.AddSystem(&systems.PulseScheduleSystem{})
	s.AddSystem(&systems.PulseDispatchSystem{
		JobChan: jobChan,
	})
	s.AddSystem(&systems.PulseResultSystem{ResultChan: resultChan})
	s.AddSystem(&systems.InterventionDispatchSystem{JobChan: ijobChan})
	s.AddSystem(&systems.InterventionResultSystem{ResultChan: iresultChan})
	s.AddSystem(&systems.CodeDispatchSystem{JobChan: cjobChan})
	s.AddSystem(&systems.CodeResultSystem{ResultChan: cresultChan})
	schedulerWG.Add(1)
	go s.Run(10 * time.Microsecond)
	timeout := time.After(2 * time.Hour)

	for {
		select {
		case job, ok := <-jobChan:
			if !ok {
				fmt.Println("existing CPRa")
				return
			}
			res := job.Execute()
			resultChan <- res
		case job, ok := <-ijobChan:
			if !ok {
				fmt.Println("existing CPRa")
				return
			}
			res := job.Execute()
			iresultChan <- res
		case job, ok := <-cjobChan:
			fmt.Println("code code job")
			if !ok {
				fmt.Println("existing CPRa")
				return
			}
			res := job.Execute()
			cresultChan <- res
		case <-timeout:
			fmt.Println("timeout")
			close(s.Done)
			schedulerWG.Wait()
			return
		}

	}

	//start := time.Now()
	//timer := time.After(3 * time.Second)
	//for {
	//	select {
	//	case <-timer:
	//		fmt.Printf("timeout after: %v\n", time.Since(start))
	//		return
	//	default:
	//		fmt.Println(time.Since(start))
	//		systems.FirstPulseSystem(c)
	//	}
	//
	//}
	//var client http.Client
	//worker := systems.SimpleWorker{}
	//monitorsNum := len(m.Monitors)
	//workersPool := systems.CreateWorkersPool(monitorsNum, 100, &worker)
	//go workersPool.Run()
	//<-workersPool.Started
	//for _, monitor := range m.Monitors {
	//	switch monitor.Pulse.Type {
	//	case "http":
	//		cfg, ok := monitor.Pulse.Config.(*schema.PulseHTTPConfig)
	//		if !ok {
	//			log.Fatal("error in pulse config")
	//		}
	//		workersPool.Jobs <- systems.PulseHTTPJob{
	//			Timeout: monitor.Pulse.Timeout,
	//			Count: monitor.Pulse.Count,
	//			Config:  *cfg,
	//			Client:  &client,
	//		}
	//		time.Sleep(time.Second * 2)
	//	case "tcp":
	//		cfg, ok := monitor.Pulse.Config.(*schema.PulseTCPConfig)
	//		if !ok {
	//			log.Fatal("error in pulse config")
	//		}
	//		workersPool.Jobs <- systems.PulseTCPJob{
	//			Timeout: monitor.Pulse.Timeout,
	//			Count: monitor.Pulse.Count,
	//			Config:  *cfg,
	//		}
	//	}
	//
	//}
	//workersPool.WG.Wait()
}
