package systems

import (
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

func TestReadyQueueIndexedCancelAndFIFO(t *testing.T) {
	w := ecs.NewWorld()
	a, b, c, d := w.NewEntity(), w.NewEntity(), w.NewEntity(), w.NewEntity()
	q := ReadyQueue{}
	q.Enqueue([]ecs.Entity{a, b, c, b, d})
	if q.Len() != 4 {
		t.Fatal("duplicate ready entries")
	}
	if !q.Cancel(b) || q.Cancel(b) {
		t.Fatal("cancel not idempotent")
	}
	first := q.Consume(2)
	if len(first) != 2 || first[0] != a || first[1] != c {
		t.Fatal("FIFO changed", first)
	}
	q.Enqueue([]ecs.Entity{b})
	last := q.Consume(10)
	if len(last) != 2 || last[0] != d || last[1] != b || q.Len() != 0 {
		t.Fatal("slot reuse reordered queue", last)
	}
	for n := 0; n < 10000; n++ {
		q.Enqueue([]ecs.Entity{a})
		q.Cancel(a)
	}
	if len(q.items) != 4 || q.Len() != 0 {
		t.Fatal("repeated edits grew retained ready slots", len(q.items))
	}
	if got := q.Consume(-1); len(got) != 0 {
		t.Fatal("negative consume returned entries")
	}
}

func TestPulseSchedulerCancelBothIndexesAndIncarnation(t *testing.T) {
	w := ecs.NewWorld()
	entity := w.NewEntity()
	p := NewPulseScheduler()
	p.Schedule(entity, time.Now().Add(time.Hour))
	p.EnqueueReady([]ecs.Entity{entity})
	if !p.Cancel(entity) || p.Len() != 0 || p.ReadyLen() != 0 {
		t.Fatal("cancel left indexed work")
	}
	w.RemoveEntity(entity)
	replacement := w.NewEntity()
	p.Schedule(replacement, time.Now().Add(2*time.Hour))
	p.EnqueueReady([]ecs.Entity{replacement})
	if p.Cancel(entity) || p.Len() != 1 || p.ReadyLen() != 1 {
		t.Fatal("stale incarnation canceled replacement")
	}
}
