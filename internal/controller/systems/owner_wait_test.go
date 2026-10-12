package systems

import (
	"errors"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestOwnerWaitIncludesSchedulingAndOperationalDeadlines(t *testing.T) {
	now := time.Now()
	world := ecs.NewWorld()
	entity := world.NewEntity()
	for _, tc := range []struct {
		name             string
		setup            func(*DurableSystem)
		minimum, maximum time.Duration
	}{
		{"idle", func(*DurableSystem) {}, time.Second, time.Second},
		{"check", func(s *DurableSystem) { s.scheduler.Schedule(entity, now.Add(40*time.Millisecond)) }, 38 * time.Millisecond, 40 * time.Millisecond},
		{"action", func(s *DurableSystem) { s.actions.Schedule(entity, now.Add(30*time.Millisecond)) }, 28 * time.Millisecond, 30 * time.Millisecond},
		{"snooze", func(s *DurableSystem) { s.snoozes.Schedule(entity, now.Add(25*time.Millisecond)) }, 25 * time.Millisecond, 25 * time.Millisecond},
		{"slo", func(s *DurableSystem) { s.lastSLO = now.Add(-s.config.SLO.EvaluationInterval + 35*time.Millisecond) }, 35 * time.Millisecond, 35 * time.Millisecond},
		{"ready", func(s *DurableSystem) { s.scheduler.EnqueueReady([]ecs.Entity{entity}) }, 5 * time.Millisecond, 5 * time.Millisecond},
		{"pending write", func(s *DurableSystem) { s.pendingWrite = &ownerWrite{} }, 5 * time.Millisecond, 5 * time.Millisecond},
		{"recovery", func(s *DurableSystem) { s.recovering.Store(true) }, 5 * time.Millisecond, 5 * time.Millisecond},
		{"control scan", func(s *DurableSystem) { s.controlsPending = true }, 5 * time.Millisecond, 5 * time.Millisecond},
		{"results", func(s *DurableSystem) { s.resultBacklog = []jobs.Result{{}} }, 5 * time.Millisecond, 5 * time.Millisecond},
		{"catalog handoff", func(s *DurableSystem) {
			s.catalogRuntime = &catalogRuntime{updates: make(chan catalogProjection, 1)}
			s.catalogRuntime.updates <- catalogProjection{}
		}, 5 * time.Millisecond, 5 * time.Millisecond},
		{"overdue", func(s *DurableSystem) { s.snoozes.Schedule(entity, now.Add(-time.Second)) }, 5 * time.Millisecond, 5 * time.Millisecond},
		{"permanent failure", func(s *DurableSystem) { s.lastError = errors.New("failed") }, time.Second, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &DurableSystem{scheduler: NewPulseScheduler(), actions: NewPulseScheduler(), snoozes: newControlDeadlines(), config: runtimeconfig.Default(), lastSLO: now}
			tc.setup(s)
			wait := s.NextUpdate(now, 5*time.Millisecond)
			if wait < tc.minimum || wait > tc.maximum {
				t.Fatalf("wait %v outside [%v,%v]", wait, tc.minimum, tc.maximum)
			}
		})
	}
}
