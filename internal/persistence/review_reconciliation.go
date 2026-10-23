package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

var errReviewProgress = errors.New("review assessment progress changed")

func (f *machine) reviewRestoreID() string {
	if f.image.Restore != nil {
		return f.image.Restore.Marker.ID
	}
	return ""
}

func reviewStateKey(m Monitor, at time.Time) string {
	data, _ := json.Marshal(struct {
		Owner, Incident, Health, Policy string
		Closed                          time.Time
		Qualified, Suppressed, Removed  bool
	}{m.incarnationIdentity(), m.IncidentID, m.notificationHealth(), m.ReviewPolicyID, m.IncidentClosedAt, m.Recovering && m.outageQualified(), m.notificationsSuppressed(at), m.Removed})
	return string(data)
}
func (f *machine) writeReview(r reviewRecord) {
	if f.reviewWrites == nil {
		f.reviewWrites = make(map[string]reviewRecord)
	}
	f.reviewWrites[r.Key] = r
}
func (f *machine) readReview(key string) (reviewRecord, bool, error) {
	if r, ok := f.reviewWrites[key]; ok {
		return r, !r.Deleted, nil
	}
	if f.collections == nil {
		return reviewRecord{}, false, nil
	}
	r, ok, err := f.collections.reviewRecord(key)
	if err != nil {
		f.err = err
	}
	return r, ok, err
}
func (f *machine) recordReviewWork(previous Monitor, m *Monitor, at time.Time) {
	if !f.projectionsEnabled() || f.image.ActionStorage == nil || !actionStorageFormat(m.ActionVersion) {
		return
	}
	if m.Notifications != nil {
		for id, alert := range m.Notifications.ReviewAlerts {
			key := reviewAlertKey(m.ID, id)
			old, exists, err := f.readReview(key)
			if err != nil {
				return
			}
			if !exists || !reflect.DeepEqual(old.Alert, &alert) {
				copy := alert
				f.writeReview(reviewRecord{Key: key, MonitorID: m.ID, Alert: &copy})
			}
		}
	}
	if m.reviewAssessment || f.image.Restore != nil && f.image.Restore.Phase != "complete" {
		return
	}
	// Conditions from removed/changed blue configuration still need closure facts.
	if m.Policy.Endpoints["blue"] == 0 && (previous.ActionProjection == nil || previous.ActionProjection.ReviewConditions == 0) && (m.Notifications == nil || len(m.Notifications.ReviewAlerts) == 0) {
		return
	}
	if m.ReviewPolicyID == "" {
		if err := f.captureReviewPolicy(m); err != nil {
			f.err = err
			return
		}
	}
	source := ""
	m.ReviewAssessmentKey = reviewStateKey(*m, at)
	changed := previous.ReviewAssessmentKey != reviewStateKey(*m, at)
	if !changed {
		for id, row := range f.actionWrites {
			intervention := row.Action.Kind == "intervention"
			replacedNotice := row.Action.Kind == "code" && row.Action.ReviewNotice != nil && row.Action.State == Cancelled && row.Action.Outcome == "worker_notification_changed"
			if row.MonitorID != m.ID || !intervention && !replacedNotice {
				continue
			}
			old, ok, err := f.actionForMonitor(previous, id)
			if err != nil {
				return
			}
			// actionWrites already contains the new row; prefer the detached old working
			// dependency when present and otherwise the pinned ledger head.
			if a, present := previous.Actions[id]; present {
				old, ok = a, true
			} else if f.collections != nil {
				pin, e := f.collections.pinActions()
				if e != nil {
					f.err = e
					return
				}
				r, present, e := pin.Get(context.Background(), id)
				pin.Close()
				if e != nil {
					f.err = e
					return
				}
				old, ok = r.Action, present
			}
			if !ok || intervention && (old.Held() != row.Action.Held() || !reflect.DeepEqual(old.Review, row.Action.Review)) || replacedNotice && (old.State != Cancelled || old.Outcome != row.Action.Outcome) {
				actionID := id
				if replacedNotice {
					// Retiring an ungranted stale worker message must reassess its
					// condition even if health has not changed again. The replacement
					// still inherits the original delivery deadline.
					actionID = row.Action.ReviewNotice.ActionID
				}
				if source != "" && source != actionID {
					changed = true
					source = ""
					break
				}
				source = actionID
			}
		}
	}
	if !changed && source == "" {
		return
	}
	context := reviewContext(*m, at)
	key := reviewPrefix("fact", m.ID) + fmt.Sprintf("%020d-%010d", f.actionPosition.Index, f.actionPosition.Ordinal)
	fact := reviewFact{ID: key, PolicyID: m.ReviewPolicyID, RestoreID: f.reviewRestoreID(), At: at, Fence: f.image.ActionStorage.Sequence + 1, Context: context, SourceAction: source, Phase: "actions", Revision: 1}
	if notificationConditionStorageFormat(f.image.Version) {
		fact.IndexVersion = 1
	}
	f.writeReview(reviewRecord{Key: key, MonitorID: m.ID, Fact: &fact})
}

// Only blue delivery eligibility belongs in a deferred assessment. Capture the
// committed suppression decision; unrelated rules, exposure maps and retained
// action summaries cannot inflate this record as incidents accumulate.
func reviewContext(m Monitor, at time.Time) Monitor {
	p := Policy{Interval: m.Policy.Interval, Enabled: true, Unhealthy: m.Policy.Unhealthy,
		Endpoints: map[string]int{"blue": m.Policy.Endpoints["blue"]}}
	c := Monitor{ID: m.ID, CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation,
		ReviewRevisionDigest: m.ReviewRevisionDigest,
		InstallationID:       m.InstallationID, AdoptedFileIncarnation: m.AdoptedFileIncarnation,
		Policy: p, ActionVersion: m.ActionVersion, LifecycleVersion: m.LifecycleVersion,
		NotificationVersion: m.NotificationVersion, NotificationEpisode: m.NotificationEpisode,
		IncidentID: m.IncidentID, IncidentRevision: m.IncidentRevision, IncidentSequence: m.IncidentSequence,
		IncidentOpenedAt: m.IncidentOpenedAt, IncidentClosedAt: m.IncidentClosedAt,
		Incident: m.Incident, Recovering: m.Recovering, Warning: m.Warning, VerifyRemaining: m.VerifyRemaining,
		LastCheck: m.LastCheck, LastOutcome: m.LastOutcome, Removed: m.Removed,
		NotificationMaintenance: m.notificationsSuppressed(at)}
	if c.LastOutcome != "" && c.LastOutcome != "success" {
		c.LastOutcome = "failure"
	}
	if c.ReviewRevisionDigest == "" && m.Revision != "" {
		c.ReviewRevisionDigest = identity(m.Revision)
	}
	if m.Notifications != nil {
		c.Notifications = &NotificationState{Version: m.Notifications.Version, QualifiedIncident: m.Notifications.QualifiedIncident}
	}
	if m.notificationConditionsEnabled() {
		c.ConfigurationProblem, c.ConfigurationProblemAt = m.ConfigurationProblem, m.ConfigurationProblemAt
	}
	return c.Clone()
}

// hydrateReviewDependencies loads only identities addressed by this command.
// It never publishes the detached historical inputs as operational references.
func (f *machine) hydrateReviewDependencies(m Monitor, id string) (Monitor, error) {
	a, ok, err := f.actionForMonitor(m, id)
	if err != nil || !ok {
		return m, err
	}
	m = withAction(m, a)
	if a.ReviewNotice == nil {
		return m, nil
	}
	var r reviewRecord
	var found bool
	if m.Notifications != nil {
		if alert, exists := m.Notifications.ReviewAlerts[a.ReviewNotice.AlertID]; exists {
			copy := alert
			r.Alert = &copy
			found = true
		}
	}
	if !found {
		r, found, err = f.readReview(reviewAlertKey(m.ID, a.ReviewNotice.AlertID))
	}
	if err != nil {
		return m, err
	}
	if !found {
		if m.Notifications != nil {
			if _, exists := m.Notifications.ReviewAlerts[a.ReviewNotice.AlertID]; exists {
				return m, nil
			}
		}
		return m, errActionLedger
	}
	m.Notifications = m.Notifications.clone()
	if m.Notifications == nil {
		m.Notifications = &NotificationState{}
	}
	if m.Notifications.ReviewAlerts == nil {
		m.Notifications.ReviewAlerts = make(map[string]ReviewAlert)
	}
	m.Notifications.ReviewAlerts[r.Alert.ID] = *r.Alert
	underlying, found, err := f.actionForMonitor(m, r.Alert.ActionID)
	if err != nil || !found {
		return m, errActionLedger
	}
	return withAction(m, underlying), nil
}
func (f *machine) reviewFactAction(fact reviewFact, id string) (Action, bool, error) {
	pin, err := f.collections.pinActions()
	if err != nil {
		return Action{}, false, err
	}
	defer pin.Close()
	pin.sequence = fact.Fence
	r, found, err := pin.Get(context.Background(), id)
	return r.Action, found && r.MonitorID == fact.Context.ID, err
}
func (f *machine) assessmentInput(fact reviewFact, alert ReviewAlert) (Monitor, error) {
	m, err := f.reviewInput(fact, alert.Endpoint)
	if err != nil {
		return m, err
	}
	m.reviewAssessment = true
	m.Sequence = f.image.Monitors[m.ID].Sequence
	m.Actions = make(map[string]Action)
	a, found, err := f.reviewFactAction(fact, alert.ActionID)
	if err != nil || !found {
		return m, errActionLedger
	}
	m.Actions[a.ID] = a
	if m.Notifications == nil {
		m.Notifications = &NotificationState{}
	}
	m.Notifications.ReviewAlerts = map[string]ReviewAlert{alert.ID: alert}
	current := f.image.Monitors[m.ID]
	for _, id := range []string{alert.OpeningDelivery, alert.ClosingDelivery} {
		if id == "" {
			continue
		}
		for attempt := 1; attempt <= 3; attempt++ {
			key := id
			if attempt > 1 {
				key = identity(fmt.Sprintf("%s/attempt/%d", id, attempt))
			}
			a, ok, e := f.actionForMonitor(current, key)
			if e != nil {
				return m, e
			}
			if ok {
				// Assessment can revise an unstarted notice. Detach its payload so
				// comparison with the current projection still detects that write.
				m.Actions[key] = a.Clone()
			}
		}
	}
	return m, nil
}

// Each committed step visits one action/endpoint or condition. This bounds both
// input and fan-out; a configured record may use its existing 2 MiB frame cap.
// No external operation runs here. Current start guards remain authoritative.
func (f *machine) applyReviewItem(c Command) Result {
	r, ok, err := f.readReview(c.Revision)
	if err != nil {
		return Result{Err: err}
	}
	if !ok || r.Fact == nil || r.MonitorID != c.MonitorID || r.Fact.Revision != c.Generation {
		return Result{Err: errReviewProgress}
	}
	first, err := f.collections.reviewPage(reviewPrefix("fact", c.MonitorID), "", 1)
	if err != nil {
		return Result{Err: err}
	}
	if len(first) == 0 || first[0].Key != r.Key {
		return Result{Err: errReviewProgress}
	}
	fact := *r.Fact
	current := f.image.Monitors[c.MonitorID]
	active := 0
	for _, a := range current.Actions {
		if a.ReviewNotice != nil && (a.State == Queued || a.State == Started) {
			active++
		}
	}
	var alert *ReviewAlert
	newAlert := false
	done := false
	if fact.Phase == "actions" {
		var a Action
		found := false
		if fact.SourceAction != "" {
			if fact.After == "" {
				a, found, err = f.reviewFactAction(fact, fact.SourceAction)
			}
		} else {
			pin, e := f.collections.pinActions()
			if e != nil {
				return Result{Err: e}
			}
			pin.sequence = fact.Fence
			var rows []actionLedgerRow
			var next string
			if fact.IndexVersion == 1 {
				rows, next, e = pin.relevantPage(context.Background(), c.MonitorID, fact.After, 64, 1<<20)
			} else {
				rows, next, e = pin.pageBounded(context.Background(), c.MonitorID, fact.After, 1, 64, 1<<20)
			}
			pin.Close()
			err = e
			if len(rows) > 0 {
				a, found = rows[0].Action, true
			} else if e == nil && next != "" {
				// A bounded page can contain only records newer than this fact.
				// Its continuation is progress, not proof that the fence is exhausted.
				fact.After = next
				fact.Revision++
				r.Fact = &fact
				f.writeReview(r)
				return Result{Allowed: true}
			}
		}
		if err != nil {
			return Result{Err: err}
		}
		if !found {
			fact.Phase = "conditions"
			fact.After = ""
			fact.Endpoint = 0
		} else {
			context, e := f.reviewInput(fact, fact.Endpoint)
			if e != nil {
				return Result{Err: e}
			}
			if context.OwnsAction(a) && a.Kind == "intervention" && a.Held() && context.Recovering && context.outageQualified() && fact.Endpoint < context.Policy.Endpoints["blue"] {
				endpoint := fact.Endpoint
				endpointID := notificationEndpointIdentity(context, "blue", endpoint)
				original := identity("review-alert/" + a.ID + "/" + context.IncidentID + "/" + endpointID)
				id := context.namespacedIdentity(original)
				saved, exists, e := f.readReview(reviewAlertKey(c.MonitorID, original))
				if e != nil {
					return Result{Err: e}
				}
				if !exists {
					saved, exists, e = f.readReview(reviewAlertKey(c.MonitorID, id))
					if e != nil {
						return Result{Err: e}
					}
				}
				if exists {
					value := *saved.Alert
					alert = &value
				} else {
					alert = &ReviewAlert{Owner: &reviewAlertOwner{CatalogUID: context.CatalogUID, FileIncarnation: context.FileIncarnation}, ID: id, ActionID: a.ID, IncidentID: context.IncidentID, Revision: context.Revision, EndpointID: endpointID, Endpoint: endpoint, OpenedAt: fact.At, Health: context.observedHealth(), ProviderCorrelationVersion: 1}
					newAlert = true
				}
				fact.Endpoint++
			}
			if fact.Endpoint >= context.Policy.Endpoints["blue"] || !context.OwnsAction(a) || a.Kind != "intervention" || !a.Held() || !context.Recovering || !context.outageQualified() {
				fact.After = a.ID
				fact.Endpoint = 0
			}
		}
	} else {
		var rows []reviewRecord
		var e error
		if fact.IndexVersion == 1 {
			rows, e = f.collections.reviewConditions(c.MonitorID, fact.SourceAction, fact.After)
		} else {
			rows, e = f.collections.reviewPage(reviewPrefix("alert", c.MonitorID), fact.After, 1)
		}
		if e != nil {
			return Result{Err: e}
		}
		if len(rows) == 0 {
			done = true
		} else {
			fact.After = rows[0].Key
			if fact.SourceAction == "" || rows[0].Alert.ActionID == fact.SourceAction {
				latest, exists, readErr := f.readReview(rows[0].Key)
				if readErr != nil || !exists {
					return Result{Err: errActionLedger}
				}
				value := *latest.Alert
				alert = &value
			}
		}
	}
	var events []Event
	next := current.Clone()
	next.reviewAssessment = true
	if alert != nil {
		input, e := f.assessmentInput(fact, *alert)
		if e != nil {
			return Result{Err: e}
		}
		emit := func(kind string, a *Action) {
			events = append(events, Event{MonitorID: next.ID, CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, Revision: a.Revision, At: fact.At, Type: kind, IncidentID: a.IncidentID, ActionID: a.ID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint, Outcome: a.Outcome, DeliveryID: a.DeliveryID, NotificationEpisode: a.NotificationEpisode, EndpointID: a.EndpointID, ReviewNotice: a.ReviewNotice}.Clone())
		}
		if newAlert {
			a := input.Actions[alert.ActionID]
			emit("review_alert_opened", &Action{ID: a.ID, Kind: "intervention", CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, Revision: a.Revision, IncidentID: alert.IncidentID, ReviewNotice: &ReviewNotice{AlertID: alert.ID, ActionID: a.ID, Phase: "open", Health: alert.Health}})
		}
		assessed := input.assessReviewAlert(*alert, fact.At, emit)
		if input.notificationConditionsEnabled() && current.notificationConditionsEnabled() &&
			input.incarnationIdentity() == current.incarnationIdentity() && input.Revision == current.Revision {
			// Recovery/review facts use their captured observations. An older
			// assessment must not overwrite current-health wording already
			// refreshed by a later check/preparation result.
			input.Notifications.ReviewAlerts[assessed.ID] = assessed
			input.refreshQueuedNotificationHealth(current.notificationHealth(), emit)
			assessed = input.Notifications.ReviewAlerts[assessed.ID]
		}
		next.Sequence = input.Sequence
		if next.Notifications == nil {
			next.Notifications = &NotificationState{}
		}
		next.Notifications.ReviewAlerts = map[string]ReviewAlert{assessed.ID: assessed}
		for id, a := range input.Actions {
			if a.Kind == "code" {
				old, exists, e := f.actionForMonitor(current, id)
				if e != nil {
					return Result{Err: e}
				}
				if !exists || !reflect.DeepEqual(old, a) {
					if next.Actions == nil {
						next.Actions = make(map[string]Action)
					}
					next.Actions[id] = a
				}
			}
		}
		if newAlert && next.ActionProjection != nil {
			next.ActionProjection.ReviewConditions++
		}
		// A delayed historical assessment cannot authorize a stale opening. Closures
		// keep their original episode and correlation, including during a later outage.
		for id, a := range next.Actions {
			if a.State == Queued && a.ReviewNotice != nil && a.ReviewNotice.AlertID == assessed.ID {
				checked, e := f.hydrateReviewDependencies(next, id)
				if e != nil {
					return Result{Err: e}
				}
				binding := a.CatalogUID == next.CatalogUID && a.FileIncarnation == next.FileIncarnation && a.Revision == next.Revision && !next.Removed && a.Endpoint < next.Policy.Endpoints["blue"] && notificationEndpointIdentity(next, "blue", a.Endpoint) == a.EndpointID
				if fact.RestoreID != f.reviewRestoreID() {
					next.cancelNotification(a, "explicit_restore", c.At, emit)
				} else if !binding {
					reason := "configuration_changed"
					if a.CatalogUID != next.CatalogUID || a.FileIncarnation != next.FileIncarnation || next.Removed {
						reason = "incarnation_changed"
					}
					assessed.ClosureBlocked = reason
					next.Notifications.ReviewAlerts[assessed.ID] = assessed
					next.cancelNotification(a, reason, c.At, emit)
				} else if a.ReviewNotice.Phase == "open" && (!checked.reviewNoticeEligible(a) || next.notificationsSuppressed(c.At)) {
					next.cancelNotification(a, "notification_superseded", c.At, emit)
				}
			}
		}
		nextActive := 0
		for _, a := range next.Actions {
			if a.ReviewNotice != nil && (a.State == Queued || a.State == Started) {
				nextActive++
			}
		}
		if nextActive > activeReviewDeliveries && nextActive > active {
			return Result{Err: errReviewProgress}
		}
		events = append(events, f.installMonitor(current, next, c.At)...)
		f.publishControlChange(f.image.Monitors[next.ID])
	}
	fact.Revision++
	if done {
		f.writeReview(reviewRecord{Key: r.Key, MonitorID: r.MonitorID, Deleted: true})
	} else {
		r.Fact = &fact
		f.writeReview(r)
	}
	return Result{Allowed: true, Monitor: &next, Events: events}
}

// New facts advance through a bounded accepted prefix in one durable command.
// Existing facts preserve their original one-item replay behavior.
func (f *machine) applyReviewStep(c Command) Result {
	r, ok, err := f.readReview(c.Revision)
	if err != nil {
		return Result{Err: err}
	}
	if !ok || r.Fact == nil || r.Fact.IndexVersion == 0 {
		return f.applyReviewItem(c)
	}
	result := Result{Allowed: true}
	for step := 0; step < 32; step++ {
		item := f.applyReviewItem(c)
		if item.Err != nil {
			if step > 0 && errors.Is(item.Err, errReviewProgress) {
				return result
			}
			return item
		}
		result.Events = append(result.Events, item.Events...)
		if item.Monitor != nil {
			result.Monitor = item.Monitor
		}
		next, exists, err := f.readReview(c.Revision)
		if err != nil {
			return Result{Err: err}
		}
		if !exists {
			return result
		}
		c.Generation = next.Fact.Revision
		bytes := 0
		for _, row := range f.actionWrites {
			data, _ := json.Marshal(row)
			bytes += len(data)
		}
		for _, row := range f.reviewWrites {
			data, _ := json.Marshal(row)
			bytes += len(data)
		}
		if bytes >= 1<<20 {
			return result
		}
	}
	return result
}

func (s *Store) maintainReviewWork(after *string) error {
	stopAt := time.Now().Add(25 * time.Millisecond)
	for n := 0; n < 32 && time.Now().Before(stopAt); n++ {
		select {
		case <-s.stop:
			return nil
		default:
		}
		if s.ControllerHealth() != nil {
			return nil
		}
		s.fsm.mu.RLock()
		ledger := s.fsm.collections
		if ledger == nil {
			s.fsm.mu.RUnlock()
			return nil
		}
		pin, err := ledger.pinActions()
		s.fsm.mu.RUnlock()
		if err != nil {
			return err
		}
		rows, err := ledger.reviewPage("fact/", *after, 1)
		pin.Close()
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			if *after == "" {
				return nil
			}
			*after = ""
			continue
		}
		row := rows[0]
		*after = reviewPrefix("fact", row.MonitorID) + "\xff"
		if row.Fact == nil {
			return errActionLedger
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := s.Submit(ctx, []Command{{Kind: "action_reconcile", MonitorID: row.MonitorID, Revision: row.Key, Generation: row.Fact.Revision, At: time.Now().UTC()}})
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if len(result) != 1 {
			return errActionLedger
		}
		if result[0].Err != nil && !errors.Is(result[0].Err, errReviewProgress) {
			return result[0].Err
		}
	}
	return nil
}
