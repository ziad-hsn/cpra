package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"fmt"
	"github.com/mlange-42/arche/generic"
	"log"
)

func FirstPulse(world *controller.CPRaWorld) {

	filter := generic.NewFilter4[components.PulseFirstCheck, components.PulseConfig, components.PulseStatus, components.PulseJob]()

	query := filter.Query(world.Mappers.World)

	for query.Next() {
		entity := query.Entity()
		_, config, status, job := query.Get()
		job.Job.Execute()
		err := world.Mappers.MarkAsPending(entity)
		if err != nil {
			log.Printf("error: %v\n", err)
		}
		fmt.Printf("max failures = %d, last Job ID=%v\n", config.MaxFailures, status.LastJobID)
	}

}
