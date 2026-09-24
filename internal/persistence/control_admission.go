package persistence

import (
	"context"
	"time"
)

// CheckCheckAdmission performs a bounded dependency and control check immediately
// before a health worker invokes its job. It is not an external-action start grant.
// The exact dispatch revision fences pre-snooze work even after an unsnooze.
func (s *Store) CheckCheckAdmission(id string, g *CatalogGuard, revision string, at time.Time) error {
	return s.CheckCheckAdmissionContext(context.Background(), id, g, revision, at)
}

// CheckCheckAdmissionContext bounds admission lock acquisition by ctx.
func (s *Store) CheckCheckAdmissionContext(ctx context.Context, id string, g *CatalogGuard, revision string, at time.Time) error {
	if at.IsZero() {
		return ErrControlInvalid
	}
	if g != nil {
		if err := g.validate(id); err != nil {
			return err
		}
		if g.Removed {
			return ErrCatalogDependency
		}
	}
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return err
	}
	defer unlock()
	f := s.fsm
	if err := f.checkCatalogGuard(id, g); err != nil {
		return err
	}
	m, ok := f.image.Monitors[id]
	if !ok || m.Removed || !m.Policy.Enabled || m.snoozed(at) || m.ControlRevision != revision {
		return ErrControlConflict
	}
	if g != nil && m.CatalogUID != g.monitorUID(id) {
		return ErrCatalogDependency
	}
	return ctx.Err()
}
