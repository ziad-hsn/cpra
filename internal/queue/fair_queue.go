package queue

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
)

const fairDriverLimit = 64

// FairQueue partitions admission and execution across the configured driver set,
// including idle drivers. Reservations are not lent to non-preemptible work.
// Flows exist only while they have accepted pending or active work, so their
// count is bounded by queue capacity plus the global execution limit.
type FairQueue struct {
	mu                                            sync.Mutex
	drivers                                       map[string]*fairDriver
	order                                         []*fairDriver
	next, depth, active, capacity, executionLimit int
	closed                                        bool
	wakeup                                        chan struct{}
	started, lastEnqueue, lastDequeue             time.Time
	first, last                                   *fairEntry
	enqueued, dequeued, dropped                   int64
	totalWait, maxWait                            time.Duration
	arrivals                                      durationMetrics
}

type fairDriver struct {
	name                      string
	flows                     map[uint64]*fairFlow
	first                     *fairFlow // Circular list of flows with pending work only.
	depth, active             int
	dropped, timeouts         int64
	activeFirst, activeLast   *fairLease
	pendingFirst, pendingLast *fairEntry
}

type fairFlow struct {
	key            uint64
	first, last    *fairEntry
	active         int
	previous, next *fairFlow
}
type fairEntry struct {
	job                      jobs.Job
	at                       time.Time
	next, older, newer       *fairEntry
	driverOlder, driverNewer *fairEntry
}
type fairLease struct {
	jobs.Job
	queue          *FairQueue
	driver         *fairDriver
	flow           *fairFlow
	at             time.Time
	previous, next *fairLease
}

func (d *fairDriver) appendFlow(f *fairFlow) {
	if d.first == nil {
		f.previous = f
		f.next = f
		d.first = f
		return
	}
	last := d.first.previous
	last.next = f
	f.previous = last
	f.next = d.first
	d.first.previous = f
}
func (d *fairDriver) removeFlow(f *fairFlow) {
	if f.next == f {
		d.first = nil
	} else {
		f.previous.next = f.next
		f.next.previous = f.previous
		if d.first == f {
			d.first = f.next
		}
	}
	f.previous = nil
	f.next = nil
}
func (l *fairLease) SetContext(ctx context.Context) {
	if job, ok := l.Job.(interface{ SetContext(context.Context) }); ok {
		job.SetContext(ctx)
	}
}
func (l *fairLease) complete(result jobs.Result) {
	q := l.queue
	q.mu.Lock()
	d, f := l.driver, l.flow
	f.active--
	d.active--
	q.active--
	if f.active == 0 && f.first == nil {
		delete(d.flows, f.key)
	}
	if l.previous != nil {
		l.previous.next = l.next
	} else {
		d.activeFirst = l.next
	}
	if l.next != nil {
		l.next.previous = l.previous
	} else {
		d.activeLast = l.previous
	}
	if errors.Is(result.Err, context.DeadlineExceeded) || result.AttemptTimeouts > 0 {
		d.timeouts++
	}
	l.Job = nil
	l.previous = nil
	l.next = nil
	q.mu.Unlock()
	q.notify()
}

type DriverQueueStats struct {
	Driver           string        `json:"driver"`
	Pending          int           `json:"pending"`
	Active           int           `json:"active"`
	ConcurrencyLimit int           `json:"concurrency_limit"`
	AdmissionLimit   int           `json:"admission_limit"`
	OldestPendingAge time.Duration `json:"oldest_pending_age"`
	OldestActiveAge  time.Duration `json:"oldest_active_age"`
	Rejections       int64         `json:"rejections"`
	Timeouts         int64         `json:"timeouts"`
	Condition        string        `json:"condition"`
}

// NewFairQueue uses all compiled check drivers unless an explicit driver set is
// supplied. The set is immutable: catalog growth never steals an idle driver's
// reserved capacity. Optional drivers absent from the binary reserve nothing.
func NewFairQueue(capacity int, names ...string) (*FairQueue, error) {
	if len(names) == 0 {
		for _, c := range jobs.Capabilities() {
			if c.Kind == "check" && c.Available {
				names = append(names, c.Driver)
			}
		}
	}
	if len(names) == 0 || len(names) > fairDriverLimit || capacity < len(names) {
		return nil, errors.New("queue capacity must provide a slot for every configured driver")
	}
	q := &FairQueue{capacity: capacity, drivers: make(map[string]*fairDriver), wakeup: make(chan struct{}, 1), started: time.Now()}
	for _, name := range names {
		if name == "" || len(name) > 64 || q.drivers[name] != nil {
			return nil, errors.New("invalid or duplicate queue driver")
		}
		d := &fairDriver{name: name, flows: make(map[uint64]*fairFlow)}
		q.drivers[name] = d
		q.order = append(q.order, d)
	}
	return q, nil
}
func (q *FairQueue) partitions() int { return len(q.order) }
func (q *FairQueue) notify() {
	select {
	case q.wakeup <- struct{}{}:
	default:
	}
}
func (q *FairQueue) notifyChannel() <-chan struct{} { return q.wakeup }
func (q *FairQueue) Enqueue(job jobs.Job) error {
	if isNilJob(job) {
		return errors.New("nil job")
	}
	identity, ok := job.(interface{ QueueIdentity() (string, uint64) })
	if !ok {
		return errors.New("pulse queue requires a driver and flow identity")
	}
	driver, key := identity.QueueIdentity()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	d := q.drivers[driver]
	if d == nil {
		return errors.New("pulse queue driver is not registered")
	}
	if q.depth >= q.capacity || d.depth >= q.capacity/len(q.order) {
		q.dropped++
		d.dropped++
		return ErrQueueFull
	}
	now := time.Now()
	job.SetEnqueueTime(now)
	f := d.flows[key]
	if f == nil {
		f = &fairFlow{key: key}
		d.flows[key] = f
	}
	e := &fairEntry{job: job, at: now, older: q.last}
	if q.last != nil {
		q.last.newer = e
	} else {
		q.first = e
	}
	q.last = e
	e.driverOlder = d.pendingLast
	if d.pendingLast != nil {
		d.pendingLast.driverNewer = e
	} else {
		d.pendingFirst = e
	}
	d.pendingLast = e
	if f.last == nil {
		f.first = e
		d.appendFlow(f)
	} else {
		f.last.next = e
	}
	f.last = e
	d.depth++
	q.depth++
	q.enqueued++
	q.lastEnqueue = now
	q.arrivals.recordArrival()
	q.notify()
	return nil
}
func (q *FairQueue) EnqueueBatch(values []interface{}) error {
	for _, value := range values {
		job, ok := value.(jobs.Job)
		if !ok {
			return errors.New("invalid job type in batch")
		}
		if err := q.Enqueue(job); err != nil {
			return err
		}
	}
	return nil
}
func fairShare(capacity, count int) int {
	// Keep a seat for a later flow when only one flow exists and capacity permits.
	if count <= 1 {
		return max(1, capacity-1)
	}
	return max(1, capacity/count)
}
func (q *FairQueue) acquire(capacity int) (jobs.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.executionLimit = capacity
	return q.take(capacity, true)
}
func (q *FairQueue) take(capacity int, reserve bool) (jobs.Job, error) {
	if q.depth == 0 {
		if q.closed {
			return nil, ErrQueueClosed
		}
		return nil, nil
	}
	if reserve && (capacity <= 0 || q.active >= capacity) {
		return nil, nil
	}
	driverLimit := max(1, capacity/len(q.order))
	for n := 0; n < len(q.order); n++ {
		index := (q.next + n) % len(q.order)
		d := q.order[index]
		if d.depth == 0 || reserve && d.active >= driverLimit {
			continue
		}
		flowLimit := fairShare(driverLimit, len(d.flows))
		first := d.first
		f := first
		for f != nil {
			next := f.next
			if !reserve || f.active < flowLimit {
				e := f.first
				f.first = e.next
				if f.first == nil {
					f.last = nil
					d.removeFlow(f)
				} else {
					d.first = next
				}
				if e.older != nil {
					e.older.newer = e.newer
				} else {
					q.first = e.newer
				}
				if e.newer != nil {
					e.newer.older = e.older
				} else {
					q.last = e.older
				}
				if e.driverOlder != nil {
					e.driverOlder.driverNewer = e.driverNewer
				} else {
					d.pendingFirst = e.driverNewer
				}
				if e.driverNewer != nil {
					e.driverNewer.driverOlder = e.driverOlder
				} else {
					d.pendingLast = e.driverOlder
				}
				d.depth--
				q.depth--
				q.dequeued++
				q.next = (index + 1) % len(q.order)
				now := time.Now()
				wait := now.Sub(e.at)
				q.totalWait += wait
				q.maxWait = max(q.maxWait, wait)
				q.lastDequeue = now
				if !reserve {
					if f.active == 0 && f.first == nil {
						delete(d.flows, f.key)
					}
					return e.job, nil
				}
				f.active++
				d.active++
				q.active++
				lease := &fairLease{Job: e.job, queue: q, driver: d, flow: f, at: now, previous: d.activeLast}
				if d.activeLast != nil {
					d.activeLast.next = lease
				} else {
					d.activeFirst = lease
				}
				d.activeLast = lease
				return lease, nil
			}
			f = next
			if f == first {
				break
			}
		}
	}
	return nil, nil
}
func (q *FairQueue) Dequeue() (jobs.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.take(0, false)
}
func (q *FairQueue) DequeueBatch(maximum int) ([]jobs.Job, error) {
	var batch []jobs.Job
	for range maximum {
		job, err := q.Dequeue()
		if err != nil {
			if len(batch) > 0 {
				return batch, nil
			}
			return nil, err
		}
		if job == nil {
			break
		}
		batch = append(batch, job)
	}
	return batch, nil
}
func (q *FairQueue) Close() { q.mu.Lock(); q.closed = true; q.mu.Unlock(); q.notify() }
func (q *FairQueue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, cv, samples := q.arrivals.snapshot()
	elapsed := time.Since(q.started)
	stats := Stats{ArrivalCV: cv, ArrivalSamples: samples, Capacity: q.capacity, QueueDepth: q.depth, Enqueued: q.enqueued, Dequeued: q.dequeued, Dropped: q.dropped, LastEnqueue: q.lastEnqueue, LastDequeue: q.lastDequeue, MaxQueueTime: q.maxWait, SampleWindow: elapsed, OldestPendingReason: "queue_empty"}
	if q.dequeued > 0 {
		stats.AvgQueueTime = q.totalWait / time.Duration(q.dequeued)
	}
	if elapsed > 0 {
		stats.EnqueueRate = float64(q.enqueued) / elapsed.Seconds()
		stats.DequeueRate = float64(q.dequeued) / elapsed.Seconds()
	}
	if q.first != nil {
		stats.OldestPendingAge = time.Since(q.first.at)
		stats.OldestPendingAvailable = true
		stats.OldestPendingReason = ""
	}
	stats.Drivers = q.driverStats()
	return stats
}
func (q *FairQueue) driverStats() []DriverQueueStats {
	limit := max(1, q.executionLimit/len(q.order))
	out := make([]DriverQueueStats, 0, len(q.order))
	for _, d := range q.order {
		s := DriverQueueStats{Driver: d.name, Pending: d.depth, Active: d.active, ConcurrencyLimit: limit, AdmissionLimit: q.capacity / len(q.order), Rejections: d.dropped, Timeouts: d.timeouts, Condition: "idle"}
		if d.pendingFirst != nil {
			s.OldestPendingAge = time.Since(d.pendingFirst.at)
		}
		if d.activeFirst != nil {
			s.OldestActiveAge = time.Since(d.activeFirst.at)
			s.Condition = "executing"
		}
		if d.depth > 0 {
			s.Condition = "queued"
			if d.active >= limit {
				s.Condition = "driver_concurrency"
			}
			if q.active >= q.executionLimit {
				s.Condition = "worker_capacity"
			}
			if q.executionLimit > 0 && q.executionLimit < len(q.order) {
				s.Condition = "insufficient_driver_capacity"
			}
		}
		if d.depth >= s.AdmissionLimit {
			s.Condition = "driver_admission"
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Driver < out[j].Driver })
	return out
}
