package scheduler

import (
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

func newTestEntities(n int) []ecs.Entity {
	w := ecs.NewWorld()
	ents := make([]ecs.Entity, n)
	for i := range ents {
		ents[i] = w.NewEntity()
	}
	return ents
}

func TestSchedulerDueOrdering(t *testing.T) {
	s := New()
	base := time.Now()
	e := newTestEntities(3)

	// Insert out of order; Due must return them in deadline order.
	s.Schedule(e[2], base.Add(30*time.Millisecond))
	s.Schedule(e[0], base.Add(10*time.Millisecond))
	s.Schedule(e[1], base.Add(20*time.Millisecond))

	got := s.Due(base.Add(35 * time.Millisecond))
	want := []ecs.Entity{e[0], e[1], e[2]}
	if len(got) != len(want) {
		t.Fatalf("Due returned %d entities, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Due[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
	if s.Len() != 0 {
		t.Fatalf("Len after draining = %d, want 0", s.Len())
	}
}

func TestSchedulerDueOnlyPast(t *testing.T) {
	s := New()
	base := time.Now()
	e := newTestEntities(2)

	s.Schedule(e[0], base.Add(10*time.Millisecond))
	s.Schedule(e[1], base.Add(100*time.Millisecond))

	// Only e[0] is due at base+50ms.
	got := s.Due(base.Add(50 * time.Millisecond))
	if len(got) != 1 || got[0] != e[0] {
		t.Fatalf("Due = %v, want [%v]", got, e[0])
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (e[1] still pending)", s.Len())
	}
}

func TestSchedulerEmpty(t *testing.T) {
	s := New()
	if got := s.Due(time.Now()); len(got) != 0 {
		t.Fatalf("Due on empty scheduler = %v, want empty", got)
	}
	if _, ok := s.Peek(); ok {
		t.Fatal("Peek on empty scheduler returned ok=true")
	}
}

func TestSchedulerPeek(t *testing.T) {
	s := New()
	base := time.Now()
	e := newTestEntities(2)
	s.Schedule(e[0], base.Add(20*time.Millisecond))
	s.Schedule(e[1], base.Add(10*time.Millisecond))

	earliest, ok := s.Peek()
	if !ok {
		t.Fatal("Peek returned ok=false")
	}
	if !earliest.Equal(base.Add(10 * time.Millisecond)) {
		t.Fatalf("Peek = %v, want %v", earliest, base.Add(10*time.Millisecond))
	}
}

func TestSchedulerReschedule(t *testing.T) {
	s := New()
	base := time.Now()
	e := newTestEntities(1)

	// A monitor checked at t=0 is re-scheduled at t=1s.
	s.Schedule(e[0], base)
	due := s.Due(base)
	if len(due) != 1 || due[0] != e[0] {
		t.Fatalf("first Due = %v, want [%v]", due, e[0])
	}
	s.Schedule(e[0], base.Add(time.Second))

	// Not due again until t=1s.
	if got := s.Due(base.Add(500 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("Due at +500ms = %v, want empty", got)
	}
	due = s.Due(base.Add(time.Second))
	if len(due) != 1 || due[0] != e[0] {
		t.Fatalf("second Due = %v, want [%v]", due, e[0])
	}
}
