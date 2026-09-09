package systems

import "github.com/mlange-42/ark/ecs"

// ReadyQueue is a FIFO queue of entities awaiting dispatch by a dispatch system.
// It is owned by the producing system (which enqueues) and the consuming system
// (which dequeues). All access happens inside the single-threaded ECS tick, so
// no locks are required.
type ReadyQueue struct {
	entities []ecs.Entity
}

// Enqueue appends entities to the queue.
func (q *ReadyQueue) Enqueue(entities []ecs.Entity) {
	q.entities = append(q.entities, entities...)
}

// Consume removes and returns up to n entities from the front of the queue.
func (q *ReadyQueue) Consume(n int) []ecs.Entity {
	if n > len(q.entities) {
		n = len(q.entities)
	}
	out := q.entities[:n]
	q.entities = q.entities[n:]
	return out
}

// Len returns the number of entities in the queue.
func (q *ReadyQueue) Len() int { return len(q.entities) }
