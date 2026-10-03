//go:build externaljobs

package persistence

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/slo"
)

const WorkerSLOFormatVersion = 25
const workerSLOSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-25\n"

// The aggregate counts admitted health checks, never recovery or notification
// latency. A single driver window bounds memory independently of the fleet and
// JobType count. Local pool feedback continues to use the local recorder only.
type workerSLOImage struct {
	ScheduleOwner string `json:"schedule_owner,omitempty"`
	// Epoch is a window identity, not a Raft index: several commands in one
	// committed batch may change the target and must still have distinct windows.
	Epoch        uint64                     `json:"epoch"`
	ResultTarget time.Duration              `json:"result_target"`
	Started      time.Time                  `json:"started"`
	UpdatedAt    time.Time                  `json:"updated_at"`
	Buckets      [slo.Slots]workerSLOBucket `json:"buckets"`
}

type workerSLOBucket struct {
	Scheduled uint64        `json:"scheduled,omitempty"`
	Abandoned uint64        `json:"abandoned,omitempty"`
	Admitted  uint64        `json:"admitted,omitempty"`
	Epoch     int64         `json:"epoch"`
	Expected  uint64        `json:"expected"`
	Completed uint64        `json:"completed"`
	Missing   uint64        `json:"missing"`
	Timeouts  uint64        `json:"timeouts"`
	ResultMet uint64        `json:"result_met"`
	Result    slo.Histogram `json:"result"`
}

func workerSLOEpoch(at time.Time) int64 {
	seconds := at.Unix()
	epoch := seconds / 5
	if seconds < 0 && seconds%5 != 0 {
		epoch--
	}
	return epoch
}

func workerSLOSlot(epoch int64) int { return int((epoch%slo.Slots + slo.Slots) % slo.Slots) }

func (s *workerSLOImage) bucket(r WorkerExecutionRecord) *workerSLOBucket {
	if s == nil || r.SLOEpoch != s.Epoch || r.ResultTarget != s.ResultTarget {
		return nil
	}
	b := &s.Buckets[workerSLOSlot(workerSLOEpoch(r.Intent.Scheduled))]
	if b.Expected == 0 || b.Epoch != workerSLOEpoch(r.Intent.Scheduled) {
		return nil
	}
	return b
}

func (s *workerSLOImage) updated(at time.Time) {
	if at.After(s.UpdatedAt) {
		s.UpdatedAt = at
	}
}

func (f *machine) workerWindowEpoch(target time.Duration) (uint64, error) {
	if target == 0 {
		return 0, nil
	}
	s := f.image.WorkerSLO
	if s == nil {
		return 1, nil
	}
	if s.ResultTarget == target {
		return s.Epoch, nil
	}
	if s.Epoch == math.MaxUint64 {
		return 0, ErrWorkerExecutionQuota
	}
	return s.Epoch + 1, nil
}

func (f *machine) admitWorkerSLO(r *WorkerExecutionRecord, at time.Time) error {
	if r.ResultTarget == 0 {
		return nil
	}
	epochID, err := f.workerWindowEpoch(r.ResultTarget)
	if err != nil {
		return err
	}
	s := f.image.WorkerSLO
	if s == nil || s.ResultTarget != r.ResultTarget {
		s = &workerSLOImage{Epoch: epochID, ResultTarget: r.ResultTarget, Started: at, UpdatedAt: at}
	}
	epoch := workerSLOEpoch(r.Intent.Scheduled)
	b := &s.Buckets[workerSLOSlot(epoch)]
	if r.Intent.Schedule == nil && b.Expected != 0 && b.Epoch == epoch && b.Expected >= math.MaxInt64/slo.Slots {
		return ErrWorkerExecutionQuota
	}
	r.SLOEpoch = s.Epoch
	if ticket := r.Intent.Schedule; ticket != nil {
		if s.ScheduleOwner == ticket.OwnerEpoch && b.Epoch == ticket.Epoch && max(b.Scheduled, ticket.Sequence) <= b.Admitted+b.Abandoned {
			return ErrWorkerExecutionConflict
		}
		if s.ScheduleOwner != ticket.OwnerEpoch {
			copy := *s
			s = &copy
		}
		s.scheduleOwner(ticket.OwnerEpoch)
		if err := s.schedule(workerScheduleBucket{Epoch: ticket.Epoch, Issued: ticket.Sequence}, at); err != nil {
			return err
		}
		bucket := &s.Buckets[workerSLOSlot(ticket.Epoch)]
		if bucket.Epoch == ticket.Epoch {
			if bucket.Admitted >= bucket.Scheduled-bucket.Abandoned {
				return ErrWorkerExecutionConflict
			}
			bucket.Admitted++
		}
		f.image.WorkerSLO = s
		f.image.Version = max(f.image.Version, WorkerScheduleFormatVersion)
		return nil
	}
	// Very late admission must not erase a newer slot. Its observation belongs
	// outside the retained window and cannot later create a latency sample.
	if b.Expected == 0 || b.Epoch <= epoch {
		if b.Epoch != epoch {
			*b = workerSLOBucket{Epoch: epoch}
		}
		b.Expected++
	}
	s.updated(at)
	f.image.WorkerSLO = s
	f.image.Version = max(f.image.Version, WorkerSLOFormatVersion)
	return nil
}

func (f *machine) finishWorkerSLO(r WorkerExecutionRecord, at time.Time) {
	b := f.image.WorkerSLO.bucket(r)
	if b == nil {
		return
	}
	if r.Result != nil && r.Result.Applied && !r.Result.NoData {
		b.Completed++
	} else {
		b.Missing++
		if !at.Before(r.Intent.Deadline) {
			b.Timeouts++
		}
	}
	f.image.WorkerSLO.updated(at)
}

func validateWorkerSLO(i image) error {
	s := i.WorkerSLO
	if s == nil {
		return nil
	}
	if s.ScheduleOwner != "" && (i.Version < WorkerScheduleFormatVersion || !catalogIdentifier(s.ScheduleOwner, 256)) {
		return ErrWorkerExecutionInvalid
	}
	if i.Version < WorkerSLOFormatVersion || s.Epoch == 0 || s.ResultTarget <= 0 ||
		s.Started.IsZero() || s.Started.Year() < 1 || s.Started.Year() > 9998 || s.UpdatedAt.Before(s.Started) || s.UpdatedAt.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	for n, b := range s.Buckets {
		if b.Scheduled > b.Expected || b.Abandoned > b.Scheduled || b.Admitted > b.Scheduled-b.Abandoned || b.Abandoned > b.Missing || s.ScheduleOwner == "" && (b.Scheduled != 0 || b.Abandoned != 0 || b.Admitted != 0) {
			return ErrWorkerExecutionInvalid
		}
		if b.Expected == 0 {
			if b != (workerSLOBucket{}) {
				return ErrWorkerExecutionInvalid
			}
			continue
		}
		if b.Expected > math.MaxInt64/slo.Slots || b.Completed > b.Expected || b.Missing > b.Expected-b.Completed ||
			b.Timeouts > b.Missing || b.Result.Count > b.Completed || b.ResultMet > b.Result.Count ||
			workerSLOSlot(b.Epoch) != n || b.Epoch > workerSLOEpoch(s.UpdatedAt) || b.Epoch < workerSLOEpoch(time.Time{}) {
			return ErrWorkerExecutionInvalid
		}
		var count uint64
		for _, value := range b.Result.Buckets {
			if value > b.Result.Count-count {
				return ErrWorkerExecutionInvalid
			}
			count += value
		}
		if count != b.Result.Count {
			return ErrWorkerExecutionInvalid
		}
	}
	return nil
}

func (s workerSLOImage) report(at time.Time, minimumSamples uint64) slo.Report {
	r := slo.Report{Driver: "external", Condition: "insufficient_observations"}
	var histogram slo.Histogram
	epoch := workerSLOEpoch(at)
	for _, b := range s.Buckets {
		if b.Epoch > epoch || b.Epoch <= epoch-slo.Slots {
			continue
		}
		histogram.Merge(b.Result)
		r.ResultMet += b.ResultMet
		r.Timeouts += b.Timeouts
		// A known outcome without its first confirmation measurement remains in
		// the denominator. A later duplicate cannot turn it into a fast result.
		r.Missed += b.Missing + b.Completed - b.Result.Count
		outstanding := b.Expected - b.Completed - b.Missing
		if !time.Unix((b.Epoch+1)*5, 0).Add(s.ResultTarget).After(at) {
			r.Overdue += outstanding
		} else {
			r.Pending += outstanding
		}
	}
	r.Samples = histogram.Count
	r.Expected = r.Samples + r.Missed + r.Overdue
	r.Result = slo.Percentiles{P50: histogram.Percentile(.5), P95: histogram.Percentile(.95), P99: histogram.Percentile(.99)}
	if r.Expected > 0 {
		attainment := float64(r.ResultMet) / float64(r.Expected)
		r.ResultAttainment = &attainment
	}
	if r.Samples >= minimumSamples && r.Expected > 0 {
		switch {
		case *r.ResultAttainment < .99:
			r.Condition = "result_delay"
		case s.Started.After(at.Add(-slo.Slots*slo.SlotDuration)) || r.Missed > 0:
			r.Condition = "coverage_gap"
		default:
			r.Condition = "timing_unavailable"
		}
	}
	// A start grant is not remote handler start. Queue/execution distributions
	// and queue attainment deliberately remain unavailable in this protocol.
	return r
}

func (s *Store) extendSLOView(ctx context.Context, view *slo.View) error {
	live, err := s.workerScheduleSnapshotContext(ctx)
	if err != nil {
		return err
	}
	// The owner records schedules never admitted and deliberate pauses locally.
	// Admitted work is counted by the committed worker window. Merge only those
	// disjoint observations, with one public report for the external driver.
	var scheduler slo.Report
	for n, report := range view.Reports {
		if report.Driver == "external" {
			scheduler = report
			view.Reports = append(view.Reports[:n], view.Reports[n+1:]...)
			break
		}
	}
	merge := func(r slo.Report) {
		r.Missed += scheduler.Missed
		r.Expected += scheduler.Missed
		r.PausedMonitors, r.PausedMonitorSeconds = scheduler.PausedMonitors, scheduler.PausedMonitorSeconds
		if r.Expected > 0 {
			attainment := float64(r.ResultMet) / float64(r.Expected)
			r.ResultAttainment = &attainment
			if r.Samples >= uint64(s.config.SLO.MinimumSamples) && attainment < .99 {
				r.Condition = "result_delay"
			}
		}
		view.Reports = append(view.Reports, r)
		slices.SortFunc(view.Reports, func(a, b slo.Report) int { return strings.Compare(a.Driver, b.Driver) })
		view.CoverageComplete = false // Remote queue and execution timing are unavailable.
	}
	if err := collectionReadLock(ctx, &s.fsm.mu); err != nil {
		return err
	}
	state := s.fsm.image.WorkerSLO
	if state == nil && live.OwnerEpoch != "" {
		state = &workerSLOImage{Epoch: 1, ResultTarget: live.ResultTarget, Started: view.Generated, UpdatedAt: view.Generated}
	}
	if state == nil {
		unmeasured := false
		if records := s.fsm.image.WorkerExecutions; records != nil {
			// At most MaxWorkerExecutions records. Pre-format-25 admissions have
			// no frozen threshold; recovery must not invent one from new config.
			for _, record := range records.Records {
				if err := ctx.Err(); err != nil {
					s.fsm.mu.RUnlock()
					return err
				}
				if record.Intent.Category == "check" {
					unmeasured = true
					break
				}
			}
		}
		s.fsm.mu.RUnlock()
		if unmeasured || scheduler.Driver != "" {
			merge(slo.Report{Driver: "external", Condition: "timing_unavailable"})
		}
		return ctx.Err()
	}
	copy := *state
	s.fsm.mu.RUnlock()
	if live.OwnerEpoch != "" && copy.ResultTarget == live.ResultTarget {
		if err := copy.schedules(live, view.Generated); err != nil {
			merge(slo.Report{Driver: "external", Condition: "coverage_gap"})
			return ctx.Err()
		}
	}
	r := slo.Report{Driver: "external", Condition: "coverage_gap"}
	if copy.ResultTarget == s.config.SLO.ResultTarget {
		r = copy.report(view.Generated, uint64(s.config.SLO.MinimumSamples))
	}
	if !view.CoverageComplete && r.Condition == "timing_unavailable" {
		r.Condition = "coverage_gap"
	}
	merge(r)
	return ctx.Err()
}
