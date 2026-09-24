package systems

import "github.com/mlange-42/ark/ecs"

// ReadyQueue is a FIFO queue of entities awaiting dispatch by a dispatch system.
// It is owned by the producing system (which enqueues) and the consuming system
// (which dequeues). All access happens inside the single-threaded ECS tick, so
// no locks are required.
type ReadyQueue struct {
	items      []readyItem
	positions  map[ecs.Entity]int
	free       []int
	head, tail int
}

type readyItem struct {
	entity         ecs.Entity
	previous, next int
}

// Enqueue appends entities to the queue.
func (q *ReadyQueue) Enqueue(entities []ecs.Entity) {
	if q.positions == nil {
		q.positions = make(map[ecs.Entity]int)
		q.head, q.tail = -1, -1
	}
	for _, entity := range entities {
		if _, ok := q.positions[entity]; ok {
			continue
		}
		index := len(q.items)
		if last := len(q.free) - 1; last >= 0 {
			index = q.free[last]
			q.free = q.free[:last]
		} else {
			q.items = append(q.items, readyItem{})
		}
		q.items[index] = readyItem{entity: entity, previous: q.tail, next: -1}
		if q.tail >= 0 {
			q.items[q.tail].next = index
		} else {
			q.head = index
		}
		q.tail = index
		q.positions[entity] = index
	}
}

// Consume removes and returns up to n entities from the front of the queue.
func (q *ReadyQueue) Consume(n int) []ecs.Entity {
	n = min(max(0, n), len(q.positions))
	if n == 0 {
		return nil
	}
	out := make([]ecs.Entity, 0, n)
	for len(out) < n {
		entity := q.items[q.head].entity
		out = append(out, entity)
		q.Cancel(entity)
	}
	return out
}

// Cancel removes one full entity identity in O(1). Slots are recycled so a
// repeatedly edited or paused monitor cannot accumulate stale ready entries.
func (q *ReadyQueue) Cancel(entity ecs.Entity) bool {
	index, ok := q.positions[entity]
	if !ok {
		return false
	}
	item := q.items[index]
	if item.previous >= 0 {
		q.items[item.previous].next = item.next
	} else {
		q.head = item.next
	}
	if item.next >= 0 {
		q.items[item.next].previous = item.previous
	} else {
		q.tail = item.previous
	}
	delete(q.positions, entity)
	q.items[index] = readyItem{}
	q.free = append(q.free, index)
	return true
}

// Len returns the number of entities in the queue.
func (q *ReadyQueue) Len() int { return len(q.positions) }
