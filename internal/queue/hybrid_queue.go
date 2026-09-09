package queue

import (
	"cpra/internal/jobs"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"go.uber.org/zap"
)

const (
	defaultRingCapacity     = 1 << 17 // 131,072
	defaultOverflowCapacity = 1 << 15 // 32,768
	defaultSoftWatermark    = 0.90
	defaultHardWatermark    = 1.0
)

// DropPolicy defines how the hybrid queue behaves once both the ring and overflow paths are saturated.
type DropPolicy int

const (
	// DropPolicyReject rejects new jobs when the queue is full.
	DropPolicyReject DropPolicy = iota
	// DropPolicyDropNewest drops the just-arrived job when capacity is exceeded.
	DropPolicyDropNewest
	// DropPolicyDropOldest evicts the oldest overflow job to admit a new job.
	DropPolicyDropOldest
)

func (p DropPolicy) String() string {
	switch p {
	case DropPolicyReject:
		return "reject"
	case DropPolicyDropNewest:
		return "drop_newest"
	case DropPolicyDropOldest:
		return "drop_oldest"
	default:
		return "unknown"
	}
}

// HybridQueueConfig controls the behaviour of a HybridQueue instance.
type HybridQueueConfig struct {
	Name             string
	RingCapacity     int
	OverflowCapacity int
	SoftWatermark    float64
	HardWatermark    float64
	DropPolicy       DropPolicy
	Logger           *zap.Logger
}

// DefaultHybridQueueConfig returns the recommended production defaults.
func DefaultHybridQueueConfig() HybridQueueConfig {
	return HybridQueueConfig{
		Name:             "hybrid",
		RingCapacity:     defaultRingCapacity,
		OverflowCapacity: defaultOverflowCapacity,
		SoftWatermark:    defaultSoftWatermark,
		HardWatermark:    defaultHardWatermark,
		DropPolicy:       DropPolicyReject,
		Logger:           zap.NewNop(),
	}
}

// HybridQueue combines a lock-free xsync ring buffer with a mutex-protected overflow slice.
// The ring handles steady-state work while the overflow absorbs bursts before optional dropping.
type HybridQueue struct {
	arrivals durationMetrics
	ring     *xsync.MPMCQueue[jobs.Job]
	cfg      HybridQueueConfig
	logger   *zap.Logger

	mu                sync.Mutex
	overflow          []jobs.Job
	overflowHead      int
	softOverflowLimit int
	hardOverflowLimit int

	closed atomic.Bool

	// ringEnqueued/ringDequeued replace a single shared ring-depth counter.
	// Enqueuers only write ringEnqueued; dequeuers only write ringDequeued, so
	// the dispatch loop and the result dispatcher never contend on the same
	// cache line. Depth = ringEnqueued - ringDequeued (eventually consistent).
	ringEnqueued atomic.Int64
	_            [56]byte // pad to a full cache line
	ringDequeued atomic.Int64
	_            [56]byte // pad to a full cache line

	overflowDepth atomic.Int64
	dequeueTurn   atomic.Uint64

	enqueuedCount   atomic.Int64
	dequeuedCount   atomic.Int64
	droppedCount    atomic.Int64
	overflowEvents  atomic.Uint64
	totalQueueWait  atomic.Int64
	maxQueueWait    atomic.Int64
	lastEnqueueNano atomic.Int64
	lastDequeueNano atomic.Int64
	startNano       atomic.Int64

	softOverflowAlerted atomic.Bool
	hardOverflowAlerted atomic.Bool
	ringSaturated       atomic.Bool
}

// NewHybridQueue builds a HybridQueue using the supplied configuration.
func NewHybridQueue(config HybridQueueConfig) (*HybridQueue, error) {
	cfg := normalizeHybridConfig(config)

	if cfg.RingCapacity <= 0 {
		return nil, errors.New("hybrid queue: ring capacity must be positive")
	}
	if cfg.OverflowCapacity < 0 {
		return nil, errors.New("hybrid queue: overflow capacity cannot be negative")
	}

	queue := &HybridQueue{
		ring:   xsync.NewMPMCQueue[jobs.Job](cfg.RingCapacity),
		cfg:    cfg,
		logger: cfg.Logger,
	}
	if cfg.OverflowCapacity > 0 {
		queue.overflow = make([]jobs.Job, 0, cfg.OverflowCapacity)
	}
	queue.softOverflowLimit = computeWatermarkLimit(cfg.OverflowCapacity, cfg.SoftWatermark)
	queue.hardOverflowLimit = computeWatermarkLimit(cfg.OverflowCapacity, cfg.HardWatermark)
	if queue.hardOverflowLimit == 0 && cfg.OverflowCapacity > 0 {
		queue.hardOverflowLimit = cfg.OverflowCapacity
	}
	if queue.hardOverflowLimit > 0 && queue.softOverflowLimit > queue.hardOverflowLimit {
		queue.softOverflowLimit = queue.hardOverflowLimit
	}
	queue.startNano.Store(time.Now().UnixNano())

	return queue, nil
}

// Enqueue adds a single job to the queue, preferring the lock-free ring fast path.
func (q *HybridQueue) Enqueue(job jobs.Job) error {
	if q.closed.Load() {
		return ErrQueueClosed
	}

	now := q.enqueueTimestamp(job)

	if q.ring.TryEnqueue(job) {
		q.ringEnqueued.Add(1)
		q.recordEnqueue(now)
		return nil
	}

	q.markRingSaturated()
	if err := q.enqueueOverflow(job, now); err != nil {
		return err
	}
	q.recordEnqueue(now)
	return nil
}

// enqueueTimestamp returns the clock reading to record for an enqueue.
// Jobs that track enqueue time get a precise per-job timestamp; for the rest
// (such as jobs with a no-op SetEnqueueTime) the zero time is
// returned and recordEnqueue samples the clock instead, keeping the hot
// dispatch path free of a per-job time.Now call.
func (q *HybridQueue) enqueueTimestamp(job jobs.Job) time.Time {
	if isNilJob(job) || !tracksEnqueueTime(job) {
		return time.Time{}
	}
	now := time.Now()
	job.SetEnqueueTime(now)
	return now
}

// EnqueueBatch inserts a slice of jobs in FIFO order.
func (q *HybridQueue) EnqueueBatch(items []interface{}) error {
	for _, item := range items {
		if item == nil {
			continue
		}
		job, ok := item.(jobs.Job)
		if !ok {
			return errors.New("hybrid queue: invalid job type in batch")
		}
		if err := q.Enqueue(job); err != nil {
			return err
		}
	}
	return nil
}

// tryDequeueRing accounts for a successful ring removal.
func (q *HybridQueue) tryDequeueRing() (jobs.Job, bool) {
	job, ok := q.ring.TryDequeue()
	if ok {
		q.ringDequeued.Add(1)
	}
	return job, ok
}

// Dequeue alternates preference between overflow and ring to keep both moving.
func (q *HybridQueue) Dequeue() (jobs.Job, error) {
	first, second := q.tryDequeueOverflow, q.tryDequeueRing
	if q.dequeueTurn.Add(1)%2 == 0 {
		first, second = second, first
	}
	job, ok := first()
	if !ok {
		job, ok = second()
	}
	if !ok {
		if q.closed.Load() && q.isEmpty() {
			return nil, ErrQueueClosed
		}
		return nil, nil
	}
	q.recordDequeue(job, time.Now())
	q.resetRingSaturation(q.ringDepthNow())
	return job, nil
}

// DequeueBatch reserves progress for both paths while either holds work.
func (q *HybridQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	if maxSize <= 0 {
		return nil, nil
	}
	if maxSize == 1 {
		job, err := q.Dequeue()
		if err != nil || job == nil {
			return nil, err
		}
		return []jobs.Job{job}, nil
	}
	result := make([]jobs.Job, 0, maxSize)
	result = append(result, q.drainOverflow((maxSize+1)/2)...)
	for len(result) < maxSize {
		job, ok := q.tryDequeueRing()
		if !ok {
			break
		}
		result = append(result, job)
	}
	if len(result) < maxSize {
		result = append(result, q.drainOverflow(maxSize-len(result))...)
	}
	if len(result) == 0 {
		if q.closed.Load() && q.isEmpty() {
			return nil, ErrQueueClosed
		}
		return nil, nil
	}
	q.recordBatchDequeue(result, time.Now())
	q.resetRingSaturation(q.ringDepthNow())
	return result, nil
}

// Close prevents new jobs from being enqueued.
func (q *HybridQueue) Close() {
	if q.closed.CompareAndSwap(false, true) {
		q.logger.Info("hybrid queue closed", zap.String("queue", q.cfg.Name))
	}
}

// Stats returns observable metrics for monitoring.
func (q *HybridQueue) Stats() Stats {
	_, arrivalCV, arrivalSamples := q.arrivals.snapshot()
	enqueued := q.enqueuedCount.Load()
	dequeued := q.dequeuedCount.Load()
	dropped := q.droppedCount.Load()

	depth := q.ringDepthNow() + q.overflowDepth.Load()
	if depth < 0 {
		depth = 0
	}

	elapsed := time.Since(time.Unix(0, q.startNano.Load()))
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}

	var avgWait time.Duration
	if dequeued > 0 {
		avgWait = time.Duration(q.totalQueueWait.Load() / dequeued)
	}

	maxWait := time.Duration(q.maxQueueWait.Load())

	return Stats{
		ArrivalCV: arrivalCV, ArrivalSamples: arrivalSamples,
		QueueDepth:    int(depth),
		Capacity:      q.cfg.RingCapacity + q.cfg.OverflowCapacity,
		Enqueued:      enqueued,
		Dequeued:      dequeued,
		Dropped:       dropped,
		MaxQueueTime:  maxWait,
		AvgQueueTime:  avgWait,
		MaxJobLatency: maxWait,
		AvgJobLatency: avgWait,
		EnqueueRate:   float64(enqueued) / elapsed.Seconds(),
		DequeueRate:   float64(dequeued) / elapsed.Seconds(),
		LastEnqueue:   time.Unix(0, q.lastEnqueueNano.Load()),
		LastDequeue:   time.Unix(0, q.lastDequeueNano.Load()),
		SampleWindow:  elapsed,
	}
}

// OverflowDepth returns the current overflow-slice depth (debug/metrics).
func (q *HybridQueue) OverflowDepth() int { return int(q.overflowDepth.Load()) }

// RingDepth returns the current ring-buffer depth (debug/metrics).
func (q *HybridQueue) RingDepth() int { return int(q.ringDepthNow()) }

// OverflowEvents returns the total number of overflow enqueue events (debug/metrics).
func (q *HybridQueue) OverflowEvents() uint64 { return q.overflowEvents.Load() }

func (q *HybridQueue) enqueueOverflow(job jobs.Job, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.enqueueOverflowLocked(job, now)
}

// liveOverflowDepth returns the number of live (not-yet-dequeued) overflow
// jobs, accounting for the consumed prefix before overflowHead.
func (q *HybridQueue) liveOverflowDepth() int {
	return len(q.overflow) - q.overflowHead
}

// compactOverflowLocked reclaims the consumed prefix of the overflow slice.
// It is O(live) but amortized O(1) per dequeue: it only runs once the consumed
// prefix reaches half the slice, so each element is copied at most once per
// compaction cycle.
func (q *HybridQueue) compactOverflowLocked() {
	if q.overflowHead == 0 {
		return
	}
	if q.overflowHead == len(q.overflow) {
		q.overflow = q.overflow[:0]
		q.overflowHead = 0
		return
	}
	if q.overflowHead*2 >= len(q.overflow) {
		copy(q.overflow, q.overflow[q.overflowHead:])
		q.overflow = q.overflow[:len(q.overflow)-q.overflowHead]
		q.overflowHead = 0
	}
}

func (q *HybridQueue) enqueueOverflowLocked(job jobs.Job, now time.Time) error {
	if q.cfg.OverflowCapacity == 0 {
		return q.handleDropLocked(job, now, q.liveOverflowDepth(), "overflow_disabled")
	}

	currentDepth := q.liveOverflowDepth()
	nextDepth := currentDepth + 1

	if q.hardOverflowLimit > 0 && nextDepth > q.hardOverflowLimit {
		return q.handleDropLocked(job, now, currentDepth, "hard_watermark_exceeded")
	}
	if q.cfg.OverflowCapacity > 0 && nextDepth > q.cfg.OverflowCapacity {
		return q.handleDropLocked(job, now, currentDepth, "overflow_capacity_exceeded")
	}

	q.overflow = append(q.overflow, job)
	newDepth := q.liveOverflowDepth()
	q.overflowDepth.Store(int64(newDepth))
	q.overflowEvents.Add(1)
	q.evaluateOverflowWatermarksLocked(newDepth)
	return nil
}

func (q *HybridQueue) handleDropLocked(job jobs.Job, now time.Time, currentDepth int, reason string) error {
	q.droppedCount.Add(1)

	fields := []zap.Field{
		zap.String("queue", q.cfg.Name),
		zap.String("policy", q.cfg.DropPolicy.String()),
		zap.String("reason", reason),
		zap.Int("overflow_capacity", q.cfg.OverflowCapacity),
		zap.Int("overflow_depth", currentDepth),
	}

	switch q.cfg.DropPolicy {
	case DropPolicyDropOldest:
		if currentDepth == 0 {
			q.logger.Warn("hybrid queue has no overflow items to drop; rejecting newest job", fields...)
			return ErrQueueFull
		}
		q.overflow[q.overflowHead] = nil
		q.overflowHead++
		q.compactOverflowLocked()
		q.overflowDepth.Store(int64(q.liveOverflowDepth()))
		q.evaluateOverflowWatermarksLocked(q.liveOverflowDepth())
		q.logger.Warn("hybrid queue dropped oldest overflow job to admit new work", fields...)
		q.overflow = append(q.overflow, job)
		q.overflowDepth.Store(int64(q.liveOverflowDepth()))
		q.overflowEvents.Add(1)
		q.evaluateOverflowWatermarksLocked(q.liveOverflowDepth())
		return nil
	case DropPolicyDropNewest:
		q.logger.Warn("hybrid queue dropping newest job due to saturation", fields...)
		return ErrQueueFull
	case DropPolicyReject:
		q.logger.Error("hybrid queue rejecting job due to saturation", fields...)
		return ErrQueueFull
	default:
		q.logger.Warn("hybrid queue encountered unknown drop policy; rejecting job", fields...)
		return ErrQueueFull
	}
}

func (q *HybridQueue) markRingSaturated() {
	if q.ringSaturated.CompareAndSwap(false, true) {
		q.logger.Warn("hybrid queue ring saturated; routing to overflow",
			zap.String("queue", q.cfg.Name),
			zap.Int("capacity", q.cfg.RingCapacity))
	}
}

func (q *HybridQueue) resetRingSaturation(depth int64) {
	if !q.ringSaturated.Load() || q.cfg.RingCapacity == 0 {
		return
	}
	threshold := int64(float64(q.cfg.RingCapacity) * 0.8)
	if depth < threshold {
		if q.ringSaturated.CompareAndSwap(true, false) {
			q.logger.Info("hybrid queue ring recovered below saturation threshold",
				zap.String("queue", q.cfg.Name),
				zap.Int64("depth", depth))
		}
	}
}

func (q *HybridQueue) tryDequeueOverflow() (jobs.Job, bool) {
	if q.cfg.OverflowCapacity == 0 {
		return nil, false
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.liveOverflowDepth() == 0 {
		q.overflowDepth.Store(0)
		q.evaluateOverflowWatermarksLocked(0)
		return nil, false
	}

	job := q.overflow[q.overflowHead]
	q.overflow[q.overflowHead] = nil
	q.overflowHead++
	q.compactOverflowLocked()

	depth := q.liveOverflowDepth()
	q.overflowDepth.Store(int64(depth))
	q.evaluateOverflowWatermarksLocked(depth)

	return job, true
}

func (q *HybridQueue) drainOverflow(limit int) []jobs.Job {
	if limit <= 0 || q.cfg.OverflowCapacity == 0 {
		return nil
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	available := q.liveOverflowDepth()
	if available == 0 {
		q.overflowDepth.Store(0)
		q.evaluateOverflowWatermarksLocked(0)
		return nil
	}
	if limit > available {
		limit = available
	}

	out := make([]jobs.Job, limit)
	copy(out, q.overflow[q.overflowHead:q.overflowHead+limit])

	for i := 0; i < limit; i++ {
		q.overflow[q.overflowHead+i] = nil
	}
	q.overflowHead += limit
	q.compactOverflowLocked()

	depth := q.liveOverflowDepth()
	q.overflowDepth.Store(int64(depth))
	q.evaluateOverflowWatermarksLocked(depth)

	return out
}

func (q *HybridQueue) evaluateOverflowWatermarksLocked(depth int) {
	if q.cfg.OverflowCapacity <= 0 {
		return
	}

	ratio := 0.0
	if q.cfg.OverflowCapacity > 0 {
		ratio = float64(depth) / float64(q.cfg.OverflowCapacity)
	}

	if q.cfg.SoftWatermark > 0 {
		if ratio >= q.cfg.SoftWatermark {
			if q.softOverflowAlerted.CompareAndSwap(false, true) {
				q.logger.Warn("hybrid queue overflow above soft watermark",
					zap.String("queue", q.cfg.Name),
					zap.Float64("ratio", ratio),
					zap.Int("overflow_depth", depth),
					zap.Float64("soft_watermark", q.cfg.SoftWatermark))
			}
		} else if q.softOverflowAlerted.Load() && ratio < q.cfg.SoftWatermark {
			if q.softOverflowAlerted.CompareAndSwap(true, false) {
				q.logger.Info("hybrid queue overflow recovered below soft watermark",
					zap.String("queue", q.cfg.Name),
					zap.Float64("ratio", ratio),
					zap.Int("overflow_depth", depth))
			}
		}
	}

	if q.cfg.HardWatermark > 0 {
		if ratio >= q.cfg.HardWatermark {
			if q.hardOverflowAlerted.CompareAndSwap(false, true) {
				q.logger.Error("hybrid queue overflow above hard watermark",
					zap.String("queue", q.cfg.Name),
					zap.Float64("ratio", ratio),
					zap.Int("overflow_depth", depth),
					zap.Float64("hard_watermark", q.cfg.HardWatermark))
			}
		} else if q.hardOverflowAlerted.Load() && ratio < q.cfg.HardWatermark {
			if q.hardOverflowAlerted.CompareAndSwap(true, false) {
				q.logger.Info("hybrid queue overflow recovered below hard watermark",
					zap.String("queue", q.cfg.Name),
					zap.Float64("ratio", ratio),
					zap.Int("overflow_depth", depth))
			}
		}
	}
}

// clockSampleInterval bounds how stale lastEnqueueNano may become for jobs
// that do not carry enqueue timestamps: the clock is read once per interval.
const clockSampleInterval = 1 << 10

func (q *HybridQueue) recordEnqueue(now time.Time) {
	q.arrivals.recordArrival()
	n := q.enqueuedCount.Add(1)
	if now.IsZero() && n&(clockSampleInterval-1) != 0 {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	q.lastEnqueueNano.Store(now.UnixNano())
}

// ringDepthNow returns the current ring occupancy as the difference of the
// producer and consumer counters. It is eventually consistent: operations in
// flight may transiently over- or under-report by a small amount, which the
// callers already tolerate (Stats clamps negatives; saturation heuristics and
// isEmpty only need a conservative approximation).
func (q *HybridQueue) ringDepthNow() int64 {
	return q.ringEnqueued.Load() - q.ringDequeued.Load()
}

// tracksEnqueueTime reports whether a job tracks its enqueue time. Immutable
// jobs can implement jobs.EnqueueTimeTracker and return false, so the
// queue skips the per-dequeue GetEnqueueTime interface call.
func tracksEnqueueTime(job jobs.Job) bool {
	if tracker, ok := job.(jobs.EnqueueTimeTracker); ok {
		return tracker.TracksEnqueueTime()
	}
	return true
}

func (q *HybridQueue) recordDequeue(job jobs.Job, now time.Time) {
	if !isNilJob(job) && tracksEnqueueTime(job) {
		enqueueTime := job.GetEnqueueTime()
		if !enqueueTime.IsZero() {
			wait := now.Sub(enqueueTime)
			if wait > 0 {
				q.totalQueueWait.Add(int64(wait))
				q.updateMaxQueueWait(int64(wait))
			}
		}
	}
	q.dequeuedCount.Add(1)
	q.lastDequeueNano.Store(now.UnixNano())
}

func (q *HybridQueue) recordBatchDequeue(batch []jobs.Job, now time.Time) {
	if len(batch) == 0 {
		return
	}

	var total int64
	var maxWait int64
	for _, job := range batch {
		if isNilJob(job) || !tracksEnqueueTime(job) {
			continue
		}
		enqueueTime := job.GetEnqueueTime()
		if enqueueTime.IsZero() {
			continue
		}
		wait := now.Sub(enqueueTime)
		if wait <= 0 {
			continue
		}
		w := int64(wait)
		total += w
		if w > maxWait {
			maxWait = w
		}
	}
	if total > 0 {
		q.totalQueueWait.Add(total)
		q.updateMaxQueueWait(maxWait)
	}
	q.dequeuedCount.Add(int64(len(batch)))
	q.lastDequeueNano.Store(now.UnixNano())
}

func (q *HybridQueue) updateMaxQueueWait(candidate int64) {
	for {
		current := q.maxQueueWait.Load()
		if candidate <= current {
			return
		}
		if q.maxQueueWait.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func (q *HybridQueue) isEmpty() bool {
	return q.ringDepthNow() == 0 && q.overflowDepth.Load() == 0
}

func normalizeHybridConfig(cfg HybridQueueConfig) HybridQueueConfig {
	defaults := DefaultHybridQueueConfig()

	if cfg.Name == "" {
		cfg.Name = defaults.Name
	}
	if cfg.RingCapacity <= 0 {
		cfg.RingCapacity = defaults.RingCapacity
	}
	if cfg.OverflowCapacity < 0 {
		cfg.OverflowCapacity = 0
	}
	if cfg.OverflowCapacity == 0 {
		cfg.SoftWatermark = 0
		cfg.HardWatermark = 0
	} else {
		if cfg.SoftWatermark <= 0 {
			cfg.SoftWatermark = defaults.SoftWatermark
		} else {
			cfg.SoftWatermark = clamp01(cfg.SoftWatermark)
		}
		if cfg.HardWatermark <= 0 {
			cfg.HardWatermark = defaults.HardWatermark
		} else {
			cfg.HardWatermark = clamp01(cfg.HardWatermark)
		}
		if cfg.HardWatermark < cfg.SoftWatermark {
			cfg.HardWatermark = cfg.SoftWatermark
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = defaults.Logger
	}

	return cfg
}

func computeWatermarkLimit(capacity int, ratio float64) int {
	if capacity <= 0 || ratio <= 0 {
		return 0
	}
	limit := int(math.Ceil(float64(capacity) * ratio))
	switch {
	case limit <= 0:
		return 1
	case limit > capacity:
		return capacity
	default:
		return limit
	}
}

func clamp01(v float64) float64 {
	switch {
	case v <= 0:
		return 0
	case v >= 1:
		return 1
	default:
		return v
	}
}
