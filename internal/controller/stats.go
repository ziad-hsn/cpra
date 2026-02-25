package controller

import (
	"cpra/internal/runtime/queue"

	"github.com/mlange-42/ark/ecs/stats"
)

// Stats ControllerStats aggregates runtime statistics for queues, worker pools, and the ECS world.
type Stats struct {
	PulseQueue          queue.Stats           `json:"pulse_queue"`
	InterventionQueue   queue.Stats           `json:"intervention_queue"`
	CodeQueue           queue.Stats           `json:"code_queue"`
	PulseWorkers        queue.WorkerPoolStats `json:"pulse_workers"`
	InterventionWorkers queue.WorkerPoolStats `json:"intervention_workers"`
	CodeWorkers         queue.WorkerPoolStats `json:"code_workers"`
	World               *stats.World          `json:"world"`
}

// Stats return a snapshot of controller runtime statistics.
func (c *Controller) Stats() Stats {
	qStats := c.queues.Stats()
	pStats := c.pools.Stats()
	return Stats{
		PulseQueue:          qStats.Pulse,
		InterventionQueue:   qStats.Intervention,
		CodeQueue:           qStats.Code,
		PulseWorkers:        pStats.Pulse,
		InterventionWorkers: pStats.Intervention,
		CodeWorkers:         pStats.Code,
		World:               c.world.Stats(),
	}
}
