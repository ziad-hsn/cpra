package systems

import (
	"time"

	"cpra/internal/scheduler"

	"github.com/mlange-42/ark/ecs"
)

// PulseScheduler tracks due checks and the ready queue. The controller loop
// owns both; they must not be accessed concurrently.
type PulseScheduler struct {
	heap  *scheduler.Scheduler
	ready ReadyQueue
}

// NewPulseScheduler returns an empty PulseScheduler.
func NewPulseScheduler() *PulseScheduler {
	return &PulseScheduler{heap: scheduler.New()}
}

// Schedule inserts an entity in the scheduler to become due at the given time.
func (p *PulseScheduler) Schedule(entity ecs.Entity, due time.Time) {
	p.heap.Schedule(entity, due)
}

// Due pops and returns all entities due at or before now from the scheduler.
func (p *PulseScheduler) Due(now time.Time) []ecs.Entity {
	return p.heap.Due(now)
}

// EnqueueReady appends entities to the ready queue for the dispatch system.
func (p *PulseScheduler) EnqueueReady(entities []ecs.Entity) {
	p.ready.Enqueue(entities)
}

// ConsumeReady removes and returns up to n entities from the front of the ready queue.
func (p *PulseScheduler) ConsumeReady(n int) []ecs.Entity {
	return p.ready.Consume(n)
}

// Requeue re-appends entities to the ready queue (used when dispatch fails and
// the entities must be retried on a later tick).
func (p *PulseScheduler) Requeue(entities []ecs.Entity) {
	p.ready.Enqueue(entities)
}

// Len returns the number of scheduled (not yet due) entities in the scheduler.
func (p *PulseScheduler) Len() int { return p.heap.Len() }

// ReadyLen returns the number of entities awaiting dispatch.
func (p *PulseScheduler) ReadyLen() int { return p.ready.Len() }
