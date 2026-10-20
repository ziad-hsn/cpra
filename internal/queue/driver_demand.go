package queue

import (
	"errors"
	"math"
)

// DriverDemand is a bounded owner-loop summary of eligible local checks.
// Occupancy is the sum of min(1, timeout/interval), not an observed mean.
type DriverDemand struct {
	Driver      string
	Monitors    int
	ArrivalRate float64
	Occupancy   float64
}

func (q *FairQueue) SetDemand(demand []DriverDemand) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(demand) > len(q.order) {
		return errors.New("too many demand drivers")
	}
	seen := make(map[string]bool, len(demand))
	for _, d := range demand {
		if q.drivers[d.Driver] == nil || seen[d.Driver] || d.Monitors < 0 || d.Monitors > 2_000_000 || d.Occupancy < 0 || d.Occupancy > float64(d.Monitors) || math.IsNaN(d.Occupancy) || math.IsInf(d.Occupancy, 0) || d.ArrivalRate < 0 || math.IsNaN(d.ArrivalRate) || math.IsInf(d.ArrivalRate, 0) || d.Monitors == 0 && (d.Occupancy != 0 || d.ArrivalRate != 0) {
			return errors.New("invalid driver demand")
		}
		seen[d.Driver] = true
	}
	for _, d := range q.order {
		d.demand = DriverDemand{Driver: d.name}
		d.reservation = 0
	}
	for _, demand := range demand {
		d := q.drivers[demand.Driver]
		d.demand = demand
		if demand.Monitors > 0 {
			d.reservation = max(1, int(math.Ceil(demand.Occupancy*1.15)))
		}
	}
	q.notify()
	return nil
}

func (q *FairQueue) requestedCapacity() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.requestedCapacityLocked()
}

func (q *FairQueue) requestedCapacityLocked() int {
	total := 0
	for _, d := range q.order {
		total += d.reservation
	}
	return total
}

// reservations apportions a bounded budget. Every configured driver receives
// one seat when possible. Under a smaller budget the first eligible driver
// rotates after every dispatch; tiny shares cannot round to zero forever.
// Accepted work from a removed configuration retains a draining seat.
func (q *FairQueue) reservations(capacity int, admission bool) (out [fairDriverLimit]int) {
	out = q.demandReservations(capacity, admission)
	if admission {
		return out
	}
	used, active := 0, 0
	for i, d := range q.order {
		used += max(d.active, out[i])
		active += d.active
	}
	if used <= capacity {
		return out
	}
	// Borrowed or draining leases cannot be preempted. Allocate only free
	// seats; otherwise an unreserved active driver can be counted in addition
	// to a full reservation budget and strand a usable worker.
	var deficits [fairDriverLimit]int
	for i, d := range q.order {
		deficits[i] = max(0, out[i]-d.active)
		out[i] = min(out[i], d.active)
	}
	remaining := max(0, capacity-active)
	for _, pending := range []bool{true, false} {
		for n := range len(q.order) {
			i := (q.next + n) % len(q.order)
			if (q.order[i].depth > 0) == pending && deficits[i] > 0 && remaining > 0 {
				out[i]++
				deficits[i]--
				remaining--
			}
		}
	}
	// Give each remaining requested share its proportional part. Iteration is
	// bounded by registered drivers, never by worker count or fleet size.
	total := 0
	for _, n := range deficits {
		total += n
	}
	budget := remaining
	for i, n := range deficits {
		if n == 0 || total == 0 {
			continue
		}
		share := min(n, int(int64(budget)*int64(n)/int64(total)))
		out[i] += share
		deficits[i] -= share
		remaining -= share
	}
	for n := range len(q.order) {
		i := (q.next + n) % len(q.order)
		if deficits[i] > 0 && remaining > 0 {
			out[i]++
			remaining--
		}
	}
	return out
}

func (q *FairQueue) demandReservations(capacity int, admission bool) (out [fairDriverLimit]int) {
	if capacity <= 0 {
		return out
	}
	var weights [fairDriverLimit]int
	count, total := 0, 0
	for i, d := range q.order {
		weight := d.reservation
		if admission {
			weight = d.demand.Monitors
		}
		if weight == 0 && (d.depth > 0 || d.active > 0) {
			weight = 1
		}
		weights[i] = weight
		if weight > 0 {
			count++
			total += weight
		}
	}
	if total <= capacity && !admission {
		return weights
	}
	remaining := capacity
	if capacity < count {
		// It is impossible to protect every configured idle driver. Rotate only
		// among waiting work, otherwise an idle driver's seat can prevent the
		// successful dispatch that would have advanced the rotation.
		for n := range len(q.order) {
			i := (q.next + n) % len(q.order)
			if weights[i] > 0 && q.order[i].depth > 0 && remaining > 0 {
				out[i] = 1
				remaining--
			}
		}
		return out
	}
	for n := range len(q.order) {
		i := (q.next + n) % len(q.order)
		if weights[i] > 0 && remaining > 0 {
			out[i] = 1
			remaining--
		}
	}
	if remaining == 0 || total == count {
		return out
	}
	// Largest-remainder allocation consumes at most one additional pass over
	// the fixed driver set. Ties follow the rotating dispatch cursor.
	var fractions [fairDriverLimit]float64
	budget := remaining
	for i, weight := range weights[:len(q.order)] {
		if weight <= 1 {
			continue
		}
		share := float64(budget) * float64(weight-1) / float64(total-count)
		whole := int(math.Floor(share))
		out[i] += whole
		remaining -= whole
		fractions[i] = share - float64(whole)
	}
	for remaining > 0 {
		best := -1
		for n := range len(q.order) {
			i := (q.next + n) % len(q.order)
			if weights[i] > 0 && (best == -1 || fractions[i] > fractions[best]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		out[best]++
		fractions[best] = -1
		remaining--
	}
	return out
}

func (q *FairQueue) admissionLimit(d *fairDriver) int {
	// At least one slot remains available for a newly active destination or
	// driver. Existing accepted work is never discarded when demand changes.
	limits := q.reservations(q.capacity, true)
	reservedOthers := 0
	for i, other := range q.order {
		if other != d {
			reservedOthers += limits[i]
		}
	}
	return max(1, q.capacity-reservedOthers)
}

func (q *FairQueue) executionLimitFor(d *fairDriver, capacity int, reserved [fairDriverLimit]int) int {
	others := 0
	for i, other := range q.order {
		if other != d {
			others += max(other.active, reserved[i])
		}
	}
	return max(0, capacity-others)
}
