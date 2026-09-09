// Package scheduler stores due checks in a timing wheel with a heap for
// entries beyond the wheel span.
package scheduler

import (
	"time"

	"github.com/mlange-42/ark/ecs"
)

// entry is a scheduled monitor: the time it is next due and its entity.
// due is stored as UnixNano (int64) so comparisons are a single integer
// compare, and so the entry is 16 bytes.
type entry struct {
	due    int64
	entity ecs.Entity
}

// minHeap is a min-heap of entries ordered by due time (earliest first). It is
// retained only as the overflow structure for entries beyond the wheel's span.
type minHeap []entry

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].due < h[j].due }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

// push appends e and sifts it up to restore the min-heap invariant.
func (h *minHeap) push(e entry) {
	*h = append(*h, e)
	h.siftUp(len(*h) - 1)
}

// pop removes and returns the root (earliest due) entry.
func (h *minHeap) pop() entry {
	old := *h
	n := len(old)
	x := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	if n-1 > 0 {
		h.siftDown(0)
	}
	return x
}

// siftUp moves the element at i up toward the root until the invariant holds.
func (h *minHeap) siftUp(i int) {
	items := *h
	for i > 0 {
		parent := (i - 1) / 2
		if items[i].due >= items[parent].due {
			break
		}
		items[i], items[parent] = items[parent], items[i]
		i = parent
	}
}

// siftDown moves the element at i down toward the leaves until the invariant holds.
func (h *minHeap) siftDown(i int) {
	items := *h
	n := len(items)
	for {
		left := 2*i + 1
		if left >= n {
			break
		}
		child := left
		if right := left + 1; right < n && items[right].due < items[left].due {
			child = right
		}
		if items[child].due >= items[i].due {
			break
		}
		items[i], items[child] = items[child], items[i]
		i = child
	}
}

// Wheel constants. resolution is a power of two (1<<20 ns = 1.048576ms) so the
// slot index is a shift, and numSlots is a power of two so the modulo is a mask.
// The span is numSlots * resolution = 8192 * 1.048576ms ~= 8.59s, which covers
// the common 1s monitor interval (and intervals up to ~8.5s) without overflow.
const (
	wheelResolution = int64(1 << 20) // 1.048576ms per slot
	wheelNumSlots   = 8192           // span ~= 8.59s
	wheelMask       = wheelNumSlots - 1
)

// timingWheel is a hashed timing wheel: a circular array of buckets, each
// holding entries due within one resolution tick. tick is the next slot to
// pop (in resolution units). Entries due more than one full span ahead go to
// the overflow min-heap.
type timingWheel struct {
	slots    [][]entry
	tick     int64
	overflow minHeap
	count    int
}

func newTimingWheel() *timingWheel {
	return &timingWheel{
		slots: make([][]entry, wheelNumSlots),
		// Anchor the wheel to the current time so the first insert lands in a
		// slot rather than the overflow heap (tick starts at 0 otherwise, and
		// every due time is ~1.6e12 slots ahead of it).
		tick: time.Now().UnixNano() / wheelResolution,
	}
}

// insert places e in a wheel bucket or the overflow heap.
func (w *timingWheel) insert(e entry) {
	slot := e.due / wheelResolution
	if slot < w.tick {
		// Overdue: clamp to the next slot to pop so it is returned immediately.
		slot = w.tick
	}
	if slot >= w.tick+wheelNumSlots {
		// Beyond the wheel's span: overflow to the min-heap.
		w.overflow.push(e)
		w.count++
		return
	}
	idx := slot & wheelMask
	w.slots[idx] = append(w.slots[idx], e)
	w.count++
}

// popDue pops and returns the entities of all entries due at or before now.
// It visits elapsed wheel slots and removes due entries from the overflow heap.
func (w *timingWheel) popDue(now int64) []ecs.Entity {
	currentTick := now / wheelResolution
	var due []ecs.Entity
	for w.tick <= currentTick {
		idx := w.tick & wheelMask
		if n := len(w.slots[idx]); n > 0 {
			for _, e := range w.slots[idx] {
				due = append(due, e.entity)
			}
			w.slots[idx] = w.slots[idx][:0]
		}
		w.tick++
	}
	for w.overflow.Len() > 0 && w.overflow[0].due <= now {
		due = append(due, w.overflow.pop().entity)
	}
	w.count -= len(due)
	return due
}

// len returns the number of scheduled entries.
func (w *timingWheel) len() int { return w.count }

// peek returns the earliest due time without removing it. It scans the wheel
// (used only by tests; production uses Due).
func (w *timingWheel) peek() (int64, bool) {
	var earliest int64
	found := false
	for i := 0; i < wheelNumSlots; i++ {
		idx := (w.tick + int64(i)) & wheelMask
		for _, e := range w.slots[idx] {
			if !found || e.due < earliest {
				earliest = e.due
				found = true
			}
		}
	}
	if w.overflow.Len() > 0 && (!found || w.overflow[0].due < earliest) {
		earliest = w.overflow[0].due
		found = true
	}
	return earliest, found
}

// Scheduler schedules monitors by their next-check time. Wheel entries are
// resolved to wheelResolution; entries beyond the wheel span use the heap.
// Callers must serialize access.
type Scheduler struct {
	wheel *timingWheel
}

// New returns an empty Scheduler.
func New() *Scheduler {
	return NewSharded(0)
}

// NewSharded returns an empty Scheduler. The shard count is accepted for API
// compatibility but is unused. The scheduler uses one timing wheel.
func NewSharded(numShards int) *Scheduler {
	return &Scheduler{wheel: newTimingWheel()}
}

// Schedule inserts entity to become due at the given time.
func (s *Scheduler) Schedule(entity ecs.Entity, due time.Time) {
	s.wheel.insert(entry{due: due.UnixNano(), entity: entity})
}

// Due pops and returns the entities of all monitors due at or before now.
// Times within the current wheel tick are included.
func (s *Scheduler) Due(now time.Time) []ecs.Entity {
	return s.wheel.popDue(now.UnixNano())
}

// Len returns the number of scheduled monitors.
func (s *Scheduler) Len() int {
	return s.wheel.len()
}

// Peek returns the earliest due time without removing it.
func (s *Scheduler) Peek() (time.Time, bool) {
	earliest, found := s.wheel.peek()
	return time.Unix(0, earliest), found
}
