// Package slo records bounded latency distributions without per-monitor state.
package slo

import (
	"fmt"
	"math"
	"slices"
	"sync"
	"time"
)

const Slots = 60
const BucketCount = 160
const SlotDuration = 5 * time.Second

var bounds = func() []time.Duration {
	b := []time.Duration{0, 250 * time.Millisecond, 5 * time.Second}
	for n := float64(time.Millisecond); n < float64(10*time.Minute); n *= 1.1 {
		b = append(b, time.Duration(n))
	}
	slices.Sort(b)
	return slices.Compact(b)
}()

type Histogram struct {
	Buckets [BucketCount]uint64 `json:"buckets"`
	Count   uint64              `json:"count"`
}

func (h *Histogram) Add(d time.Duration) {
	i, _ := slices.BinarySearch(bounds, max(0, d))
	h.Buckets[i]++
	h.Count++
}
func (h *Histogram) Merge(other Histogram) {
	for n, c := range other.Buckets {
		h.Buckets[n] += c
	}
	h.Count += other.Count
}
func (h Histogram) Percentile(p float64) *float64 {
	if h.Count == 0 {
		return nil
	}
	rank := uint64(math.Ceil(p * float64(h.Count)))
	var count uint64
	for n, c := range h.Buckets {
		count += c
		if count >= rank {
			if n >= len(bounds) {
				return nil
			}
			v := float64(bounds[n]) / float64(time.Millisecond)
			return &v
		}
	}
	return nil
}

type Bucket struct {
	Epoch     int64     `json:"epoch"`
	Queue     Histogram `json:"queue"`
	Execution Histogram `json:"execution"`
	Result    Histogram `json:"result"`
	Expected  uint64    `json:"expected"`
	Samples   uint64    `json:"samples"`
	QueueMet  uint64    `json:"queue_met"`
	ResultMet uint64    `json:"result_met"`
	Timeouts  uint64    `json:"timeouts"`
	Missed    uint64    `json:"missed"`
}
type State struct {
	ByDriver     map[string]*[Slots]Bucket `json:"by_driver"`
	QueueTarget  time.Duration             `json:"queue_target"`
	ResultTarget time.Duration             `json:"result_target"`
	Started      time.Time                 `json:"started"`
	Persisted    time.Time                 `json:"persisted"`
	GapStart     time.Time                 `json:"gap_start"`
	GapEnd       time.Time                 `json:"gap_end"`
}

func (s State) Clone() State {
	if s.ByDriver == nil {
		return s
	}
	b := make(map[string]*[Slots]Bucket, len(s.ByDriver))
	for k, v := range s.ByDriver {
		if v != nil {
			copy := *v
			b[k] = &copy
		}
	}
	s.ByDriver = b
	return s
}

type Recorder struct {
	mu    sync.RWMutex
	state State
}

func New(at time.Time, queue, result time.Duration) *Recorder {
	return &Recorder{state: State{Started: at, QueueTarget: queue, ResultTarget: result, ByDriver: make(map[string]*[Slots]Bucket)}}
}
func (r *Recorder) Restore(s State, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.ByDriver == nil {
		return
	}
	r.state = s.Clone()
	r.state.GapStart = s.Persisted
	r.state.GapEnd = at
}
func (r *Recorder) Snapshot(at time.Time) State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := r.state.Clone()
	s.Persisted = at
	return s
}

// Observe is called after result commit. Missed cadence slots and timeouts stay
// in the attainment denominator even when they have no successful result.
func (r *Recorder) Observe(driver string, scheduled, start, end, committed time.Time, timeout bool, missed uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stamp := scheduled
	if stamp.IsZero() {
		stamp = committed
	}
	epoch := stamp.UnixNano() / int64(SlotDuration)
	// A result older than the retained window cannot overwrite a newer slot.
	if committed.Sub(stamp) >= Slots*SlotDuration {
		return
	}
	window := r.state.ByDriver[driver]
	if window == nil {
		window = new([Slots]Bucket)
		r.state.ByDriver[driver] = window
	}
	index := epoch % Slots
	b := &window[index]
	if b.Epoch > epoch {
		return
	}
	if b.Epoch != epoch {
		*b = Bucket{Epoch: epoch}
	}
	b.Samples++
	b.Missed += missed
	if timeout {
		b.Timeouts++
	}
	valid := !scheduled.IsZero() && !start.IsZero() && !end.IsZero() && !start.Before(scheduled) && !end.Before(start) && !committed.Before(end)
	if valid {
		queue, result := start.Sub(scheduled), committed.Sub(scheduled)
		b.Queue.Add(queue)
		b.Execution.Add(end.Sub(start))
		b.Result.Add(result)
		if queue <= r.state.QueueTarget && !timeout {
			b.QueueMet++
		}
		if result <= r.state.ResultTarget && !timeout {
			b.ResultMet++
		}
	}
	r.state.ByDriver[driver] = window
}

// Expect records a scheduled obligation before queue admission. An unfinished
// obligation remains in the denominator once its entire five-second bucket is
// older than the result target. No per-monitor histogram or completion is needed.
func (r *Recorder) Expect(driver string, scheduled time.Time) {
	r.account(driver, scheduled, 1, 0)
}
func (r *Recorder) Missed(driver string, at time.Time, count uint64) {
	if count > 0 {
		r.account(driver, at, 0, count)
	}
}
func (r *Recorder) account(driver string, at time.Time, expected, missed uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	epoch := at.UnixNano() / int64(SlotDuration)
	window := r.state.ByDriver[driver]
	if window == nil {
		window = new([Slots]Bucket)
		r.state.ByDriver[driver] = window
	}
	b := &window[epoch%Slots]
	if b.Epoch > epoch {
		return
	}
	if b.Epoch != epoch {
		*b = Bucket{Epoch: epoch}
	}
	b.Expected += expected
	b.Missed += missed
	r.state.ByDriver[driver] = window
}

type Percentiles struct {
	P50 *float64 `json:"p50_ms"`
	P95 *float64 `json:"p95_ms"`
	P99 *float64 `json:"p99_ms"`
}

func percentiles(h Histogram) Percentiles {
	return Percentiles{h.Percentile(.5), h.Percentile(.95), h.Percentile(.99)}
}

type Report struct {
	Driver           string      `json:"driver"`
	Samples          uint64      `json:"samples"`
	Expected         uint64      `json:"expected"`
	Overdue          uint64      `json:"overdue"`
	Pending          uint64      `json:"pending"`
	Timeouts         uint64      `json:"timeouts"`
	Missed           uint64      `json:"missed"`
	Queue            Percentiles `json:"scheduling_queue"`
	Execution        Percentiles `json:"execution"`
	Result           Percentiles `json:"scheduled_result"`
	QueueMet         uint64      `json:"queue_met"`
	ResultMet        uint64      `json:"result_met"`
	QueueAttainment  *float64    `json:"queue_attainment"`
	ResultAttainment *float64    `json:"result_attainment"`
	Condition        string      `json:"condition"`
}
type View struct {
	Generated        time.Time `json:"generated"`
	WindowSeconds    int       `json:"window_seconds"`
	QueueTargetMS    float64   `json:"queue_target_ms"`
	ResultTargetMS   float64   `json:"result_target_ms"`
	CoverageComplete bool      `json:"coverage_complete"`
	GapStart         time.Time `json:"gap_start,omitempty"`
	GapEnd           time.Time `json:"gap_end,omitempty"`
	Reports          []Report  `json:"reports"`
}

func (r *Recorder) View(at time.Time, duration time.Duration, minSamples uint64) View {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := r.state
	v := View{Generated: at, WindowSeconds: int(duration.Seconds()), QueueTargetMS: float64(s.QueueTarget) / float64(time.Millisecond), ResultTargetMS: float64(s.ResultTarget) / float64(time.Millisecond), GapStart: s.GapStart, GapEnd: s.GapEnd, Reports: []Report{}}
	v.CoverageComplete = !s.Started.After(at.Add(-duration)) && !s.GapEnd.After(at.Add(-duration))
	epoch := at.UnixNano() / int64(SlotDuration)
	slots := int64(duration / SlotDuration)
	for driver, window := range s.ByDriver {
		var total Bucket
		var overdue, pending uint64
		for _, b := range window {
			if b.Epoch > epoch || b.Epoch <= epoch-slots {
				continue
			}
			total.Queue.Merge(b.Queue)
			total.Execution.Merge(b.Execution)
			total.Result.Merge(b.Result)
			total.Samples += b.Samples
			if b.Expected > b.Samples {
				outstanding := b.Expected - b.Samples
				if !time.Unix(0, (b.Epoch+1)*int64(SlotDuration)).Add(s.ResultTarget).After(at) {
					overdue += outstanding
				} else {
					pending += outstanding
				}
			}
			total.Missed += b.Missed
			total.Timeouts += b.Timeouts
			total.QueueMet += b.QueueMet
			total.ResultMet += b.ResultMet
		}
		report := Report{Driver: driver, Samples: total.Samples, Overdue: overdue, Pending: pending, Expected: total.Samples + total.Missed + overdue, Missed: total.Missed, Timeouts: total.Timeouts, Queue: percentiles(total.Queue), Execution: percentiles(total.Execution), Result: percentiles(total.Result), QueueMet: total.QueueMet, ResultMet: total.ResultMet, Condition: "insufficient_observations"}
		if report.Expected > 0 {
			q := float64(report.QueueMet) / float64(report.Expected)
			e := float64(report.ResultMet) / float64(report.Expected)
			report.QueueAttainment = &q
			report.ResultAttainment = &e
		}
		if report.Samples >= minSamples {
			report.Condition = "healthy"
			if *report.QueueAttainment < .99 {
				report.Condition = "queue_delay"
			}
			if *report.ResultAttainment < .99 {
				report.Condition = "result_delay"
				if report.Execution.P99 != nil && *report.Execution.P99 >= v.ResultTargetMS {
					report.Condition = "downstream_limited"
				}
			}
			if !v.CoverageComplete && report.Condition == "healthy" {
				report.Condition = "coverage_gap"
			}
		}
		v.Reports = append(v.Reports, report)
	}
	slices.SortFunc(v.Reports, func(a, b Report) int {
		if a.Driver < b.Driver {
			return -1
		}
		if a.Driver > b.Driver {
			return 1
		}
		return 0
	})
	return v
}

// Validate rejects corrupt aggregate snapshots before they can reach readers.
func (s State) Validate() error {
	if s.ByDriver == nil {
		return nil
	} // no measurements were persisted yet
	if s.QueueTarget <= 0 || s.ResultTarget <= s.QueueTarget {
		return fmt.Errorf("invalid SLO thresholds")
	}
	if len(s.ByDriver) > 64 {
		return fmt.Errorf("invalid SLO driver cardinality")
	}
	for name, window := range s.ByDriver {
		if name == "" || window == nil {
			return fmt.Errorf("invalid SLO driver window")
		}
		for _, b := range window {
			if b.Epoch < 0 || b.QueueMet > b.Samples || b.ResultMet > b.Samples || b.Timeouts > b.Samples {
				return fmt.Errorf("invalid SLO counts")
			}
			for _, h := range []Histogram{b.Queue, b.Execution, b.Result} {
				var total uint64
				for _, n := range h.Buckets {
					total += n
				}
				if h.Count != total || h.Count > b.Samples {
					return fmt.Errorf("invalid SLO histogram")
				}
			}
		}
	}
	return nil
}
