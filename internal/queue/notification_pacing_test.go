package queue

import (
	"context"
	"errors"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"testing"
	"time"
)

func TestNotificationPacingCancellationReclaimsPausedEntries(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	d := notificationDispatch("cancelled")
	d.MonitorID, d.ActionID = "monitor", "action"
	d.Before = func(context.Context) error { called = true; return nil }
	d.ConfigureNotificationPacing("destination", 0, "")
	q.SetNotificationRates(map[string]jobs.NotificationRateState{d.RateScopes[0].ID: {NotBefore: time.Now().Add(24 * time.Hour)}})
	if err := q.Enqueue(d); err != nil {
		t.Fatal(err)
	}
	if j, _ := q.acquire(4); j != nil {
		t.Fatal("pause bypassed before cancellation")
	}
	q.CancelPending("monitor", func(string) bool { return false })
	j, err := q.acquire(4)
	if err != nil || j == nil {
		t.Fatal("cancelled entry stuck behind provider pause", err)
	}
	r := j.Execute()
	if called || !errors.Is(r.Err, context.Canceled) || !r.ExecutionStart.IsZero() || len(q.pendingActions) != 0 || q.depth != 0 || q.active != 0 || q.outstanding != 0 {
		t.Fatal("cancellation invoked work or leaked capacity")
	}
}

func TestCancelledSequenceCannotReleaseAnotherExecution(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		d := notificationDispatch("same-sequence")
		d.MonitorID, d.ActionID = "m", id
		d.ConfigureNotificationPacing("destination", 0, "")
		if err := q.Enqueue(d); err != nil {
			t.Fatal(err)
		}
	}
	j, _ := q.acquire(4)
	if j == nil {
		t.Fatal("first blocked")
	}
	first := j.(*fairLease)
	result := first.Execute()
	first.complete(result)
	q.CancelPending("m", func(id string) bool { return id != "b" })
	j, _ = q.acquire(4)
	if j == nil {
		t.Fatal("cancelled entry blocked")
	}
	if _, ok := j.(*fairLease); ok {
		t.Fatal("cancelled entry acquired another reservation")
	}
	if r := j.Execute(); r.ReleaseDispatch != nil || !errors.Is(r.Err, context.Canceled) {
		t.Fatal("cancelled entry owns execution")
	}
	if next, _ := q.acquire(4); next != nil {
		t.Fatal("cancellation released active sequence")
	}
	result.ReleaseDispatch()
	j, _ = q.acquire(4)
	if j == nil {
		t.Fatal("own acknowledgement did not release sequence")
	}
	last := j.(*fairLease)
	r := last.Execute()
	last.complete(r)
	r.ReleaseDispatch()
	if q.outstanding != 0 || q.active != 0 || q.depth != 0 {
		t.Fatal("cancellation leaked capacity")
	}
}

func TestNotificationPacingDrainCancelsBeforeProviderPermission(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		d := notificationDispatch("paused")
		d.ConfigureNotificationPacing("destination", 0, "")
		q.SetNotificationRates(map[string]jobs.NotificationRateState{d.RateScopes[0].ID: {NotBefore: time.Now().Add(24 * time.Hour)}})
		d.Before = func(context.Context) error { t.Error("drain invoked provider preparation"); return nil }
		if err := q.Enqueue(d); err != nil {
			t.Fatal(err)
		}
	}
	cfg := DefaultWorkerPoolConfig()
	cfg.FairDispatch = true
	cfg.MinWorkers = 2
	cfg.MaxWorkers = 4
	cfg.NumShards = 1
	cfg.AdjustmentInterval = 0
	cfg.DrainTimeout = 20 * time.Millisecond
	cfg.ResultBatchTimeout = time.Millisecond
	p, err := NewDynamicWorkerPool(NewHandle(q), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Start()
	done := make(chan struct{})
	go func() { p.DrainAndStop(); close(done) }()
	count := 0
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case batch, ok := <-p.GetRouter().CodeResultChan:
			if !ok {
				<-done
				if count != 8 || q.depth != 0 || q.active != 0 || q.outstanding != 0 {
					t.Fatal("drain lost results", count)
				}
				return
			}
			for _, r := range batch {
				if !errors.Is(r.Err, context.Canceled) || !r.ExecutionStart.IsZero() {
					t.Fatal("drain invoked action")
				}
				if r.ReleaseDispatch != nil {
					r.ReleaseDispatch()
				}
				count++
			}
		case <-deadline.C:
			t.Fatal("drain waited for provider cooldown")
		}
	}
}

func TestNotificationPacingWaitsBeforeWorkerAndAfterProviderReturn(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	for n, seq := range []string{"a", "b", "other"} {
		d := notificationDispatch(seq)
		d.FlowKey = uint64(n + 1)
		key := "shared"
		if n == 2 {
			key = "independent"
		}
		d.ConfigureNotificationPacing(key, 20*time.Millisecond, "")
		if err := q.Enqueue(d); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := q.acquire(8)
	if first == nil {
		t.Fatal("first request blocked")
	}
	other, _ := q.acquire(8)
	if other == nil || other.(*fairLease).Job.(*jobs.Dispatch).NotificationCorrelation != "other" {
		t.Fatal("paced scope blocked independent work")
	}
	other.(*fairLease).complete(jobs.Result{})
	// Even a delayed grant beyond the original interval cannot overlap sends.
	time.Sleep(25 * time.Millisecond)
	if job, _ := q.acquire(8); job != nil {
		t.Fatal("late grant released concurrent request")
	}
	lease := first.(*fairLease)
	result := lease.Execute()
	lease.complete(result)
	if job, _ := q.acquire(8); job != nil {
		t.Fatal("result not durably acknowledged")
	}
	result.ReleaseDispatch()
	if job, _ := q.acquire(8); job != nil {
		t.Fatal("release bypassed post-completion interval")
	}
	time.Sleep(25 * time.Millisecond)
	job, _ := q.acquire(8)
	if job == nil {
		t.Fatal("eligible request did not resume")
	}
	job.(*fairLease).complete(jobs.Result{})
	if q.active != 0 || q.outstanding != 0 {
		t.Fatal("pacing leaked execution capacity")
	}
}

func TestNotificationPacingActiveRetryRestrictsNewPolicy(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	old := notificationDispatch("retry")
	old.ConfigureNotificationPacing("same-destination", time.Second, "")
	next := notificationDispatch("new")
	next.ConfigureNotificationPacing("same-destination", 0, "")
	if err := q.Enqueue(old); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(next); err != nil {
		t.Fatal(err)
	}
	j, _ := q.acquire(4)
	if j == nil {
		t.Fatal("retry did not acquire capacity")
	}
	if j, _ := q.acquire(4); j != nil {
		t.Fatal("new policy overlapped a paced retry")
	}
	l := j.(*fairLease)
	r := l.Execute()
	l.complete(r)
	r.ReleaseDispatch()
	if j, _ := q.acquire(4); j != nil {
		t.Fatal("new policy bypassed retry completion interval")
	}
}

func TestNotificationPacingSerialAdaptiveScope(t *testing.T) {
	q, err := NewActionQueue(32, "notification")
	if err != nil {
		t.Fatal(err)
	}
	for n, seq := range []string{"a", "b"} {
		d := notificationDispatch(seq)
		d.FlowKey = uint64(n + 1)
		d.RateScopes = []jobs.NotificationRateScope{{ID: "account", Serial: true}}
		if err := q.Enqueue(d); err != nil {
			t.Fatal(err)
		}
	}
	j, _ := q.acquire(8)
	if j == nil {
		t.Fatal("first dispatch unavailable")
	}
	if next, _ := q.acquire(8); next != nil {
		t.Fatal("adaptive scope overlapped accepted operation")
	}
	lease := j.(*fairLease)
	r := lease.Execute()
	// The controller publishes the committed response pause before releasing
	// the original result's fence; release must not shorten that pause.
	q.SetNotificationRates(map[string]jobs.NotificationRateState{"account": {NotBefore: time.Now().Add(time.Minute)}})
	lease.complete(r)
	r.ReleaseDispatch()
	if next, _ := q.acquire(8); next != nil {
		t.Fatal("learned account cooldown lost at completion")
	}
}
