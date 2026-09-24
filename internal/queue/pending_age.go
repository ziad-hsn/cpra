package queue

import (
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
)

// pendingEntry belongs to one admission attempt, never to a reusable Job.
// The producer owns one reference until finish; the queue owns another after
// publication until take. It is recycled only when both have relinquished it.
// All fields are protected by pendingAges.mu, except the queue's exclusive
// possession of the entry pointer itself.
type pendingEntry struct {
	previous, next *pendingEntry
	job            jobs.Job
	at             time.Duration
	listed         bool
	settled        bool
}

// pendingAges keeps pending copies in chronological registration order. Its
// mutex never holds a queue lock or calls job code. Queue removal may take this
// mutex while owning an overflow/segment lock; there is no reverse lock path.
// Registration is provisional until admission succeeds; a provisional oldest
// entry makes the observation unavailable rather than counting rejected work.
// Newer in-flight admissions do not hide an older settled pending entry.
type pendingAges struct {
	mu          sync.Mutex
	first, last *pendingEntry
	epoch       time.Time
	pool        sync.Pool
}

func (p *pendingAges) begin(job jobs.Job) *pendingEntry {
	entry, _ := p.pool.Get().(*pendingEntry)
	if entry == nil {
		entry = &pendingEntry{}
	}
	p.mu.Lock()
	// Reading the clock while holding this lock gives the list chronological
	// order even when producers publish to the actual queue in a different order.
	now := time.Now()
	if p.epoch.IsZero() {
		p.epoch = now
	}
	*entry = pendingEntry{previous: p.last, job: job, at: now.Sub(p.epoch), listed: true}
	if p.last != nil {
		p.last.next = entry
	} else {
		p.first = entry
	}
	p.last = entry
	p.mu.Unlock()
	return entry
}

func (p *pendingAges) unlink(entry *pendingEntry) {
	if entry.previous != nil {
		entry.previous.next = entry.next
	} else {
		p.first = entry.next
	}
	if entry.next != nil {
		entry.next.previous = entry.previous
	} else {
		p.last = entry.previous
	}
	entry.previous, entry.next, entry.listed = nil, nil, false
}

func (p *pendingAges) recycle(entry *pendingEntry) {
	*entry = pendingEntry{}
	p.pool.Put(entry)
}

func (p *pendingAges) finish(entry *pendingEntry, accepted bool) {
	p.mu.Lock()
	entry.settled = true
	if !accepted {
		p.unlink(entry)
	}
	recycle := !entry.listed
	p.mu.Unlock()
	if recycle {
		p.recycle(entry)
	}
}

// take removes a published entry before its job can reach an executor. A fast
// consumer can win the race with finish; finish will then perform the recycle.
func (p *pendingAges) take(entry *pendingEntry) jobs.Job {
	p.mu.Lock()
	job := entry.job
	p.unlink(entry)
	recycle := entry.settled
	p.mu.Unlock()
	if recycle {
		p.recycle(entry)
	}
	return job
}

func (p *pendingAges) observe() (time.Duration, bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.first == nil {
		return 0, false, "queue_empty"
	}
	if !p.first.settled {
		return 0, false, "admission_in_progress"
	}
	// Both the epoch and now retain Go's monotonic component; wall-clock
	// adjustments cannot age a pending job backwards or create a timing sample.
	age := time.Since(p.epoch) - p.first.at
	if age < 0 {
		return 0, false, "invalid_clock_observation"
	}
	return age, true, ""
}

func (p *pendingAges) addTo(stats Stats) Stats {
	stats.OldestPendingAge, stats.OldestPendingAvailable, stats.OldestPendingReason = p.observe()
	return stats
}
