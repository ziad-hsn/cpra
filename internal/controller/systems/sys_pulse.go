package systems

import (
	"cpra/internal/controller"
	"cpra/internal/controller/components"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"time"
)

func FirstPulseSystem(world *controller.CPRaWorld) {

	filter := generic.NewFilter2[components.PulseFirstCheck, components.PulseJob]().Without(generic.T[components.DisabledMonitor]())
	query := filter.Query(world.Mappers.World)

	exchange := generic.NewExchange(world.Mappers.World).
		Adds(generic.T[components.PulsePending]()).
		Removes(
			generic.T[components.PulseFirstCheck](),
		)
	var toExchange []ecs.Entity

	for query.Next() {
		_, job := query.Get()
		entity := query.Entity()

		go job.Job.Execute()

		toExchange = append(toExchange, entity)
	}
	for _, entity := range toExchange {
		_, status := world.Mappers.Pulse.Get(entity)
		exchange.Exchange(entity)

		status.LastStatus = "Pending"
		status.LastCheckTime = time.Now()
		status.LastError = nil
	}

}
