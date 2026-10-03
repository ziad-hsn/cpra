//go:build externaljobs

package persistence

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/internal/slo"
)

const WorkerScheduleFormatVersion = 30
const workerScheduleSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-30\n"
const maxWorkerSchedules = math.MaxInt64 / slo.Slots

// WorkerScheduleTicket identifies one owner-issued obligation within a five
// second bucket. It conveys no permission to invoke a handler.
type WorkerScheduleTicket struct {
	OwnerEpoch string `json:"owner_epoch"`
	Epoch      int64  `json:"epoch"`
	Sequence   uint64 `json:"sequence"`
}

// Preserve the format-30 digest projection when live scheduling metadata grows.
type workerScheduleTicketDigestV2 struct {
	OwnerEpoch string `json:"owner_epoch"`
	Epoch      int64  `json:"epoch"`
	Sequence   uint64 `json:"sequence"`
}

func (t WorkerScheduleTicket) digestProjection() workerScheduleTicketDigestV2 {
	return workerScheduleTicketDigestV2{OwnerEpoch: t.OwnerEpoch, Epoch: t.Epoch, Sequence: t.Sequence}
}

func (t WorkerScheduleTicket) valid(at time.Time) bool {
	return catalogIdentifier(t.OwnerEpoch, 256) && t.Epoch == workerSLOEpoch(at) && t.Sequence > 0 && t.Sequence <= maxWorkerSchedules
}

type workerScheduleBucket struct {
	Epoch     int64  `json:"epoch"`
	Issued    uint64 `json:"issued"`
	Abandoned uint64 `json:"abandoned"`
}

// WorkerScheduleCommand checkpoints cumulative owner counters. Replaying an
// older checkpoint cannot subtract a later admission or add its obligation twice.
type WorkerScheduleCommand struct {
	OwnerEpoch   string                          `json:"owner_epoch"`
	ResultTarget time.Duration                   `json:"result_target"`
	Buckets      [slo.Slots]workerScheduleBucket `json:"buckets"`
}

func (c WorkerScheduleCommand) validate(at time.Time) error {
	if !catalogIdentifier(c.OwnerEpoch, 256) || c.ResultTarget <= 0 || at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	for n, b := range c.Buckets {
		if b.Issued == 0 {
			if b != (workerScheduleBucket{}) {
				return ErrWorkerExecutionInvalid
			}
			continue
		}
		if b.Issued > maxWorkerSchedules || b.Abandoned > b.Issued || b.Epoch < workerSLOEpoch(time.Time{}) || b.Epoch > workerSLOEpoch(at) || workerSLOSlot(b.Epoch) != n {
			return ErrWorkerExecutionInvalid
		}
	}
	return nil
}

type storeExtensions struct {
	// The ECS owner changes these counters; API readers take detached copies.
	// They contain 60 buckets, regardless of monitor or outstanding-work count.
	workerScheduleMu sync.Mutex
	workerSchedules  WorkerScheduleCommand
}

// ScheduleWorkerCheck records demand before preparation or admission. The
// returned ticket must stay with the original check across every local retry.
func (s *Store) ScheduleWorkerCheck(at time.Time) (WorkerScheduleTicket, error) {
	if at.IsZero() || at.Year() < 1 || at.Year() > 9998 || !catalogIdentifier(s.executorSession, 256) {
		return WorkerScheduleTicket{}, ErrWorkerExecutionInvalid
	}
	s.workerScheduleMu.Lock()
	defer s.workerScheduleMu.Unlock()
	c := &s.workerSchedules
	if c.OwnerEpoch == "" {
		c.OwnerEpoch, c.ResultTarget = s.executorSession, s.config.SLO.ResultTarget
	}
	epoch := workerSLOEpoch(at)
	b := &c.Buckets[workerSLOSlot(epoch)]
	if b.Issued > 0 && b.Epoch > epoch {
		return WorkerScheduleTicket{}, ErrWorkerExecutionExpired
	}
	if b.Epoch != epoch {
		*b = workerScheduleBucket{Epoch: epoch}
	}
	if b.Issued >= maxWorkerSchedules {
		return WorkerScheduleTicket{}, ErrWorkerExecutionQuota
	}
	b.Issued++
	return WorkerScheduleTicket{OwnerEpoch: c.OwnerEpoch, Epoch: epoch, Sequence: b.Issued}, nil
}

// AbandonWorkerCheck records a definitively unadmitted obligation. The owner
// consumes its ticket exactly once; uncertain submissions must settle first.
func (s *Store) AbandonWorkerCheck(ticket WorkerScheduleTicket) error {
	s.workerScheduleMu.Lock()
	defer s.workerScheduleMu.Unlock()
	if ticket.OwnerEpoch != s.workerSchedules.OwnerEpoch || ticket.Sequence == 0 {
		return ErrWorkerExecutionConflict
	}
	b := &s.workerSchedules.Buckets[workerSLOSlot(ticket.Epoch)]
	if b.Epoch > ticket.Epoch {
		return nil // Outside the retained window; never overwrite a newer slot.
	}
	if b.Epoch != ticket.Epoch || ticket.Sequence > b.Issued || b.Abandoned >= b.Issued {
		return ErrWorkerExecutionConflict
	}
	b.Abandoned++
	return nil
}

func (s *Store) workerScheduleSnapshot() WorkerScheduleCommand {
	c, _ := s.workerScheduleSnapshotContext(context.Background())
	return c
}

func (s *Store) workerScheduleSnapshotContext(ctx context.Context) (WorkerScheduleCommand, error) {
	if ctx == nil {
		return WorkerScheduleCommand{}, ErrWorkerExecutionInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return WorkerScheduleCommand{}, err
		}
		if s.workerScheduleMu.TryLock() {
			defer s.workerScheduleMu.Unlock()
			if err := ctx.Err(); err != nil {
				return WorkerScheduleCommand{}, err
			}
			return s.workerSchedules, nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return WorkerScheduleCommand{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// WorkerScheduleCheckpoint has no provider data and allocates no execution.
// The controller submits it with its normal aggregate checkpoint and retains
// the exact command while reconciling an uncertain commit.
func (s *Store) WorkerScheduleCheckpoint(at time.Time) *Command {
	c := s.workerScheduleSnapshot()
	if c.OwnerEpoch == "" {
		return nil
	}
	return &Command{Kind: "worker_schedule", At: at, commandExtensions: commandExtensions{WorkerSchedule: &c}}
}

// FinishWorkerSchedules runs after the dispatch pool is joined. No further
// admission can occur, so all issued obligations without a committed assignment
// are definitively abandoned, including entries still in the owner's ready queue.
func (s *Store) FinishWorkerSchedules() error {
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	s.workerScheduleMu.Lock()
	defer s.workerScheduleMu.Unlock()
	live := &s.workerSchedules
	if live.OwnerEpoch == "" {
		return nil
	}
	state := s.fsm.image.WorkerSLO
	next := *live
	for n, b := range live.Buckets {
		if b.Issued == 0 {
			continue
		}
		var admitted uint64
		if state != nil && state.ResultTarget == live.ResultTarget && state.ScheduleOwner == live.OwnerEpoch && state.Buckets[n].Epoch == b.Epoch {
			admitted = state.Buckets[n].Admitted
		}
		if admitted > b.Issued-b.Abandoned {
			return ErrWorkerExecutionConflict
		}
		next.Buckets[n].Abandoned = b.Issued - admitted
	}
	*live = next
	return nil
}

func (s *workerSLOImage) scheduleOwner(owner string) {
	if s.ScheduleOwner == owner {
		return
	}
	s.ScheduleOwner = owner
	for n := range s.Buckets {
		s.Buckets[n].Scheduled, s.Buckets[n].Abandoned, s.Buckets[n].Admitted = 0, 0, 0
	}
}

func (s *workerSLOImage) schedule(b workerScheduleBucket, at time.Time) error {
	if b.Issued == 0 {
		return nil
	}
	current := &s.Buckets[workerSLOSlot(b.Epoch)]
	if current.Expected > 0 && current.Epoch > b.Epoch {
		return nil
	}
	next := *current
	if next.Epoch != b.Epoch {
		next = workerSLOBucket{Epoch: b.Epoch}
	}
	issued, abandoned := max(next.Scheduled, b.Issued), max(next.Abandoned, b.Abandoned)
	if abandoned > issued || next.Admitted > issued-abandoned {
		return ErrWorkerExecutionConflict
	}
	if issued-next.Scheduled > maxWorkerSchedules-next.Expected {
		return ErrWorkerExecutionQuota
	}
	next.Expected += issued - next.Scheduled
	if abandoned-next.Abandoned > next.Expected-next.Completed-next.Missing {
		return ErrWorkerExecutionInvalid
	}
	next.Missing += abandoned - next.Abandoned
	next.Scheduled, next.Abandoned = issued, abandoned
	*current = next
	s.updated(at)
	return nil
}

func (s *workerSLOImage) schedules(c WorkerScheduleCommand, at time.Time) error {
	s.scheduleOwner(c.OwnerEpoch)
	for _, b := range c.Buckets {
		if err := s.schedule(b, at); err != nil {
			return err
		}
	}
	return nil
}

func (f *machine) applyWorkerSchedule(c WorkerScheduleCommand, at time.Time) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	epoch, err := f.workerWindowEpoch(c.ResultTarget)
	if err != nil {
		return Result{Err: err}
	}
	next := workerSLOImage{Epoch: epoch, ResultTarget: c.ResultTarget, Started: at, UpdatedAt: at}
	if old := f.image.WorkerSLO; old != nil && old.ResultTarget == c.ResultTarget {
		next = *old
	}
	if err := next.schedules(c, at); err != nil {
		return Result{Err: err}
	}
	f.image.WorkerSLO = &next
	f.image.Version = max(f.image.Version, WorkerScheduleFormatVersion)
	return Result{Allowed: true}
}
