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

// FairQueue partitions admission and execution across eligible configured demand.
// Spare capacity can be shared; idle compiled drivers reserve nothing.
// Flows exist only while they have accepted pending or active work, so their
// count is bounded by queue capacity plus the global execution limit.
type FairQueue struct {
	pendingActions                                map[string]map[string]*jobs.Dispatch
	rates                                         map[string]*notificationRate
	mu                                            sync.Mutex
	drivers                                       map[string]*fairDriver
	order                                         []*fairDriver
	next, depth, active, capacity, executionLimit int
	closed                                        bool
	serialFlows                                   bool
	notifications                                 bool
	outstanding                                   int // Executions retained until both publication and owner acknowledgement.
	concurrency                                   map[string]int
	wakeup                                        chan struct{}
	started, lastEnqueue, lastDequeue             time.Time
	first, last                                   *fairEntry
	enqueued, dequeued, dropped                   int64
	totalWait, maxWait                            time.Duration
	arrivals                                      durationMetrics
}

type fairDriver struct {
	name                      string
	demand                    DriverDemand
	reservation               int
	flows                     map[uint64]*fairFlow
	first                     *fairFlow // Circular list of flows with pending work only.
	depth, active             int
	dropped, timeouts         int64
	rateDeferred              int64
	activeFirst, activeLast   *fairLease
	pendingFirst, pendingLast *fairEntry
}

type fairFlow struct {
	sequences      map[string]bool
	key            uint64
	first, last    *fairEntry
	depth          int
	active         int
	previous, next *fairFlow
}
type fairEntry struct {
	rateDeferred             bool
	sequence                 string
	job                      jobs.Job
	at                       time.Time
	next, older, newer       *fairEntry
	driverOlder, driverNewer *fairEntry
}
type fairLease struct {
	rateScopes          []jobs.NotificationRateScope
	sequence            string
	fenced              bool
	completed, released bool
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
	l.completed = true
	if q.notifications && (!l.fenced || l.released) {
		q.outstanding--
	}
	if !l.fenced {
		delete(f.sequences, l.sequence)
		q.releaseNotificationRates(l, result)
	}
	if f.active == 0 && f.first == nil && len(f.sequences) == 0 {
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
	DestinationConcurrency *int          `json:"destination_concurrency,omitempty"`
	WorkerHeadroom         int           `json:"worker_headroom"`
	RateDeferredTotal      int64         `json:"rate_deferred_total"`
	Driver                 string        `json:"driver"`
	RequestedCapacity      int           `json:"requested_capacity"`
	ReservedCapacity       int           `json:"reserved_capacity"`
	Pending                int           `json:"pending"`
	Active                 int           `json:"active"`
	ConcurrencyLimit       int           `json:"concurrency_limit"`
	AdmissionLimit         int           `json:"admission_limit"`
	OldestPendingAge       time.Duration `json:"oldest_pending_age"`
	OldestActiveAge        time.Duration `json:"oldest_active_age"`
	Rejections             int64         `json:"rejections"`
	Timeouts               int64         `json:"timeouts"`
	Condition              string        `json:"condition"`
}

// NewFairQueue registers compiled check drivers unless names are supplied.
// Registration permits admission; it creates no demand or reserved capacity.
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

// NewActionQueue preserves intervention destination serialization. Notifications
// additionally permit bounded independent sequences at each private destination.
// Identities are never exported as metric labels.
func NewActionQueue(capacity int, kind string) (*FairQueue, error) {
	var names []string
	for _, c := range jobs.Capabilities() {
		if c.Kind == kind && c.Available {
			names = append(names, c.Driver)
		}
	}
	if len(names) == 0 {
		return nil, errors.New("no compiled action drivers")
	}
	q, err := NewFairQueue(capacity, names...)
	if err == nil {
		q.serialFlows = kind != "notification"
		q.notifications = kind == "notification"
		q.concurrency = map[string]int{"webhook": 4}
	}
	return q, err
}
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
		return errors.New("fair queue requires a driver and flow identity")
	}
	driver, key := identity.QueueIdentity()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	d := q.drivers[driver]
	if d == nil {
		return errors.New("fair queue driver is not registered")
	}
	f := d.flows[key]
	admissionLimit := q.admissionLimit(d)
	// An existing flow cannot consume the last waiting slot.
	// Several stalled destinations must not exclude a new flow while execution
	// capacity is still available. No accepted work is evicted to make this room.
	reservedForNewFlow := f != nil &&
		(q.capacity > 1 && q.depth >= q.capacity-1 || admissionLimit > 1 && d.depth >= admissionLimit-1)
	retainedFull := q.notifications && q.depth+q.outstanding >= q.capacity+q.executionLimit
	if retainedFull || reservedForNewFlow || q.depth >= q.capacity || d.depth >= admissionLimit || f != nil && f.depth >= max(1, admissionLimit/2) {
		q.dropped++
		d.dropped++
		return ErrQueueFull
	}
	now := time.Now()
	job.SetEnqueueTime(now)
	if dispatch, ok := job.(*jobs.Dispatch); ok && q.notifications && dispatch.ActionID != "" {
		if q.pendingActions == nil {
			q.pendingActions = map[string]map[string]*jobs.Dispatch{}
		}
		if q.pendingActions[dispatch.MonitorID] == nil {
			q.pendingActions[dispatch.MonitorID] = map[string]*jobs.Dispatch{}
		}
		q.pendingActions[dispatch.MonitorID][dispatch.ActionID] = dispatch
	}
	if f == nil {
		f = &fairFlow{key: key}
		d.flows[key] = f
	}
	e := &fairEntry{job: job, at: now, older: q.last}
	if q.notifications {
		if sequenced, ok := job.(interface{ NotificationSequence() string }); ok {
			e.sequence = sequenced.NotificationSequence()
		}
	}
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
	f.depth++
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
	reserved := q.reservations(capacity, false)
	for n := 0; n < len(q.order); n++ {
		index := (q.next + n) % len(q.order)
		d := q.order[index]
		driverLimit := q.executionLimitFor(d, capacity, reserved)
		if d.depth == 0 || reserve && d.active >= driverLimit {
			continue
		}
		flowLimit := fairShare(driverLimit, len(d.flows))
		if q.serialFlows {
			flowLimit = 1
		} else if q.notifications {
			limit := q.concurrency[d.name]
			if limit == 0 {
				limit = 1
			}
			flowLimit = min(flowLimit, limit)
		}
		first := d.first
		f := first
		for f != nil {
			next := f.next
			e, previous := f.first, (*fairEntry)(nil)
			if q.notifications && reserve {
				for e != nil && !cancelledDispatch(e.job) {
					// Ordered followers are already ineligible. Do not walk the
					// limiter registry merely to count another reason to wait.
					if f.sequences[e.sequence] {
						previous, e = e, e.next
						continue
					}
					paced := !q.notificationRateAvailable(e.job, time.Now())
					if paced && !e.rateDeferred {
						e.rateDeferred = true
						d.rateDeferred++
					}
					if !paced {
						break
					}
					previous, e = e, e.next
				}
			}
			if e != nil && (!reserve || f.active < flowLimit) {
				if dispatch, ok := e.job.(*jobs.Dispatch); ok && q.notifications {
					delete(q.pendingActions[dispatch.MonitorID], dispatch.ActionID)
					if len(q.pendingActions[dispatch.MonitorID]) == 0 {
						delete(q.pendingActions, dispatch.MonitorID)
					}
				}
				if previous == nil {
					f.first = e.next
				} else {
					previous.next = e.next
				}
				if f.last == e {
					f.last = previous
				}
				f.depth--
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
				if !reserve || cancelledDispatch(e.job) {
					if f.active == 0 && f.first == nil && len(f.sequences) == 0 {
						delete(d.flows, f.key)
					}
					return e.job, nil
				}
				f.active++
				d.active++
				q.active++
				if q.notifications {
					q.outstanding++
					if f.sequences == nil {
						f.sequences = map[string]bool{}
					}
					f.sequences[e.sequence] = true
				}
				lease := &fairLease{sequence: e.sequence, Job: e.job, queue: q, driver: d, flow: f, at: now, previous: d.activeLast}
				if q.notifications {
					lease.rateScopes = q.reserveNotificationRates(e.job)
				}
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
	stats := Stats{ArrivalCV: cv, ArrivalSamples: samples, Capacity: q.capacity, QueueDepth: q.depth, Enqueued: q.enqueued, Dequeued: q.dequeued, Dropped: q.dropped, LastEnqueue: q.lastEnqueue.UTC(), LastDequeue: q.lastDequeue.UTC(), MaxQueueTime: q.maxWait, SampleWindow: elapsed, OldestPendingReason: "queue_empty"}
	if q.notifications {
		stats.AwaitingReconciliation = max(0, q.outstanding-q.active)
	}
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
	reserved := q.reservations(q.executionLimit, false)
	out := make([]DriverQueueStats, 0, len(q.order))
	for i, d := range q.order {
		limit := q.executionLimitFor(d, q.executionLimit, reserved)
		s := DriverQueueStats{Driver: d.name, Pending: d.depth, Active: d.active, RequestedCapacity: d.reservation, ReservedCapacity: reserved[i], ConcurrencyLimit: limit, AdmissionLimit: q.admissionLimit(d), Rejections: d.dropped, Timeouts: d.timeouts, Condition: "idle"}
		s.WorkerHeadroom = max(0, limit-d.active)
		s.RateDeferredTotal = d.rateDeferred
		if q.notifications || q.serialFlows {
			concurrency := 1
			if q.notifications && q.concurrency[d.name] > 0 {
				concurrency = q.concurrency[d.name]
			}
			s.DestinationConcurrency = &concurrency
		}
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
			if d.reservation > reserved[i] {
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
