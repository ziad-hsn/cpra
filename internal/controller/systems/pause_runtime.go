package systems

import (
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

// projectPause accounts owner-observed exposure independently of SLO check
// obligations. No paused fleet is scanned or scheduled just to count intervals.
// Call with no active Ark query, and reacquire component pointers afterward.
func (s *DurableSystem) projectPause(ent ecs.Entity, m persistence.Monitor, at time.Time) {
	driver := s.configs.Get(ent).Type
	paused := !m.Removed && (!m.Policy.Enabled || !m.SnoozedUntil.IsZero())
	current := s.pauses.Get(ent)
	if current != nil && (!paused || current.Driver != driver) {
		s.removePause(ent, at)
		current = nil
	}
	if paused && current == nil {
		s.pauses.Add(ent, &components.CheckPause{Driver: driver})
		s.store.SLO().ChangePaused(driver, 1, at)
	}
}

func (s *DurableSystem) removePause(ent ecs.Entity, at time.Time) {
	if current := s.pauses.Get(ent); current != nil {
		driver := current.Driver
		s.pauses.Remove(ent)
		s.store.SLO().ChangePaused(driver, -1, at)
	}
}
