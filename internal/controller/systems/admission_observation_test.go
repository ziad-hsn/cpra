package systems

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type admissionQueue struct {
	queue.Queue
	rejected bool
	full     bool
	admitted []jobs.Job
}

func (q *admissionQueue) Stats() queue.Stats {
	depth := 0
	if q.full {
		depth = 1
	}
	return queue.Stats{Capacity: 1, QueueDepth: depth}
}
func (q *admissionQueue) Enqueue(j jobs.Job) error {
	if q.rejected {
		return queue.ErrQueueFull
	}
	q.admitted = append(q.admitted, j)
	return nil
}

func TestAdmissionRejectionRemainsVisibleAndInSLODenominator(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{true: "enqueue_rejected", false: "capacity_exhausted"}[rejected], func(t *testing.T) {
			cfg := runtimeconfig.Default()
			cfg.Storage.Mode = "memory"
			store, err := persistence.Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			world := ecs.NewWorld()
			ent := releaseMonitor(t, &world, 1, 1)
			q := &admissionQueue{rejected: rejected, full: !rejected}
			system := NewDurableSystem(&world, store, cfg, &fleetview.Holder{}, noopLogger{}, q, nil, nil, nil, time.Minute, false)
			if err := system.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			system.Initialize(&world)
			now := time.Now()
			system.scheduler.Cancel(ent)
			system.checks[ent.ID()].next = now.UnixNano()
			system.scheduler.Schedule(ent, now)
			system.dispatchChecks(now)
			row, _ := system.index.Get(ent.ID())
			if row.CheckAdmission != "awaiting_capacity" || row.Warning == "" || system.scheduler.ReadyLen() != 1 {
				t.Fatalf("missing admission state: %+v", row)
			}
			if rejected && row.QueueRejections != 1 {
				t.Fatalf("missing rejection count: %+v", row)
			}
			system.dispatchChecks(now.Add(2 * time.Second))
			row, _ = system.index.Get(ent.ID())
			if row.MissedChecks != 2 || len(q.admitted) != 0 {
				t.Fatalf("missing skipped cadence evidence: %+v", row)
			}
			view := store.SLO().View(now.Add(20*time.Second), time.Minute, 1)
			if len(view.Reports) != 1 || view.Reports[0].Expected != 3 || view.Reports[0].Missed != 2 || view.Reports[0].Overdue != 1 || view.Reports[0].Samples != 0 {
				t.Fatalf("lost SLO obligations: %+v", view)
			}
			q.rejected, q.full = false, false
			system.dispatchChecks(now.Add(2 * time.Second))
			row, _ = system.index.Get(ent.ID())
			if row.CheckAdmission != "pending" || row.Warning != "" || len(q.admitted) != 1 || system.scheduler.ReadyLen() != 0 {
				t.Fatalf("retained obligation did not recover: %+v", row)
			}
			job := q.admitted[0].(*jobs.Dispatch)
			if !job.Scheduled.Equal(now) {
				t.Fatalf("requeue reset scheduled obligation: %v want %v", job.Scheduled, now)
			}
			state := ecs.NewMap1[components.MonitorState](&world).Get(ent)
			if !state.IsPulsePending() {
				t.Fatal("accepted check not pending")
			}
		})
	}
}
