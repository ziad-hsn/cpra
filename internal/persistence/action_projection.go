package persistence

import (
	"context"
	"reflect"
	"time"
)

const actionProjectionVersion = 1
const activeReviewDeliveries = 64

// ActionProjection contains only operational summaries. Retained provider facts
// remain in the ledger; health checks never hydrate that history.
type ActionProjection struct {
	// Existing pre-cutover active blue work may exceed the new admission cap.
	// It keeps its identities and drains without admitting more work above 64.
	PreviousReviewCapacity int                `json:"previous_review_capacity,omitempty"`
	Notifications          NotificationCounts `json:"notifications"`
	Version                int                `json:"version"`
	ReviewConditions       int                `json:"review_conditions,omitempty"`
	Unknown                int                `json:"unknown"`
	Held                   int                `json:"held"`
	RecoveryHeld           int                `json:"recovery_held"`
	LatestRecovery         *Action            `json:"latest_recovery,omitempty"`
	LastFailure            time.Time          `json:"last_failure,omitzero"`
	RejectedAt             time.Time          `json:"rejected_at,omitzero"`
}

func (p *ActionProjection) clone() *ActionProjection {
	if p == nil {
		return nil
	}
	v := *p
	if p.LatestRecovery != nil {
		a := p.LatestRecovery.Clone()
		v.LatestRecovery = &a
	}
	return &v
}
func (m Monitor) UnknownActionCount() int {
	if m.ActionProjection != nil {
		return m.ActionProjection.Unknown
	}
	return m.unknownActions
}
func (m Monitor) HeldActionCount() int {
	if m.ActionProjection != nil {
		return m.ActionProjection.Held
	}
	return m.heldActions
}

func (f *machine) projectActions(previous Monitor, m *Monitor) {
	if !f.projectionsEnabled() || f.image.ActionStorage == nil || !actionStorageFormat(m.ActionVersion) {
		return
	}
	p := previous.ActionProjection.clone()
	initial := p == nil
	if initial {
		p = &ActionProjection{Version: actionProjectionVersion}
		if m.Notifications != nil {
			p.ReviewConditions = len(m.Notifications.ReviewAlerts)
		}
	}
	if m.ActionProjection != nil {
		p.ReviewConditions = max(p.ReviewConditions, m.ActionProjection.ReviewConditions)
	}
	sameOwner := previous.CatalogUID == m.CatalogUID && previous.FileIncarnation == m.FileIncarnation ||
		previous.CatalogUID == "" && m.CatalogUID != "" && m.AdoptedFileIncarnation != nil && *m.AdoptedFileIncarnation == previous.FileIncarnation
	if !sameOwner {
		p.RecoveryHeld = 0
		p.LastFailure = time.Time{}
		p.Notifications = NotificationCounts{}
	}
	if !sameOwner || previous.IncidentID != m.IncidentID {
		p.LatestRecovery = nil
		p.RejectedAt = time.Time{}
	}
	recoveryChanged := false
	seen := make(map[string]bool)
	for _, set := range []map[string]Action{m.retainedActions, m.Actions} {
		for id, a := range set {
			if seen[id] {
				continue
			}
			seen[id] = true
			old, exists := previous.Actions[id]
			if !initial && (!exists) {
				var err error
				if f.collections != nil {
					pin, e := f.collections.pinActions()
					if e != nil {
						f.err = e
						return
					}
					row, found, e := pin.Get(context.Background(), id)
					pin.Close()
					old, exists, err = row.Action, found, e
				}
				if err != nil {
					f.err = err
					return
				}
			}
			changed := !exists || !reflect.DeepEqual(old, a)
			if changed && a.Kind == "intervention" {
				recoveryChanged = true
			}
			if initial || changed {
				if !initial && exists {
					if sameOwner && previous.OwnsAction(old) && old.Kind == "code" {
						p.Notifications.adjust(old.State, -1)
					}
					if old.State == Unknown {
						p.Unknown--
					}
					if old.Held() {
						p.Held--
						if sameOwner && previous.OwnsAction(old) && old.Kind == "intervention" {
							p.RecoveryHeld--
						}
					}
				}
				if m.OwnsAction(a) && a.Kind == "code" {
					p.Notifications.adjust(a.State, 1)
				}
				if a.State == Unknown {
					p.Unknown++
				}
				if a.Held() {
					p.Held++
					if m.OwnsAction(a) && a.Kind == "intervention" {
						p.RecoveryHeld++
					}
				}
			} else if !sameOwner && m.OwnsAction(a) && a.Kind == "intervention" && a.Held() {
				p.RecoveryHeld++
			}
			if a.Kind == "intervention" && m.OwnsAction(a) {
				if a.State == Failed && a.FinishedAt.After(p.LastFailure) {
					p.LastFailure = a.FinishedAt
				}
				if a.IncidentID == "" || a.IncidentID == m.IncidentID {
					old := p.LatestRecovery
					if old == nil || a.ID == old.ID || a.Attempt > old.Attempt || a.Attempt == old.Attempt && (a.NotBefore.After(old.NotBefore) || a.NotBefore.Equal(old.NotBefore) && a.ID > old.ID) {
						v := a.Clone()
						p.LatestRecovery = &v
					}
					if a.IncidentID == m.IncidentID && a.State == Unknown && !a.Held() && a.Review != nil && a.Review.Resolution == "rejected" {
						for _, at := range []time.Time{a.Review.At, a.FinishedAt, a.ExecutorFinishedAt} {
							if at.After(p.RejectedAt) {
								p.RejectedAt = at
							}
						}
					}
				}
			}
		}
	}
	if !initial && recoveryChanged {
		at, err := f.currentRejectedAt(*m)
		if err != nil {
			f.err = err
			return
		}
		p.RejectedAt = at
	}
	blue := 0
	for _, a := range m.Actions {
		if a.ReviewNotice != nil && (a.State == Queued || a.State == Started) {
			blue++
		}
	}
	if p.PreviousReviewCapacity > 0 {
		if blue <= activeReviewDeliveries {
			p.PreviousReviewCapacity = 0
		} else {
			p.PreviousReviewCapacity = min(p.PreviousReviewCapacity, blue)
		}
	}
	m.ActionProjection = p
	m.unknownActions, m.heldActions = p.Unknown, p.Held
	// The latest recovery is a single bounded policy dependency. Everything else
	// that cannot execute retires, including unresolved evidence (never deleted).
	for id, a := range m.Actions {
		if a.State != Queued && a.State != Started {
			delete(m.Actions, id)
		}
	}
	if m.Notifications != nil {
		m.Notifications.ReviewAlerts = nil
	}
}

func (m Monitor) RetainedNotifications() NotificationCounts {
	if m.ActionProjection != nil {
		return m.ActionProjection.Notifications
	}
	var counts NotificationCounts
	for _, a := range m.Actions {
		if a.Kind == "code" && m.OwnsAction(a) {
			counts.adjust(a.State, 1)
		}
	}
	return counts
}
func (m Monitor) RecoveryHeld() bool {
	if m.ActionProjection != nil {
		return m.ActionProjection.RecoveryHeld > 0
	}
	return m.unresolvedIntervention()
}

func (p *ActionProjection) include(m Monitor, a Action) {
	if a.State == Unknown {
		p.Unknown++
	}
	if a.Held() {
		p.Held++
		if m.OwnsAction(a) && a.Kind == "intervention" {
			p.RecoveryHeld++
		}
	}
	if !m.OwnsAction(a) {
		return
	}
	if a.Kind == "code" {
		p.Notifications.adjust(a.State, 1)
		return
	}
	if a.Kind != "intervention" {
		return
	}
	if a.State == Failed && a.FinishedAt.After(p.LastFailure) {
		p.LastFailure = a.FinishedAt
	}
	if a.IncidentID != "" && a.IncidentID != m.IncidentID {
		return
	}
	latest := p.LatestRecovery
	if latest == nil || a.Attempt > latest.Attempt || a.Attempt == latest.Attempt && (a.NotBefore.After(latest.NotBefore) || a.NotBefore.Equal(latest.NotBefore) && a.ID > latest.ID) {
		copy := a.Clone()
		p.LatestRecovery = &copy
	}
	if a.IncidentID == m.IncidentID && a.State == Unknown && !a.Held() && a.Review != nil && a.Review.Resolution == "rejected" {
		for _, at := range []time.Time{a.Review.At, a.FinishedAt, a.ExecutorFinishedAt} {
			if at.After(p.RejectedAt) {
				p.RejectedAt = at
			}
		}
	}
}
