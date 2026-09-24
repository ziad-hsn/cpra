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

// location makes replacement/cancellation bounded by one bucket removal or one
// indexed heap removal. Full ecs.Entity identity includes the recycled-ID epoch.
type location struct{ slot, index int }

type minHeap struct {
	entries   []entry
	locations map[ecs.Entity]location
}

func (h minHeap) Len() int           { return len(h.entries) }
func (h minHeap) Less(i, j int) bool { return h.entries[i].due < h.entries[j].due }
func (h minHeap) Swap(i, j int) {
	h.entries[i], h.entries[j] = h.entries[j], h.entries[i]
	h.locations[h.entries[i].entity] = location{-1, i}
	h.locations[h.entries[j].entity] = location{-1, j}
}
func (h *minHeap) push(e entry) {
	h.locations[e.entity] = location{-1, len(h.entries)}
	h.entries = append(h.entries, e)
	h.up(len(h.entries) - 1)
}
func (h *minHeap) remove(index int) entry {
	n := len(h.entries) - 1
	h.Swap(index, n)
	e := h.entries[n]
	h.entries[n] = entry{}
	h.entries = h.entries[:n]
	delete(h.locations, e.entity)
	if index < n {
		if index > 0 && h.Less(index, (index-1)/2) {
			h.up(index)
		} else {
			h.down(index)
		}
	}
	return e
}
func (h *minHeap) up(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !h.Less(i, parent) {
			return
		}
		h.Swap(i, parent)
		i = parent
	}
}
func (h *minHeap) down(i int) {
	for {
		child := i*2 + 1
		if child >= len(h.entries) {
			return
		}
		if child+1 < len(h.entries) && h.Less(child+1, child) {
			child++
		}
		if !h.Less(child, i) {
			return
		}
		h.Swap(i, child)
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
	slots     [][]entry
	tick      int64
	overflow  minHeap
	count     int
	locations map[ecs.Entity]location
}

func newTimingWheel() *timingWheel {
	w := &timingWheel{
		slots: make([][]entry, wheelNumSlots),
		// Anchor the wheel to the current time so the first insert lands in a
		// slot rather than the overflow heap (tick starts at 0 otherwise, and
		// every due time is ~1.6e12 slots ahead of it).
		tick:      time.Now().UnixNano() / wheelResolution,
		locations: make(map[ecs.Entity]location),
	}
	w.overflow.locations = w.locations
	return w
}

// insert places e in a wheel bucket or the overflow heap.
func (w *timingWheel) insert(e entry) {
	w.cancel(e.entity)
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
	w.locations[e.entity] = location{int(idx), len(w.slots[idx])}
	w.slots[idx] = append(w.slots[idx], e)
	w.count++
}

func (w *timingWheel) cancel(entity ecs.Entity) bool {
	loc, ok := w.locations[entity]
	if !ok {
		return false
	}
	if loc.slot < 0 {
		w.overflow.remove(loc.index)
	} else {
		bucket := w.slots[loc.slot]
		last := len(bucket) - 1
		if loc.index != last {
			bucket[loc.index] = bucket[last]
			w.locations[bucket[loc.index].entity] = location{loc.slot, loc.index}
		}
		bucket[last] = entry{}
		w.slots[loc.slot] = bucket[:last]
		delete(w.locations, entity)
	}
	w.count--
	return true
}

// popDue pops and returns the entities of all entries due at or before now.
// It visits elapsed wheel slots and removes due entries from the overflow heap.
func (w *timingWheel) popDue(now int64) []ecs.Entity {
	currentTick := now / wheelResolution
	var due []ecs.Entity
	// Every wheel entry is within one wheel span of tick. After a host sleep,
	// visit each bucket at most once instead of iterating every elapsed tick.
	end := min(currentTick, w.tick+wheelNumSlots-1)
	for w.tick <= end {
		idx := w.tick & wheelMask
		if n := len(w.slots[idx]); n > 0 {
			for _, e := range w.slots[idx] {
				due = append(due, e.entity)
				delete(w.locations, e.entity)
			}
			clear(w.slots[idx])
			w.slots[idx] = w.slots[idx][:0]
		}
		w.tick++
	}
	w.tick = max(w.tick, currentTick+1)
	for w.overflow.Len() > 0 && w.overflow.entries[0].due <= now {
		due = append(due, w.overflow.remove(0).entity)
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
	if w.overflow.Len() > 0 && (!found || w.overflow.entries[0].due < earliest) {
		earliest = w.overflow.entries[0].due
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

// Schedule inserts or replaces the entity's one due time.
func (s *Scheduler) Schedule(entity ecs.Entity, due time.Time) {
	s.wheel.insert(entry{due: due.UnixNano(), entity: entity})
}

// Cancel removes an entity's indexed due entry without a fleet scan.
func (s *Scheduler) Cancel(entity ecs.Entity) bool { return s.wheel.cancel(entity) }

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
