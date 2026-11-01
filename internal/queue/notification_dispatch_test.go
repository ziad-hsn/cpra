package queue

import (
	"context"
	"errors"
	"fmt"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"testing"
	"time"
)

func TestNotificationUnreconciledAdmissionBound(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 4
	var outcomes []jobs.Result
	for i := 0; i < 1000; i++ {
		d := notificationDispatch(fmt.Sprint(i))
		d.FlowKey = uint64(i + 1)
		err = q.Enqueue(d)
		if errors.Is(err, ErrQueueFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		j, err := q.acquire(workers)
		if err != nil || j == nil {
			t.Fatal("independent destination stalled", err)
		}
		l := j.(*fairLease)
		r := l.Execute()
		l.complete(r)
		outcomes = append(outcomes, r)
	}
	if len(outcomes) != q.capacity+workers || q.outstanding != len(outcomes) || len(q.drivers["webhook"].flows) != len(outcomes) {
		t.Fatalf("unbounded retained work: results=%d outstanding=%d flows=%d", len(outcomes), q.outstanding, len(q.drivers["webhook"].flows))
	}
	outcomes[0].ReleaseDispatch()
	d := notificationDispatch("reclaimed")
	d.FlowKey = 1001
	if err := q.Enqueue(d); err != nil {
		t.Fatal("acknowledgement did not restore admission", err)
	}
	for _, r := range outcomes {
		r.ReleaseDispatch()
	}
	if q.outstanding != 0 || len(q.drivers["webhook"].flows) != 1 {
		t.Fatal("acknowledgement leaked bookkeeping")
	}
}

func TestNotificationOwnerAcknowledgesBeforePublicationCompletes(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := q.Enqueue(notificationDispatch("a")); err != nil {
			t.Fatal(err)
		}
	}
	j, _ := q.acquire(8)
	first := j.(*fairLease)
	r := first.Execute()
	r.ReleaseDispatch()
	if got := q.Stats().AwaitingReconciliation; got != 0 {
		t.Fatal("acknowledged active dispatch counted as awaiting reconciliation", got)
	}
	j, _ = q.acquire(8)
	if j == nil {
		t.Fatal("acknowledged sequence remains blocked")
	}
	second := j.(*fairLease)
	first.complete(r)
	r.ReleaseDispatch()
	if got := q.Stats().AwaitingReconciliation; got != 0 {
		t.Fatal("completed acknowledgement changed reconciliation gauge", got)
	}
	if next, _ := q.acquire(8); next != nil {
		t.Fatal("old callback released the newer reservation")
	}
	second.complete(jobs.Result{})
	j, _ = q.acquire(8)
	if j == nil {
		t.Fatal("second reservation did not release")
	}
	j.(*fairLease).complete(jobs.Result{})
	if q.outstanding != 0 || q.active != 0 || len(q.drivers["webhook"].flows) != 0 {
		t.Fatal("handoff leaked a reservation")
	}
}

func TestNotificationPoolDrainsRelatedSequences(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultWorkerPoolConfig()
	cfg.FairDispatch, cfg.MinWorkers, cfg.MaxWorkers, cfg.NumShards = true, 2, 2, 1
	cfg.AdjustmentInterval, cfg.ResultChannelDepth, cfg.ResultBatchSize, cfg.ResultBatchTimeout = 0, 1, 1, time.Millisecond
	p, err := NewDynamicWorkerPool(NewHandle(q), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := q.Enqueue(notificationDispatch("a")); err != nil {
			t.Fatal(err)
		}
	}
	p.Start()
	stopped := make(chan struct{})
	go func() { p.DrainAndStop(); close(stopped) }()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	received := 0
	for {
		select {
		case batch, ok := <-p.GetRouter().CodeResultChan:
			if !ok {
				select {
				case <-stopped:
				case <-ctx.Done():
					t.Fatal("drain did not finish")
				}
				q.mu.Lock()
				defer q.mu.Unlock()
				if received != 2 || q.outstanding != 0 || q.active != 0 || len(q.drivers["webhook"].flows) != 0 {
					t.Fatal("drain lost work or retained fences", received)
				}
				return
			}
			for _, r := range batch {
				if r.ReleaseDispatch == nil {
					t.Fatal("missing acknowledgement")
				}
				r.ReleaseDispatch()
				received++
			}
		case <-ctx.Done():
			p.CancelWork()
			t.Fatal("related sequences did not drain")
		}
	}
}

type immediateNotification struct{ jobs.Execution }

func (j *immediateNotification) Execute() jobs.Result    { return jobs.Result{Type: "code"} }
func (j *immediateNotification) Copy() jobs.Job          { return &immediateNotification{} }
func (*immediateNotification) GetEnqueueTime() time.Time { return time.Time{} }
func (*immediateNotification) SetEnqueueTime(time.Time)  {}
func (*immediateNotification) GetStartTime() time.Time   { return time.Time{} }
func (*immediateNotification) SetStartTime(time.Time)    {}
func (j *immediateNotification) IsNil() bool             { return j == nil }
func notificationDispatch(sequence string) *jobs.Dispatch {
	d := jobs.NewDispatch(&immediateNotification{}, ecs.Entity{}, "code", "red", 0, 0)
	d.Driver = "webhook"
	d.FlowKey = 7
	d.MonitorID = "monitor"
	d.NotificationCorrelation = sequence
	d.FenceResult = true
	return d
}
func TestNotificationConcurrencyAndDurableSequenceFence(t *testing.T) {
	q, err := NewActionQueue(64, "notification")
	if err != nil {
		t.Fatal(err)
	}
	for _, sequence := range []string{"a", "a", "b", "c", "d", "e"} {
		if err = q.Enqueue(notificationDispatch(sequence)); err != nil {
			t.Fatal(err)
		}
	}
	leases := make([]*fairLease, 0, 4)
	for range 4 {
		j, e := q.acquire(16)
		if e != nil || j == nil {
			t.Fatal("independent sequence blocked", e)
		}
		leases = append(leases, j.(*fairLease))
	}
	if j, _ := q.acquire(16); j != nil {
		t.Fatal("destination cap exceeded")
	}
	seen := map[string]bool{}
	for _, l := range leases {
		key := l.Job.(*jobs.Dispatch).NotificationCorrelation
		if seen[key] {
			t.Fatal("same sequence concurrent")
		}
		seen[key] = true
	}
	result := leases[0].Execute()
	leases[0].complete(result)
	if result.ReleaseDispatch == nil {
		t.Fatal("result lost reconciliation fence")
	}
	stats := q.Stats()
	if stats.AwaitingReconciliation != 1 {
		t.Fatal("completed invocation lost its reconciliation reservation", stats)
	}
	for _, driver := range stats.Drivers {
		if driver.Driver == "webhook" && (driver.DestinationConcurrency == nil || *driver.DestinationConcurrency != 4 || driver.WorkerHeadroom <= 0) {
			t.Fatal("worker headroom or destination bound misreported", driver)
		}
	}
	next, _ := q.acquire(16)
	if next == nil || next.(*fairLease).Job.(*jobs.Dispatch).NotificationCorrelation != "e" {
		t.Fatal("held first sequence blocked unrelated work")
	}
	next.(*fairLease).complete(jobs.Result{})
	if j, _ := q.acquire(16); j != nil {
		t.Fatal("provider return released sequence before durable result")
	}
	result.ReleaseDispatch()
	result.ReleaseDispatch()
	if q.Stats().AwaitingReconciliation != 0 {
		t.Fatal("idempotent reconciliation retained a completed reservation")
	}
	next, _ = q.acquire(16)
	if next == nil || next.(*fairLease).Job.(*jobs.Dispatch).NotificationCorrelation != "a" {
		t.Fatal("reconciled sequence did not resume")
	}
	next.(*fairLease).complete(jobs.Result{})
	for _, l := range leases[1:] {
		l.complete(jobs.Result{})
	}
	if q.active != 0 || q.depth != 0 || len(q.drivers["webhook"].flows) != 0 {
		t.Fatal("flow bookkeeping survived completion")
	}
}
func TestNotificationConcurrencyRespectsGlobalAndConfiguredLimits(t *testing.T) {
	for _, limit := range []int{1, 2, 8} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			q, e := NewActionQueue(64, "notification")
			if e != nil {
				t.Fatal(e)
			}
			if e = q.SetNotificationConcurrency(map[string]int{"webhook": limit}); e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 20; i++ {
				if e = q.Enqueue(notificationDispatch(fmt.Sprint(i))); e != nil {
					t.Fatal(e)
				}
			}
			active := 0
			var leases []*fairLease
			for {
				j, e := q.acquire(4)
				if e != nil {
					t.Fatal(e)
				}
				if j == nil {
					break
				}
				active++
				leases = append(leases, j.(*fairLease))
			}
			if active < 1 || active > limit || active > 4 {
				t.Fatal("invalid bounds", active)
			}
			for _, l := range leases {
				l.complete(jobs.Result{})
			}
		})
	}
}

func TestNotificationRateDeferralsCountAdmissionsNotPolls(t *testing.T) {
	q, err := NewActionQueue(64, "notification")
	if err != nil {
		t.Fatal(err)
	}
	d := notificationDispatch("paced")
	d.RateScopes = []jobs.NotificationRateScope{{ID: "private-scope", Interval: time.Second}}
	q.SetNotificationRates(map[string]jobs.NotificationRateState{"private-scope": {NotBefore: time.Now().Add(time.Minute)}})
	if err := q.Enqueue(d); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if job, err := q.acquire(4); err != nil || job != nil {
			t.Fatal("paced admission started", err)
		}
	}
	stats := q.Stats()
	for _, driver := range stats.Drivers {
		if driver.Driver == "webhook" && (driver.RateDeferredTotal != 1 || driver.Active != 0 || driver.Pending != 1 || driver.WorkerHeadroom <= 0) {
			t.Fatal("pacing counter inflated or capacity conflated", driver)
		}
	}
}
