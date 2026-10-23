package persistence

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"time"
)

// A one-time, committed startup conversion reconstructs current resolution
// dependencies from retained starts. Old exposure flags do not bind a revision.
// The cursor freezes one monitor and action fence; ordinary admission remains
// closed until every bounded step has completed.
type notificationProjectionMigration struct {
	MonitorID string                                 `json:"monitor_id"`
	Fence     uint64                                 `json:"fence"`
	After     string                                 `json:"after"`
	At        time.Time                              `json:"at"`
	Opening   *notificationProjectionOpening         `json:"opening,omitempty"`
	Closures  map[string]notificationProjectionClose `json:"closures,omitempty"`
}

type notificationProjectionOpening struct {
	ActionID        string    `json:"action_id"`
	Episode         string    `json:"episode"`
	StartedAt       time.Time `json:"started_at"`
	ProviderVersion int       `json:"provider_version"`
}

type notificationProjectionClose struct {
	ActionID   string      `json:"action_id"`
	DeliveryID string      `json:"delivery_id"`
	Attempt    int         `json:"attempt"`
	State      ActionState `json:"state"`
	Reason     string      `json:"reason,omitempty"`
	QueuedAt   time.Time   `json:"queued_at"`
	Deadline   time.Time   `json:"deadline,omitzero"`
	Started    bool        `json:"started,omitempty"`
	Conflict   bool        `json:"conflict,omitempty"`
}

func (p *notificationProjectionMigration) clone() *notificationProjectionMigration {
	if p == nil {
		return nil
	}
	v := *p
	if p.Opening != nil {
		o := *p.Opening
		v.Opening = &o
	}
	v.Closures = maps.Clone(p.Closures)
	return &v
}

func needsNotificationProjection(m Monitor) bool {
	return m.NotificationVersion == DeliveryFormatVersion && !m.notificationConditionsEnabled()
}

func requiresProjectionMigration(i image) bool {
	if state := i.ActionProjectionState; state != nil && (!state.Ready || state.Notifications != nil) {
		return true
	}
	for _, m := range i.Monitors {
		if needsNotificationProjection(m) {
			return true
		}
	}
	return false
}

func (p *notificationProjectionMigration) include(m Monitor, a Action) {
	if a.Kind != "code" || a.Control != nil || a.ReviewNotice != nil || a.Revision != m.Revision ||
		a.CatalogUID != m.CatalogUID || a.FileIncarnation != m.FileIncarnation || a.NotificationCorrelation == "" ||
		a.NotificationCorrelation != m.NotificationCorrelation || a.Endpoint < 0 || a.Endpoint >= m.Policy.Endpoints[a.Color] ||
		a.EndpointID != notificationEndpointIdentity(m, a.Color, a.Endpoint) {
		return
	}
	if slices.Contains([]string{"red", "yellow", "cyan"}, a.Color) && !a.StartedAt.IsZero() {
		// The scan is ordered by action identity. Retain one actual start, never
		// the current clock or a guessed start inferred from an attempt ordinal.
		if p.Opening == nil {
			p.Opening = &notificationProjectionOpening{ActionID: a.ID, Episode: a.NotificationEpisode,
				StartedAt: a.StartedAt, ProviderVersion: a.ProviderCorrelationVersion}
		}
		return
	}
	if a.Color != "green" || a.DeliveryID == "" {
		return
	}
	if p.Closures == nil {
		p.Closures = make(map[string]notificationProjectionClose)
	}
	old, exists := p.Closures[a.EndpointID]
	value := notificationProjectionClose{ActionID: a.ID, DeliveryID: a.DeliveryID, Attempt: a.Attempt,
		State: a.State, QueuedAt: a.QueuedAt, Deadline: a.Deadline, Started: !a.StartedAt.IsZero(), Reason: a.TerminationReason}
	if a.State == Cancelled {
		value.Reason = a.Outcome
	}
	if exists {
		conflict := old.Conflict || old.DeliveryID != value.DeliveryID || old.Attempt == value.Attempt && old.ActionID != value.ActionID ||
			!old.Deadline.Equal(value.Deadline)
		started := old.Started || value.Started
		if old.Attempt >= value.Attempt {
			value = old
		}
		value.Conflict, value.Started = conflict, started
	}
	p.Closures[a.EndpointID] = value
}

func (p *notificationProjectionMigration) conditions(m Monitor) map[string]NotificationResolution {
	if p.Opening == nil && !m.greenEligible() && len(p.Closures) == 0 || m.NotificationCorrelation == "" {
		return nil
	}
	opened, openingID, providerVersion := m.IncidentOpenedAt, "", m.ProviderCorrelationVersion
	if p.Opening != nil {
		opened, openingID, providerVersion = p.Opening.StartedAt, p.Opening.ActionID, p.Opening.ProviderVersion
	} else if opened.IsZero() {
		opened = m.LastCheck
		if opened.IsZero() {
			opened = p.At
		}
	}
	// This temporary value only constructs summaries. It never changes an
	// action, generates provider evidence or invokes the normal admission path.
	v := m.Clone()
	v.Notifications = &NotificationState{Version: NotificationConditionFormatVersion}
	v.captureResolutionTargets(m.NotificationCorrelation, openingID, opened, providerVersion)
	for id, condition := range v.Notifications.Resolutions {
		if p.Opening == nil {
			condition.MigrationReason = "migration_start_unverified"
		}
		if !m.IncidentClosedAt.IsZero() && !m.IncidentClosedAt.Before(opened) {
			condition.RecoveredAt = m.IncidentClosedAt
		}
		if closing, ok := p.Closures[condition.EndpointID]; ok {
			condition.ActionID, condition.DeliveryID = closing.ActionID, closing.DeliveryID
			condition.State, condition.Reason = closing.State, closing.Reason
			condition.Started, condition.ExecutionDeadline = closing.Started, closing.Deadline
			if !closing.QueuedAt.IsZero() && !closing.QueuedAt.Before(opened) {
				condition.RecoveredAt = closing.QueuedAt
			}
			if closing.Conflict {
				condition.State = Unknown
				condition.MigrationReason = "migration_delivery_evidence_conflict"
			}
		}
		v.Notifications.Resolutions[id] = condition
	}
	return v.Notifications.Resolutions
}

func (f *machine) applyNotificationProjection(c Command) Result {
	state := f.image.ActionProjectionState
	if state == nil || !state.Ready || state.Current != nil || state.Revision != c.Generation {
		return Result{Err: errActionProjectionPending}
	}
	m, exists := f.image.Monitors[c.MonitorID]
	if !exists || !needsNotificationProjection(m) || f.image.ActionStorage == nil {
		return Result{Err: errActionProjectionPending}
	}
	next := state.clone()
	if next.Notifications == nil {
		next.Notifications = &notificationProjectionMigration{MonitorID: m.ID, Fence: f.image.ActionStorage.Sequence, At: c.At}
	}
	current := next.Notifications
	if current.MonitorID != m.ID || current.Fence != f.image.ActionStorage.Sequence {
		return Result{Err: errActionProjectionPending}
	}
	if current.Fence != 0 {
		if f.collections == nil {
			f.err = errActionLedger
			return Result{Err: f.err}
		}
		pin, err := f.collections.pinActions()
		if err != nil {
			f.err = err
			return Result{Err: err}
		}
		pin.sequence = current.Fence
		rows, after, err := pin.pageBounded(context.Background(), m.ID, current.After, 64, 64, 1<<20)
		pin.Close()
		if err != nil {
			f.err = err
			return Result{Err: err}
		}
		for _, row := range rows {
			current.include(m, row.Action)
		}
		current.After = after
	}
	result := Result{Allowed: true}
	if current.After == "" {
		updated := m.Clone()
		updated.upgradeLifecycle()
		// Formats 40/42 have delivery identities but not the action-policy
		// marker required by notification state. Preserve the existing upgrade:
		// only unsent records lacking their original budget are cancelled.
		updated.upgradeActions(current.At, func(kind string, a *Action) {
			result.Events = append(result.Events, Event{MonitorID: m.ID, Revision: a.Revision, At: current.At,
				Type: kind, CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, IncidentID: a.IncidentID,
				ActionID: a.ID, DeliveryID: a.DeliveryID, NotificationEpisode: a.NotificationEpisode, EndpointID: a.EndpointID,
				Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint, Outcome: a.Outcome, Cause: a.Cause, Failure: a.Failure})
		})
		updated.upgradeNotificationConditions()
		updated.Notifications.Resolutions = current.conditions(m)
		for _, event := range result.Events {
			if action, exists := updated.Actions[event.ActionID]; exists {
				updated.recordNotificationCondition(action)
			}
		}
		result.Events = append(result.Events, f.installMonitor(m, updated, c.At)...)
		result.Monitor = &updated
		next.Notifications = nil
	}
	next.Revision++
	f.image.ActionProjectionState = next
	f.image.Version = joinStorageFormat(f.image.Version, NotificationConditionFormatVersion)
	return result
}

func (s *Store) migrateNotificationConditions(ctx context.Context) error {
	s.fsm.mu.RLock()
	ids := make([]string, 0)
	for id, m := range s.fsm.image.Monitors {
		if needsNotificationProjection(m) {
			ids = append(ids, id)
		}
	}
	state := s.fsm.image.ActionProjectionState.clone()
	s.fsm.mu.RUnlock()
	slices.Sort(ids)
	if state != nil && state.Notifications != nil {
		for n, id := range ids {
			if id == state.Notifications.MonitorID {
				ids[0], ids[n] = ids[n], ids[0]
				break
			}
		}
	}
	for _, id := range ids {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			s.fsm.mu.RLock()
			pending := needsNotificationProjection(s.fsm.image.Monitors[id])
			state := s.fsm.image.ActionProjectionState
			generation := uint64(0)
			if state != nil {
				generation = state.Revision
			}
			s.fsm.mu.RUnlock()
			if !pending {
				break
			}
			if generation == 0 {
				return errActionProjectionPending
			}
			results, err := s.Submit(ctx, []Command{{Kind: "action_project", Revision: "notifications", MonitorID: id, Generation: generation, At: time.Now().UTC()}})
			if err != nil {
				return err
			}
			if len(results) != 1 {
				return errActionProjectionPending
			}
			if results[0].Err != nil {
				return results[0].Err
			}
			if !results[0].Allowed {
				return errActionProjectionPending
			}
		}
	}
	return nil
}

func validateNotificationProjectionState(i image) error {
	s := i.ActionProjectionState
	if s == nil || s.Notifications == nil {
		return nil
	}
	p := s.Notifications
	m, exists := i.Monitors[p.MonitorID]
	if !notificationConditionStorageFormat(i.Version) || !s.Ready || s.Current != nil || !exists || !needsNotificationProjection(m) ||
		i.ActionStorage == nil || p.Fence != i.ActionStorage.Sequence || !catalogIdentifier(p.After, 256) ||
		p.At.IsZero() || p.At.Year() < 1 || p.At.Year() > 9999 || len(p.Closures) > m.Policy.Endpoints["green"] {
		return errActionLedger
	}
	if o := p.Opening; o != nil && (!catalogIdentifier(o.ActionID, 256) || !catalogIdentifier(o.Episode, 256) || o.StartedAt.IsZero() || o.ProviderVersion < 0 || o.ProviderVersion > 1) {
		return errActionLedger
	}
	for endpoint, closing := range p.Closures {
		if !catalogIdentifier(endpoint, 256) || !catalogIdentifier(closing.ActionID, 256) || !catalogIdentifier(closing.DeliveryID, 256) ||
			closing.Attempt < 1 || closing.Attempt > 3 || !validDeliveryState(closing.State) || !validNotificationReason(closing.Reason) {
			return errActionLedger
		}
	}
	return nil
}

func validateNotificationProjectionMigration(i image, view *collectionLedgerView) error {
	if i.ActionProjectionState == nil || i.ActionProjectionState.Notifications == nil {
		return nil
	}
	p := i.ActionProjectionState.Notifications
	m := i.Monitors[p.MonitorID]
	expected := &notificationProjectionMigration{MonitorID: p.MonitorID, Fence: p.Fence, After: p.After, At: p.At}
	if err := view.walkActions(func(_ []byte, row actionLedgerRow) error {
		if row.MonitorID != p.MonitorID || row.Action.ID > p.After {
			return nil
		}
		version, found, err := readActionVersion(view.tx, view.actions, row.Action.ID, p.Fence)
		if err != nil {
			return err
		}
		if found {
			expected.include(m, version.Action)
		}
		return nil
	}); err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, p) {
		return errActionLedger
	}
	return nil
}
