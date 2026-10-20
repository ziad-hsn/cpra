package queue

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
)

func fairTestJob(driver string, flow uint64) *jobs.Dispatch {
	d := jobs.NewDispatch(newTestHybridJob(0), jobs.Result{}.Ent, "pulse", "", 1, 0)
	d.Driver, d.FlowKey = driver, flow
	return d
}

func TestFairQueueReservesAdmissionAndExecutionForOtherDrivers(t *testing.T) {
	q, _ := NewFairQueue(8, "http", "tcp")
	if err := q.SetDemand([]DriverDemand{{Driver: "http", Monitors: 4, Occupancy: 1}, {Driver: "tcp", Monitors: 4, Occupancy: 1}}); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		if err := q.Enqueue(fairTestJob("http", uint64(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Enqueue(fairTestJob("http", 2)); !errors.Is(err, ErrQueueFull) {
		t.Fatal("driver occupied reserved waiting capacity", err)
	}
	if err := q.Enqueue(fairTestJob("tcp", 1)); err != nil {
		t.Fatal(err)
	}
	first, _ := q.acquire(4)
	second, _ := q.acquire(4)
	if first == nil || second == nil || first.(*fairLease).driver.name == second.(*fairLease).driver.name {
		t.Fatal("other driver did not receive execution capacity")
	}
	stats := q.Stats()
	if stats.Dropped != 1 || len(stats.Drivers) != 2 {
		t.Fatal("admission accounting lost rejection")
	}
	first.(*fairLease).complete(jobs.Result{Err: context.DeadlineExceeded})
	second.(*fairLease).complete(jobs.Result{})
	if q.active != 0 || q.drivers["http"].timeouts != 1 {
		t.Fatal("reservation or timeout accounting leaked")
	}
}

func TestFairQueueNewFlowProgressesWhileExistingFlowIsHeld(t *testing.T) {
	q, _ := NewFairQueue(100, "http")
	for range 20 {
		if err := q.Enqueue(fairTestJob("http", 1)); err != nil {
			t.Fatal(err)
		}
	}
	var held []*fairLease
	for {
		j, _ := q.acquire(4)
		if j == nil {
			break
		}
		held = append(held, j.(*fairLease))
	}
	if len(held) == 0 || len(held) >= 4 {
		t.Fatal("existing flow borrowed all capacity")
	}
	// Distinct targets retain separate flows without fixed bucket collisions.
	other := uint64(65)
	if err := q.Enqueue(fairTestJob("http", other)); err != nil {
		t.Fatal(err)
	}
	j, _ := q.acquire(4)
	if j == nil || j.(*fairLease).Job.(*jobs.Dispatch).FlowKey != other {
		t.Fatal("new flow starved behind held work")
	}
	j.(*fairLease).complete(jobs.Result{})
	for _, lease := range held {
		lease.complete(jobs.Result{})
	}
	for {
		j, _ := q.Dequeue()
		if j == nil {
			break
		}
	}
	if q.active != 0 || len(q.drivers["http"].flows) != 0 || q.first != nil || q.last != nil {
		t.Fatal("drain leaked flow state")
	}
}

func TestActionQueueLateDestinationAdmittedBetweenStalledFlows(t *testing.T) {
	q, err := NewActionQueue(64, "notification")
	if err != nil {
		t.Fatal(err)
	}
	var held []*fairLease
	for _, flow := range []uint64{1, 2} {
		if err := q.Enqueue(fairTestJob("webhook", flow)); err != nil {
			t.Fatal(err)
		}
		job, err := q.acquire(4)
		if err != nil || job == nil {
			t.Fatal("stalled fixture did not start", err)
		}
		held = append(held, job.(*fairLease))
	}
	for n := 0; n < 128; n++ {
		err := q.Enqueue(fairTestJob("webhook", uint64(n%2+1)))
		if err != nil && !errors.Is(err, ErrQueueFull) {
			t.Fatal(err)
		}
	}
	if q.depth != 63 || q.active != 2 {
		t.Fatal("fixture did not saturate existing destinations", q.depth, q.active)
	}
	for n := 0; n < 4; n++ {
		if err := q.Enqueue(fairTestJob("webhook", 3)); err != nil {
			t.Fatal("late destination lost its admission slot", err)
		}
		job, err := q.acquire(4)
		if err != nil || job == nil || job.(*fairLease).flow.key != 3 {
			t.Fatal("healthy destination could not use spare execution capacity", err)
		}
		job.(*fairLease).complete(jobs.Result{})
	}
	for _, lease := range held {
		lease.complete(jobs.Result{})
	}
	if q.depth > q.capacity || len(q.drivers["webhook"].flows) > q.capacity {
		t.Fatal("flow capacity exceeded")
	}
}

func TestFairQueueLateCheckAdmittedBetweenStalledFlows(t *testing.T) {
	q, err := NewFairQueue(64, "http")
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 8; n++ {
		if err := q.Enqueue(fairTestJob("http", uint64(n%2+1))); err != nil {
			t.Fatal(err)
		}
	}
	var held []*fairLease
	for n := 0; n < 4; n++ {
		job, err := q.acquire(5)
		if err != nil || job == nil {
			t.Fatal("failed to occupy slow flows", err)
		}
		held = append(held, job.(*fairLease))
	}
	for n := 0; n < 128; n++ {
		if err := q.Enqueue(fairTestJob("http", uint64(n%2+1))); err != nil && !errors.Is(err, ErrQueueFull) {
			t.Fatal(err)
		}
	}
	if q.depth != 63 || q.active != 4 {
		t.Fatal("fixture did not refill both stalled flows", q.depth, q.active)
	}
	if err := q.Enqueue(fairTestJob("http", 3)); err != nil {
		t.Fatal("healthy check excluded from admission", err)
	}
	job, err := q.acquire(5)
	if err != nil || job == nil || job.(*fairLease).flow.key != 3 {
		t.Fatal("healthy check excluded from spare execution seat", err)
	}
	job.(*fairLease).complete(jobs.Result{})
	for _, lease := range held {
		lease.complete(jobs.Result{})
	}
}

func TestFairQueueBoundsFlowsAndReportsOldestPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _ := NewFairQueue(2000, "http")
		for n := uint64(0); n < 1000; n++ {
			if err := q.Enqueue(fairTestJob("http", n)); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Second)
		if len(q.drivers["http"].flows) != 1000 || q.Stats().OldestPendingAge != time.Second {
			t.Fatal("unbounded flow bookkeeping or wrong pending age")
		}
		q.Close()
		if !errors.Is(q.Enqueue(fairTestJob("tcp", 1)), ErrQueueClosed) {
			t.Fatal("closed queue accepted work")
		}
		for n := 0; n < 1000; n++ {
			j, err := q.acquire(8)
			if err != nil || j == nil {
				t.Fatal("accepted work lost on close")
			}
			j.(*fairLease).complete(jobs.Result{})
		}
		if _, err := q.acquire(8); !errors.Is(err, ErrQueueClosed) {
			t.Fatal("closed drain not terminal")
		}
	})
}

func TestSizingUsesActiveAgesAndPendingSubmissions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &sizingQueue{stats: Stats{EnqueueRate: 10}}
		p := newSizingPool(t, q)
		p.SetTargetWorkers(4)
		baseline := p.desiredCapacity(q.Stats())
		active := p.activeExecutions.begin(newTestHybridJob(1))
		p.activeExecutions.finish(active, true)
		pending := p.pendingSubmissions.begin(newTestHybridJob(2))
		p.pendingSubmissions.finish(pending, true)
		time.Sleep(5 * time.Second)
		got := p.desiredCapacity(q.Stats())
		if got <= baseline || got > 8 {
			t.Fatalf("censored executions did not produce bounded growth: %d baseline %d", got, baseline)
		}
		stats := p.Stats()
		if stats.ActiveExecutions != 1 || stats.PendingSubmissions != 1 || stats.WaitingTasks != 1 || stats.OldestExecutionAge != 5*time.Second || stats.OldestSubmissionAge != 5*time.Second {
			t.Fatal("active/pending accounting omitted accepted work", stats)
		}
		p.activeExecutions.take(active)
		p.pendingSubmissions.take(pending)
	})
}

func TestFairQueueLateThirdDriverRetainsAdmissionAndExecution(t *testing.T) {
	q, err := NewFairQueue(12, "http", "grpc", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetDemand([]DriverDemand{{Driver: "http", Monitors: 4, Occupancy: 1}, {Driver: "grpc", Monitors: 4, Occupancy: 1}, {Driver: "tcp", Monitors: 4, Occupancy: 1}}); err != nil {
		t.Fatal(err)
	}
	var held []*fairLease
	for _, driver := range []string{"http", "grpc"} {
		for i := range 4 {
			if err := q.Enqueue(fairTestJob(driver, uint64(i))); err != nil {
				t.Fatal(err)
			}
		}
		for {
			j, _ := q.acquire(5)
			if j == nil {
				break
			}
			held = append(held, j.(*fairLease))
		}
	}
	if err := q.Enqueue(fairTestJob("tcp", 1)); err != nil {
		t.Fatal("late driver lost waiting reservation", err)
	}
	j, _ := q.acquire(5)
	if j == nil || j.(*fairLease).driver.name != "tcp" {
		t.Fatal("late driver lost execution reservation")
	}
	j.(*fairLease).complete(jobs.Result{})
	for _, lease := range held {
		lease.complete(jobs.Result{})
	}
}

func TestFairCapacityFloorSurvivesIdleAndSmallTargets(t *testing.T) {
	q, _ := NewFairQueue(64, "http", "tcp", "grpc")
	cfg := DefaultWorkerPoolConfig()
	cfg.MinWorkers = 1
	cfg.MaxWorkers = 8
	cfg.FairDispatch = true
	p, err := NewDynamicWorkerPool(NewHandle(q), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.DrainAndStop()
	p.SetTargetWorkers(1)
	if p.Stats().CurrentCapacity != 1 {
		t.Fatal("idle compiled drivers reserved capacity")
	}
	p.SetArrivalRate(0)
	if p.desiredCapacity(Stats{}) != 1 {
		t.Fatal("empty catalog reserved capacity")
	}
	p.SetModelWorkers(2)
	if p.Stats().CurrentCapacity != 2 {
		t.Fatal("initial model multiplied by compiled drivers")
	}
	p.SetArrivalRate(100)
	p.SetServiceTime(.1)
	if got := p.desiredCapacity(Stats{QueueDepth: 20}); got < 2 || got > 4 {
		t.Fatal("catalog growth violated global bounds")
	}
}
