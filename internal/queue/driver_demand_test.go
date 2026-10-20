package queue

import (
	"errors"
	"testing"

	"github.com/ziad-hsn/cpra/internal/jobs"
)

func TestDemandReservationsBorrowOnlySpareCapacity(t *testing.T) {
	q, _ := NewFairQueue(64, "http", "tcp", "grpc")
	if err := q.SetDemand([]DriverDemand{{Driver: "http", Monitors: 20, Occupancy: 3}, {Driver: "tcp", Monitors: 1, Occupancy: .5}}); err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 20; i++ {
		if err := q.Enqueue(fairTestJob("http", i)); err != nil {
			t.Fatal(err)
		}
	}
	var leases []*fairLease
	for {
		job, _ := q.acquire(8)
		if job == nil {
			break
		}
		leases = append(leases, job.(*fairLease))
	}
	if len(leases) != 7 {
		t.Fatalf("borrowed %d; want 4 reserved plus 3 spare", len(leases))
	}
	if err := q.Enqueue(fairTestJob("tcp", 1)); err != nil {
		t.Fatal(err)
	}
	job, _ := q.acquire(8)
	if job == nil || job.(*fairLease).driver.name != "tcp" {
		t.Fatal("configured idle reservation was lent away")
	}
	job.(*fairLease).complete(jobs.Result{})
	for _, l := range leases {
		l.complete(jobs.Result{})
	}
	if q.requestedCapacity() != 5 {
		t.Fatal("compiled idle driver consumed a floor")
	}
}

func TestConstrainedReservationsDoNotStarveSmallDrivers(t *testing.T) {
	for _, capacity := range []int{1, 2, 3} {
		q, _ := NewFairQueue(1024, "http", "tcp", "grpc")
		if err := q.SetDemand([]DriverDemand{{Driver: "http", Monitors: 100, Occupancy: 100}, {Driver: "tcp", Monitors: 1, Occupancy: .5}, {Driver: "grpc", Monitors: 1, Occupancy: .5}}); err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for n := 0; n < 30; n++ {
			for _, name := range []string{"http", "tcp", "grpc"} {
				if q.drivers[name].depth == 0 {
					if err := q.Enqueue(fairTestJob(name, 1)); err != nil {
						t.Fatal(err)
					}
				}
			}
			job, _ := q.acquire(capacity)
			if job == nil {
				t.Fatalf("capacity %d deadlocked while no work active", capacity)
			}
			l := job.(*fairLease)
			seen[l.driver.name]++
			l.complete(jobs.Result{})
		}
		if len(seen) != 3 {
			t.Fatalf("capacity %d starved a driver: %v", capacity, seen)
		}
	}
}

func TestConstrainedIdleDriverCannotPreventDispatch(t *testing.T) {
	q, _ := NewFairQueue(64, "http", "tcp", "grpc")
	if err := q.SetDemand([]DriverDemand{{Driver: "http", Monitors: 100, Occupancy: 100}, {Driver: "tcp", Monitors: 1, Occupancy: .5}, {Driver: "grpc", Monitors: 1, Occupancy: .5}}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(fairTestJob("tcp", 1)); err != nil {
		t.Fatal(err)
	}
	job, _ := q.acquire(1)
	if job == nil || job.(*fairLease).driver.name != "tcp" {
		t.Fatal("unfulfillable idle reservation deadlocked the only pending driver")
	}
	job.(*fairLease).complete(jobs.Result{})
}

func TestConstrainedReservationsAccountForOutstandingLease(t *testing.T) {
	q, _ := NewFairQueue(64, "http", "tcp", "grpc")
	if err := q.SetDemand([]DriverDemand{{Driver: "http", Monitors: 10, Occupancy: 10}, {Driver: "tcp", Monitors: 1, Occupancy: 1}, {Driver: "grpc", Monitors: 1, Occupancy: 1}}); err != nil {
		t.Fatal(err)
	}
	_ = q.Enqueue(fairTestJob("http", 1))
	first, _ := q.acquire(2)
	if first == nil {
		t.Fatal("no held lease")
	}
	defer first.(*fairLease).complete(jobs.Result{})
	for _, driver := range []string{"tcp", "grpc"} {
		if err := q.Enqueue(fairTestJob(driver, 1)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for range 2 {
		j, _ := q.acquire(2)
		if j == nil {
			t.Fatal("held lease double counted; free worker stranded")
		}
		l := j.(*fairLease)
		seen[l.driver.name] = true
		if q.active != 2 {
			t.Fatal("capacity accounting lost a lease")
		}
		l.complete(jobs.Result{})
	}
	if !seen["tcp"] || !seen["grpc"] {
		t.Fatal("small drivers failed to progress while HTTP stalled")
	}
}

func TestActionDestinationCannotFillAdmissionOrExecution(t *testing.T) {
	q, _ := NewActionQueue(64, "notification")
	for range 32 {
		if err := q.Enqueue(fairTestJob("webhook", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Enqueue(fairTestJob("webhook", 1)); !errors.Is(err, ErrQueueFull) {
		t.Fatal("unbounded destination backlog", err)
	}
	held, _ := q.acquire(4)
	if held == nil {
		t.Fatal("no first action")
	}
	if job, _ := q.acquire(4); job != nil {
		t.Fatal("same destination executed concurrently")
	}
	if err := q.Enqueue(fairTestJob("webhook", 2)); err != nil {
		t.Fatal("healthy destination denied admission", err)
	}
	job, _ := q.acquire(4)
	if job == nil || job.(*fairLease).flow.key != 2 {
		t.Fatal("healthy destination starved")
	}
	job.(*fairLease).complete(jobs.Result{})
	held.(*fairLease).complete(jobs.Result{})
}

func TestPulseFlowCannotOccupyNewFlowAdmission(t *testing.T) {
	q, _ := NewFairQueue(64, "http")
	for range 32 {
		if err := q.Enqueue(fairTestJob("http", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Enqueue(fairTestJob("http", 1)); !errors.Is(err, ErrQueueFull) {
		t.Fatal("stalled pulse flow filled the queue", err)
	}
	var held []*fairLease
	for {
		j, _ := q.acquire(4)
		if j == nil {
			break
		}
		held = append(held, j.(*fairLease))
	}
	if len(held) != 3 {
		t.Fatal("missing reserved execution seat")
	}
	if err := q.Enqueue(fairTestJob("http", 2)); err != nil {
		t.Fatal("healthy flow denied admission", err)
	}
	j, _ := q.acquire(4)
	if j == nil || j.(*fairLease).flow.key != 2 {
		t.Fatal("healthy flow starved")
	}
	j.(*fairLease).complete(jobs.Result{})
	for _, l := range held {
		l.complete(jobs.Result{})
	}
}

func TestPulseFlowCannotOccupyReservedDriverAdmission(t *testing.T) {
	q, _ := NewFairQueue(64, "http", "tcp")
	if err := q.SetDemand([]DriverDemand{{Driver: "http", Monitors: 32, Occupancy: 1}, {Driver: "tcp", Monitors: 32, Occupancy: 1}}); err != nil {
		t.Fatal(err)
	}
	for range 16 {
		if err := q.Enqueue(fairTestJob("http", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Enqueue(fairTestJob("http", 1)); !errors.Is(err, ErrQueueFull) {
		t.Fatal("flow consumed its driver's entire admission budget", err)
	}
	var held []*fairLease
	for {
		j, _ := q.acquire(4)
		if j == nil {
			break
		}
		held = append(held, j.(*fairLease))
	}
	if err := q.Enqueue(fairTestJob("http", 2)); err != nil {
		t.Fatal("healthy HTTP denied its own driver's reserved admission", err)
	}
	j, _ := q.acquire(4)
	if j == nil || j.(*fairLease).flow.key != 2 {
		t.Fatal("healthy HTTP starved behind reserved driver budget")
	}
	j.(*fairLease).complete(jobs.Result{})
	for _, l := range held {
		l.complete(jobs.Result{})
	}
}

func TestDemandChangePreservesLeasesAndGlobalBound(t *testing.T) {
	q, _ := NewFairQueue(64, "http", "tcp")
	_ = q.SetDemand([]DriverDemand{{Driver: "http", Monitors: 10, Occupancy: 10}})
	for i := uint64(0); i < 8; i++ {
		_ = q.Enqueue(fairTestJob("http", i))
	}
	var held []*fairLease
	for {
		j, _ := q.acquire(8)
		if j == nil {
			break
		}
		held = append(held, j.(*fairLease))
	}
	_ = q.SetDemand([]DriverDemand{{Driver: "tcp", Monitors: 1, Occupancy: .5}})
	_ = q.Enqueue(fairTestJob("tcp", 1))
	if j, _ := q.acquire(2); j != nil {
		t.Fatal("shrink admitted above live lease limit")
	}
	for _, l := range held {
		l.complete(jobs.Result{})
	}
	j, _ := q.acquire(2)
	if j == nil {
		t.Fatal("new demand failed to resume after drain")
	}
	j.(*fairLease).complete(jobs.Result{})
}
