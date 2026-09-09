package systems

import (
	"time"

	"cpra/internal/scheduler"

	"github.com/mlange-42/ark/ecs"
)

// CodeScheduler holds the scheduling state for the code/alert pipeline: a
// min-heap of deferred-alert release times (NotBefore) and a ready queue of
// entities awaiting dispatch.
//
// It replaces the O(N)-per-tick scans in BatchCodeScheduleSystem and
// BatchCodeSystem with O(log N) insert and O(M log N) extraction. The heap is
// owned by the result systems (which defer alerts) and the code schedule
// system (which releases them); the ready queue is owned by the code schedule
// and code dispatch systems.
type CodeScheduler struct {
	heap   *scheduler.Scheduler
	ready  ReadyQueue
	active map[ecs.Entity]struct{}
}

// NewCodeScheduler returns an empty CodeScheduler.
func NewCodeScheduler() *CodeScheduler {
	return &CodeScheduler{heap: scheduler.New(), active: make(map[ecs.Entity]struct{})}
}

// ScheduleDeferred inserts an entity into the heap to be released at notBefore.
func (c *CodeScheduler) ScheduleDeferred(entity ecs.Entity, notBefore time.Time) {
	c.heap.Schedule(entity, notBefore)
}

// Due pops and returns all entities whose release time is at or before now.
func (c *CodeScheduler) Due(now time.Time) []ecs.Entity {
	return c.heap.Due(now)
}

// EnqueueReady appends entities to the ready queue for the code dispatch system.
func (c *CodeScheduler) EnqueueReady(entities []ecs.Entity) {
	c.ready.Enqueue(entities)
}

// ConsumeReady removes and returns up to n entities from the front of the ready queue.
func (c *CodeScheduler) ConsumeReady(n int) []ecs.Entity {
	return c.ready.Consume(n)
}

// Requeue re-appends entities to the ready queue (used when dispatch fails).
func (c *CodeScheduler) Requeue(entities []ecs.Entity) {
	c.ready.Enqueue(entities)
}

// Len returns the number of deferred alerts in the heap.
func (c *CodeScheduler) Len() int { return c.heap.Len() }

// ReadyLen returns the number of entities awaiting dispatch.
func (c *CodeScheduler) ReadyLen() int { return c.ready.Len() }
