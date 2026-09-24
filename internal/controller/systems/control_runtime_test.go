package systems

import (
	"github.com/mlange-42/ark/ecs"
	"testing"
	"time"
)

func TestControlDeadlinesBoundExpiryAndReuseEntityIdentity(t *testing.T) {
	w := ecs.NewWorld()
	h := newControlDeadlines()
	now := time.Now()
	entities := make([]ecs.Entity, 200)
	for i := range entities {
		entities[i] = w.NewEntity()
		h.Schedule(entities[i], now)
	}
	for n := 0; n < 1000; n++ {
		h.Schedule(entities[0], now.Add(time.Hour))
	}
	if h.Len() != 200 {
		t.Fatal("rescheduling grew expiry index")
	}
	first := h.Due(now, 25)
	if len(first) != 25 || h.Len() != 175 {
		t.Fatal("expiry exceeded bounded pass")
	}
	old := entities[0]
	h.Cancel(old)
	w.RemoveEntity(old)
	replacement := w.NewEntity()
	h.Schedule(replacement, now)
	h.Cancel(old)
	if _, ok := h.positions[replacement]; !ok {
		t.Fatal("old incarnation cancelled replacement deadline")
	}
	for h.Len() > 0 {
		if len(h.Due(now, 25)) == 0 {
			t.Fatal("due deadlines were lost")
		}
	}
	if len(h.positions) != 0 {
		t.Fatal("expiry retained entity index")
	}
}
