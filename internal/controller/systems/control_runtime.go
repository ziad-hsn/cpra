package systems

import (
	"container/heap"
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

// Expiry is an indexed, bounded heap: one deadline per full entity identity.
// A simultaneous fleet pause does not create an unbounded expiry pass.
type controlDeadline struct {
	entity ecs.Entity
	at     time.Time
}
type controlDeadlines struct {
	entries   []controlDeadline
	positions map[ecs.Entity]int
}

func newControlDeadlines() *controlDeadlines {
	return &controlDeadlines{positions: map[ecs.Entity]int{}}
}
func (h controlDeadlines) Len() int           { return len(h.entries) }
func (h controlDeadlines) Less(i, j int) bool { return h.entries[i].at.Before(h.entries[j].at) }
func (h controlDeadlines) Swap(i, j int) {
	h.entries[i], h.entries[j] = h.entries[j], h.entries[i]
	h.positions[h.entries[i].entity] = i
	h.positions[h.entries[j].entity] = j
}
func (h *controlDeadlines) Push(value any) {
	d := value.(controlDeadline)
	h.positions[d.entity] = len(h.entries)
	h.entries = append(h.entries, d)
}
func (h *controlDeadlines) Pop() any {
	i := len(h.entries) - 1
	d := h.entries[i]
	h.entries[i] = controlDeadline{}
	h.entries = h.entries[:i]
	delete(h.positions, d.entity)
	return d
}
func (h *controlDeadlines) Schedule(e ecs.Entity, at time.Time) {
	if i, ok := h.positions[e]; ok {
		h.entries[i].at = at
		heap.Fix(h, i)
	} else {
		heap.Push(h, controlDeadline{e, at})
	}
}
func (h *controlDeadlines) Cancel(e ecs.Entity) {
	if i, ok := h.positions[e]; ok {
		heap.Remove(h, i)
	}
}
func (h *controlDeadlines) Due(now time.Time, limit int) []ecs.Entity {
	var due []ecs.Entity
	for len(due) < limit && h.Len() > 0 && !h.entries[0].at.After(now) {
		due = append(due, heap.Pop(h).(controlDeadline).entity)
	}
	return due
}

func (s *DurableSystem) isSnoozed(ent ecs.Entity) bool {
	c := s.controls.Get(ent)
	return c != nil && !c.SnoozedUntil.IsZero()
}

// projectControls runs on the owner with no open Ark query. Reacquire every
// component pointer after adding the always-present component.
func (s *DurableSystem) projectControls(ent ecs.Entity, m persistence.Monitor) {
	old := s.controls.Get(ent)
	changed := old != nil && old.Revision != m.ControlRevision
	if old == nil {
		s.controls.Add(ent, &components.ControlState{})
	}
	*s.controls.Get(ent) = components.ControlState{Revision: m.ControlRevision, SnoozedUntil: m.SnoozedUntil, IncidentID: m.IncidentID, IncidentRevision: m.IncidentRevision, NotificationsDismissed: m.Dismissed}
	if changed {
		s.scheduler.Cancel(ent)
		s.resetCheckSchedule(ent)
		// Retain pending invocation identity until its result arrives. Unsnooze
		// must not allow a second check while the first one is still running.
		if m.Policy.Enabled && !m.Removed && m.SnoozedUntil.IsZero() {
			due := time.Now().Add(staggerPhase(ent.ID(), m.Policy.Interval))
			s.checks[ent.ID()].next = due.UnixNano()
			s.scheduler.Schedule(ent, due)
		}
	}
	if m.Removed || m.SnoozedUntil.IsZero() {
		s.snoozes.Cancel(ent)
	} else {
		s.snoozes.Schedule(ent, m.SnoozedUntil)
	}
	if !m.SnoozedUntil.IsZero() || !m.Policy.Enabled || m.Removed {
		s.scheduler.Cancel(ent)
		s.resetCheckSchedule(ent)
		s.actions.Cancel(ent)
		delete(s.actionDue, ent)
	}
	interval := time.Duration(0)
	if m.Policy.Enabled && !m.Removed && m.SnoozedUntil.IsZero() {
		interval = m.Policy.Interval
	}
	s.setPulseDemand(ent, interval)
	s.projectPause(ent, m, time.Now())
}

func (s *DurableSystem) projectControlChange(ctx context.Context, change persistence.ControlChange) error {
	ent, ok := s.entities[change.MonitorID]
	if !ok || !s.world.Alive(ent) {
		return persistence.ErrControlConflict
	}
	m, ok, err := s.store.GetContext(ctx, change.MonitorID)
	if err != nil {
		return err
	}
	if !ok || m.Removed || m.CatalogUID != change.MonitorUID || s.states.Get(ent).Revision != m.Revision {
		return persistence.ErrControlConflict
	}
	if p, managed := s.managed[m.ID]; managed && p.uid != m.CatalogUID {
		return persistence.ErrControlConflict
	}
	s.project(ent, m)
	s.scheduleActions(ent, m)
	if s.index != nil {
		s.index.Put(s.summary(ent, m))
	}
	return nil
}

func (s *DurableSystem) receiveControlProjection() {
	if s.stopping.Load() {
		return
	}
	const limit = 100
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	project := func(rows []persistence.ControlChange) bool {
		for _, change := range rows {
			if err := s.projectControlChange(ctx, change); err != nil && !errors.Is(err, persistence.ErrControlConflict) {
				s.controlsPending = true
				s.recoveryError(err, ctx)
				return false
			}
		}
		return true
	}
	if s.controlView != nil {
		rows, next, err := s.controlView.Page(s.controlAfter, limit)
		if err != nil {
			s.controlsPending = true
			s.fail(err)
			return
		}
		if !project(rows) {
			return
		}
		s.controlAfter = next
		if next == "" {
			s.controlCursor = s.controlView.Cursor
			s.controlView = nil
		}
		s.controlsPending = next != ""
		return
	}
	changes, err := s.store.ControlsChangedSinceContext(ctx, s.controlCursor, limit)
	if errors.Is(err, persistence.ErrControlChangeGap) {
		s.controlsPending = true
		view, err := s.store.ControlSnapshotContext(ctx)
		if err != nil {
			s.recoveryError(err, ctx)
			return
		}
		s.controlView = &view
		s.controlAfter = ""
		return
	}
	if err != nil {
		s.controlsPending = true
		s.recoveryError(err, ctx)
		return
	}
	if !project(changes.Changes) {
		return
	}
	s.controlCursor = changes.Next
	s.controlsPending = changes.More
}

func (s *DurableSystem) expireSnoozes(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	commands := make([]persistence.Command, 0, min(100, s.config.Storage.BatchSize))
	entities := make([]ecs.Entity, 0, cap(commands))
	due := s.snoozes.Due(now, cap(commands))
	for _, ent := range due {
		if !s.world.Alive(ent) {
			continue
		}
		state, control := s.states.Get(ent), s.controls.Get(ent)
		if state == nil || control == nil || control.SnoozedUntil.IsZero() {
			continue
		}
		m, ok, err := s.store.GetContext(ctx, state.MonitorID)
		if err != nil {
			// Nothing in this batch has been submitted. Restore every still-live
			// deadline before retaining admission for a later bounded pass.
			for _, expired := range due {
				if s.world.Alive(expired) {
					if c := s.controls.Get(expired); c != nil && !c.SnoozedUntil.IsZero() {
						s.snoozes.Schedule(expired, c.SnoozedUntil)
					}
				}
			}
			s.recoveryError(err, ctx)
			return
		}
		if !ok || m.Removed {
			continue
		}
		if m.ControlRevision != control.Revision || !m.SnoozedUntil.Equal(control.SnoozedUntil) {
			s.projectControls(ent, m)
			continue
		}
		revision := uuid.NewString()
		commands = append(commands, persistence.Command{Kind: "control", MonitorID: m.ID, At: now, Control: &persistence.ControlCommand{Action: "expire_snooze", MonitorUID: m.CatalogUID, ExpectedRevision: m.ControlRevision, Revision: revision, OperationID: revision, Actor: "system", Until: m.SnoozedUntil}})
		entities = append(entities, ent)
	}
	if len(commands) == 0 {
		return
	}
	// Popped deadlines and original CAS/operation IDs remain owned until their
	// outcomes are confirmed; a leadership change cannot allocate new identities.
	s.beginOwnerWrite(&ownerWrite{kind: expiryWrite, commands: commands, expiries: entities})
}

func (s *DurableSystem) finishExpiryWrite(ctx context.Context, p *ownerWrite) error {
	// A successful expiry always returns its committed control projection.
	// Validate the whole response before consuming any popped deadline. An
	// empty or unrelated reply is not evidence that the snooze expired.
	for i, result := range p.results {
		command := p.commands[i]
		if result.Err != nil {
			if result.Monitor != nil {
				return errors.New("failed expiry returned a monitor projection")
			}
			if !errors.Is(result.Err, persistence.ErrControlConflict) && !errors.Is(result.Err, persistence.ErrCatalogBusy) {
				return result.Err
			}
			continue
		}
		m := result.Monitor
		if m == nil || m.ID != command.MonitorID || m.CatalogUID != command.Control.MonitorUID || m.ControlRevision != command.Control.Revision || !m.SnoozedUntil.IsZero() || m.Removed {
			return persistence.ErrCommitUnconfirmed
		}
	}
	for p.completed < len(p.results) {
		i := p.completed
		result := p.results[i]
		ent, command := p.expiries[i], p.commands[i]
		if !s.world.Alive(ent) {
			p.completed++
			continue
		}
		if result.Err != nil {
			if errors.Is(result.Err, persistence.ErrControlConflict) || errors.Is(result.Err, persistence.ErrCatalogBusy) {
				// A duplicate expiry may conflict after committing. Adopt current
				// controls for the original incarnation; a newer operator edit wins.
				m, ok, err := s.store.GetContext(ctx, command.MonitorID)
				if err != nil {
					return err
				}
				if ok && !m.Removed && m.CatalogUID == command.Control.MonitorUID && m.Revision == s.states.Get(ent).Revision {
					s.project(ent, m)
					s.scheduleActions(ent, m)
					if s.index != nil {
						s.index.Put(s.summary(ent, m))
					}
					if m.ControlRevision == command.Control.ExpectedRevision && !m.SnoozedUntil.IsZero() {
						s.snoozes.Schedule(ent, time.Now().Add(time.Second))
					}
				}
				p.completed++
				continue
			}
			return result.Err
		}
		if result.Monitor != nil {
			m := *result.Monitor
			s.project(ent, m)
			s.scheduleActions(ent, m)
			if s.index != nil {
				s.index.Put(s.summary(ent, m))
			}
		}
		p.completed++
	}
	return nil
}

func (s *DurableSystem) applyControlReceipt(ctx context.Context, receipt persistence.OperationReceipt) error {
	if receipt.Subject == "review" {
		// A review belongs to the original invocation even after its monitor
		// is removed or recreated. Observing this receipt never schedules work.
		action, ok, err := s.store.ActionContext(ctx, receipt.ActionID)
		if err != nil {
			return err
		}
		if !ok || action.MonitorID != receipt.Key.ID || action.CatalogUID != receipt.UID || action.Review == nil || action.Review.Revision != receipt.NewVersion || action.Review.Conflict {
			return persistence.ErrControlConflict
		}
		// Refresh the current entity only if this is still its incarnation.
		// Old reviews can complete without a live replacement entity.
		current, ok, err := s.store.GetContext(ctx, receipt.Key.ID)
		if err != nil {
			return err
		}
		if ok && !current.Removed && current.CatalogUID == receipt.UID {
			if err := s.projectControlChange(ctx, persistence.ControlChange{MonitorID: receipt.Key.ID, MonitorUID: receipt.UID}); err != nil {
				return err
			}
		}
		return nil
	}
	err := s.projectControlChange(ctx, persistence.ControlChange{MonitorID: receipt.Key.ID, MonitorUID: receipt.UID})
	if err != nil {
		return err
	}
	control := s.controls.Get(s.entities[receipt.Key.ID])
	if receipt.Subject == "control" && control.Revision == receipt.NewVersion {
		return nil
	}
	if receipt.Subject == "incident" && control.IncidentID == receipt.IncidentID && control.IncidentRevision == receipt.NewVersion {
		return nil
	}
	if receipt.Subject == "recovery" {
		action, ok, err := s.store.ActionContext(ctx, receipt.ActionID)
		if err != nil {
			return err
		}
		if ok && action.MonitorID == receipt.Key.ID && action.CatalogUID == receipt.UID && action.OperationID == receipt.ID && action.State != persistence.Cancelled {
			return nil
		}
	}
	return persistence.ErrControlConflict
}
