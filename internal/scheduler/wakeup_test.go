package scheduler

import (
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

func TestNextWakeAgainstDeadlineOracle(t *testing.T) {
	s := New()
	// Cross both a bitmap-word boundary and the wheel wrap in this fixture.
	s.wheel.tick = wheelNumSlots - 70
	base := time.Unix(0, s.wheel.tick*wheelResolution)
	entities := newTestEntities(256)
	deadlines := map[ecs.Entity]time.Time{}
	random := rand.New(rand.NewPCG(31, 47))
	for step := 0; step < 1000; step++ {
		entity := entities[random.IntN(len(entities))]
		if random.IntN(4) == 0 {
			s.Cancel(entity)
			delete(deadlines, entity)
		} else {
			at := base.Add(time.Duration(random.IntN(20_000))*time.Millisecond + 123*time.Nanosecond)
			s.Schedule(entity, at)
			deadlines[entity] = at
		}
		var earliest time.Time
		for _, at := range deadlines {
			if earliest.IsZero() || at.Before(earliest) {
				earliest = at
			}
		}
		wake, found := s.NextWake()
		if found != (len(deadlines) != 0) {
			t.Fatalf("step %d: wake availability disagrees with oracle", step)
		}
		if found && (wake.After(earliest) || earliest.Sub(wake) >= time.Duration(wheelResolution)) {
			t.Fatalf("step %d: wake %v outside earliest bucket for %v", step, wake, earliest)
		}
	}
	// A large clock jump drains every bucket and overflow exactly once.
	if due := s.Due(base.Add(time.Hour)); len(due) != len(deadlines) {
		t.Fatalf("clock jump lost scheduled entries: got %d, want %d", len(due), len(deadlines))
	}
	if _, found := s.NextWake(); found {
		t.Fatal("drained scheduler retained an occupied bucket")
	}
	s.Schedule(entities[0], base.Add(2*time.Hour))
	s.Cancel(entities[0])
	if _, found := s.NextWake(); found {
		t.Fatal("last cancellation retained a deadline")
	}
}

func TestNextWakePartialWordAndOverdueReplacement(t *testing.T) {
	s := New()
	s.wheel.tick = 63
	base := time.Unix(0, 63*wheelResolution)
	entities := newTestEntities(3)
	s.Schedule(entities[0], base.Add(time.Duration(3*wheelResolution)))
	s.Schedule(entities[1], base.Add(time.Duration((wheelNumSlots-2)*wheelResolution)))
	s.Schedule(entities[2], base.Add(time.Hour))
	wake, found := s.NextWake()
	if !found || !wake.Equal(base.Add(time.Duration(3*wheelResolution))) {
		t.Fatal("word boundary skipped the first bucket", wake)
	}
	s.Schedule(entities[0], base.Add(-time.Second))
	wake, found = s.NextWake()
	if !found || !wake.Equal(base) {
		t.Fatal("overdue replacement must wake at the current bucket", wake)
	}
	s.Cancel(entities[0])
	wake, found = s.NextWake()
	if !found || !wake.Equal(base.Add(time.Duration((wheelNumSlots-2)*wheelResolution))) {
		t.Fatal("wrapped partial word selected a stale bit", wake)
	}
}

func BenchmarkSchedulerNextWake(b *testing.B) {
	for _, count := range []int{0, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			s := New()
			entities := newTestEntities(count)
			base := time.Now()
			for i, entity := range entities {
				s.Schedule(entity, base.Add(time.Duration(i%60_000)*time.Millisecond))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.NextWake()
			}
		})
	}
}
