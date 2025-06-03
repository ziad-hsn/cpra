package main

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"cpra/internal/controller/systems"
	"cpra/internal/loader/loader"
	"fmt"
	"github.com/mlange-42/arche/generic"
	"log"
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

	for {
		systems.FirstPulse(c)
	}
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
