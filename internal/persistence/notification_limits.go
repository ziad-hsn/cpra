package persistence

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
)

const MaxNotificationLimitScopes = 4096

var ErrNotificationRateLimited = errors.New("notification rate permission is not available")
var errNotificationLimit = errors.New("invalid notification limit record")

// Scope IDs are private digests. They contain no provider URLs, keys or names.
type NotificationRateScope struct {
	Global   bool          `json:"global,omitempty"`
	Serial   bool          `json:"serial,omitempty"`
	ID       string        `json:"id"`
	Interval time.Duration `json:"interval,omitempty"`
}
type NotificationPermit struct {
	Scopes []NotificationRateScope `json:"scopes"`
}
type NotificationRateState struct {
	InFlight        int           `json:"in_flight,omitempty"`
	ReleaseInterval time.Duration `json:"release_interval,omitempty"`
	NotBefore       time.Time     `json:"not_before"`
	Spacing         time.Duration `json:"spacing,omitempty"`
	SpacingUntil    time.Time     `json:"spacing_until,omitzero"`
}
type NotificationRateUpdate struct {
	ID    string                `json:"id"`
	State NotificationRateState `json:"state"`
}
type NotificationFeedback struct {
	Updates []NotificationRateUpdate `json:"updates,omitempty"`
}

func validSHA256(value string) bool {
	raw, err := hex.DecodeString(value)
	return len(value) == 64 && len(raw) == 32 && err == nil && hex.EncodeToString(raw) == value
}

func (p *NotificationPermit) clone() *NotificationPermit {
	if p == nil {
		return nil
	}
	return &NotificationPermit{Scopes: slices.Clone(p.Scopes)}
}
func (p *NotificationPermit) valid() bool {
	if p == nil || len(p.Scopes) == 0 || len(p.Scopes) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, s := range p.Scopes {
		if !validSHA256(s.ID) || seen[s.ID] || s.Interval < 0 || s.Interval > 24*time.Hour {
			return false
		}
		seen[s.ID] = true
	}
	return true
}
func validNotificationRateState(s NotificationRateState, at time.Time) bool {
	return s.InFlight == 0 && s.ReleaseInterval == 0 && !s.NotBefore.IsZero() && !s.NotBefore.Before(at) && s.NotBefore.Sub(at) <= 24*time.Hour &&
		s.Spacing >= 0 && s.Spacing <= 24*time.Hour &&
		(s.Spacing == 0 && s.SpacingUntil.IsZero() || s.Spacing > 0 && s.SpacingUntil.After(at) && s.SpacingUntil.Sub(at) <= 24*time.Hour)
}
func validateNotificationLimitCommand(c Command) error {
	if c.NotificationPermit != nil && (c.Kind != "start" || !c.NotificationPermit.valid()) {
		return errNotificationLimit
	}
	if c.Kind != "notification_feedback" {
		if c.NotificationFeedback != nil {
			return errNotificationLimit
		}
		return nil
	}
	extra := c
	extra.Kind, extra.MonitorID, extra.Revision, extra.ActionID, extra.ExecutorSession, extra.At, extra.NotificationFeedback = "", "", "", "", "", time.Time{}, nil
	if extra != (Command{}) || c.At.IsZero() || !catalogIdentifier(c.MonitorID, 256) || c.Revision == "" || len(c.Revision) > 512 || !catalogIdentifier(c.ActionID, 256) || c.ExecutorSession == "" || len(c.ExecutorSession) > 128 || c.NotificationFeedback == nil || len(c.NotificationFeedback.Updates) > 4 {
		return errNotificationLimit
	}
	seen := map[string]bool{}
	for _, u := range c.NotificationFeedback.Updates {
		if !validSHA256(u.ID) || seen[u.ID] || !validNotificationRateState(u.State, c.At) {
			return errNotificationLimit
		}
		seen[u.ID] = true
	}
	return nil
}

// admitNotificationPermit is called only after the pure transition would grant
// a start, and before installing it. Refusal consumes no attempt or budget.
func (f *machine) admitNotificationPermit(c Command, r *Result) {
	if !r.Allowed || r.Monitor == nil {
		return
	}
	a := r.Monitor.Actions[c.ActionID]
	permit := a.NotificationPermit
	if permit == nil {
		permit = c.NotificationPermit
	}
	if permit == nil {
		return
	}
	if a.Kind != "code" {
		*r = Result{Err: errNotificationLimit}
		return
	}
	for _, scope := range permit.Scopes {
		state := f.image.NotificationLimits[scope.ID]
		paced := scope.Serial || scope.Interval > 0 || state.ReleaseInterval > 0 || c.At.Before(state.SpacingUntil) && state.Spacing > 0
		if state.InFlight >= maxLocalExecutors || c.At.Before(state.NotBefore) || paced && state.InFlight > 0 {
			*r = Result{Err: ErrNotificationRateLimited}
			return
		}
	}
	needed := make([]string, 0, len(permit.Scopes))
	for _, scope := range permit.Scopes {
		needed = append(needed, scope.ID)
	}
	if !f.notificationLimitCapacity(needed, c.At) {
		*r = Result{Err: ErrNotificationRateLimited}
		return
	}
	for _, scope := range permit.Scopes {
		state := f.image.NotificationLimits[scope.ID]
		spacing := scope.Interval
		if c.At.Before(state.SpacingUntil) {
			spacing = max(spacing, state.Spacing)
		} else {
			state.Spacing = 0
			state.SpacingUntil = time.Time{}
		}
		state.InFlight++
		state.ReleaseInterval = max(state.ReleaseInterval, spacing)
		if state.NotBefore.IsZero() {
			state.NotBefore = c.At
		}
		if f.image.NotificationLimits == nil {
			f.image.NotificationLimits = map[string]NotificationRateState{}
		}
		f.image.NotificationLimits[scope.ID] = state
	}
	a.NotificationPermit = permit.clone()
	r.Monitor.Actions[c.ActionID] = a
}

func (f *machine) notificationLimitCapacity(ids []string, at time.Time) bool {
	needed := 0
	for _, id := range ids {
		if _, exists := f.image.NotificationLimits[id]; !exists {
			needed++
		}
	}
	if len(f.image.NotificationLimits)+needed <= MaxNotificationLimitScopes {
		return true
	}
	// Bounded by a fixed process-wide cap; deleting every eligible entry makes
	// the outcome independent of map iteration order. Active windows survive.
	for id, state := range f.image.NotificationLimits {
		if state.InFlight == 0 && !at.Before(state.NotBefore) && !at.Before(state.SpacingUntil) {
			delete(f.image.NotificationLimits, id)
		}
	}
	needed = 0
	for _, id := range ids {
		if _, exists := f.image.NotificationLimits[id]; !exists {
			needed++
		}
	}
	return len(f.image.NotificationLimits)+needed <= MaxNotificationLimitScopes
}
func mergeNotificationRateState(old, next NotificationRateState) NotificationRateState {
	if next.NotBefore.After(old.NotBefore) {
		old.NotBefore = next.NotBefore
	}
	if next.SpacingUntil.After(old.SpacingUntil) {
		old.SpacingUntil = next.SpacingUntil
	}
	old.Spacing = max(old.Spacing, next.Spacing)
	return old
}
func (f *machine) applyNotificationFeedback(c Command) Result {
	m, ok := f.image.Monitors[c.MonitorID]
	if !ok {
		return Result{Err: ErrCatalogDependency}
	}
	a, found, err := f.actionForMonitor(m, c.ActionID)
	if err != nil {
		f.err = err
		return Result{Err: err}
	}
	if !found || a.Kind != "code" || a.Revision != c.Revision || a.ExecutorSession != c.ExecutorSession || a.StartedAt.IsZero() || c.At.Before(a.StartedAt) || a.NotificationPermit == nil {
		return Result{Err: errNotificationLimit}
	}
	raw, _ := json.Marshal(c.NotificationFeedback)
	digest := identity(c.At.UTC().Format(time.RFC3339Nano) + "/" + string(raw))
	if a.NotificationFeedbackDigest != "" {
		if a.NotificationFeedbackDigest != digest {
			return Result{Err: errNotificationLimit}
		}
		return Result{Allowed: true}
	}
	// A restarted executor has already reset the old session's reservations.
	// Fresh feedback from it cannot release permission owned by the new one.
	if c.ExecutorSession != f.image.LocalExecutorSession {
		return Result{Err: errNotificationLimit}
	}
	ids := make([]string, 0, len(c.NotificationFeedback.Updates))
	for _, update := range c.NotificationFeedback.Updates {
		if !slices.ContainsFunc(a.NotificationPermit.Scopes, func(s NotificationRateScope) bool { return s.ID == update.ID }) {
			return Result{Err: errNotificationLimit}
		}
		ids = append(ids, update.ID)
	}
	// Every scope was reserved before start. A current-session response never
	// competes with new admission for the space needed to preserve its outcome.
	for _, scope := range a.NotificationPermit.Scopes {
		if f.image.NotificationLimits[scope.ID].InFlight < 1 {
			return Result{Err: errNotificationLimit}
		}
	}
	if f.image.NotificationLimits == nil {
		f.image.NotificationLimits = map[string]NotificationRateState{}
	}
	for _, update := range c.NotificationFeedback.Updates {
		f.image.NotificationLimits[update.ID] = mergeNotificationRateState(f.image.NotificationLimits[update.ID], update.State)
	}
	for _, scope := range a.NotificationPermit.Scopes {
		state := f.image.NotificationLimits[scope.ID]
		interval := max(scope.Interval, state.ReleaseInterval)
		if c.At.Before(state.SpacingUntil) {
			interval = max(interval, state.Spacing)
		}
		if next := c.At.Add(interval); next.After(state.NotBefore) {
			state.NotBefore = next
		}
		state.InFlight--
		if state.InFlight == 0 {
			state.ReleaseInterval = 0
		}
		f.image.NotificationLimits[scope.ID] = state
		if state.InFlight == 0 && !c.At.Before(state.NotBefore) && !c.At.Before(state.SpacingUntil) {
			delete(f.image.NotificationLimits, scope.ID)
		}
	}
	m = m.Clone()
	_, projected := m.Actions[a.ID]
	a.NotificationFeedbackDigest = digest
	if m.Actions == nil {
		m.Actions = map[string]Action{}
	}
	m.Actions[a.ID] = a
	if !projected {
		m.retireAction(a.ID)
	}
	return Result{Allowed: true, Monitor: &m}
}

func (f *machine) resetNotificationReservations(at time.Time) {
	for id, state := range f.image.NotificationLimits {
		if state.InFlight > 0 {
			interval := state.ReleaseInterval
			if at.Before(state.SpacingUntil) {
				interval = max(interval, state.Spacing)
			}
			if next := at.Add(interval); next.After(state.NotBefore) {
				state.NotBefore = next
			}
			state.InFlight, state.ReleaseInterval = 0, 0
			f.image.NotificationLimits[id] = state
		}
		if !at.Before(state.NotBefore) && !at.Before(state.SpacingUntil) {
			delete(f.image.NotificationLimits, id)
		}
	}
}
func validateNotificationActionLimits(a Action, version int) error {
	if a.NotificationPermit == nil {
		if a.NotificationFeedbackDigest != "" {
			return errNotificationLimit
		}
		return nil
	}
	queuedRetry := (a.State == Queued || a.State == Cancelled) && a.Attempt > 1 && a.StartedAt.IsZero() && a.ExecutorSession == "" && !a.Deadline.IsZero() && a.NotificationFeedbackDigest == ""
	if !dispatchStorageFormat(version) || !a.NotificationPermit.valid() || a.Kind != "code" || !queuedRetry && (a.StartedAt.IsZero() || a.ExecutorSession == "") || a.NotificationFeedbackDigest != "" && !validSHA256(a.NotificationFeedbackDigest) {
		return errNotificationLimit
	}
	return nil
}
func validateNotificationLimitsImage(i image) error {
	if len(i.NotificationLimits) > MaxNotificationLimitScopes || len(i.NotificationLimits) > 0 && !dispatchStorageFormat(i.Version) {
		return errNotificationLimit
	}
	for id, state := range i.NotificationLimits {
		if !validSHA256(id) || state.NotBefore.IsZero() || state.InFlight < 0 || state.InFlight > maxLocalExecutors || state.ReleaseInterval < 0 || state.ReleaseInterval > 24*time.Hour || state.InFlight == 0 && state.ReleaseInterval != 0 || state.Spacing < 0 || state.Spacing > 24*time.Hour || (state.Spacing == 0) != state.SpacingUntil.IsZero() {
			return errNotificationLimit
		}
	}
	return nil
}

// Snapshot validation visits the existing action inventory once. Only this
// bounded aggregate is retained; completed historical actions add no entries.
type notificationReservationInventory map[string]NotificationRateState

func (v notificationReservationInventory) add(i image, a Action) error {
	if a.NotificationPermit == nil || a.StartedAt.IsZero() || a.ExecutorSession != i.LocalExecutorSession || a.NotificationFeedbackDigest != "" {
		return nil
	}
	for _, scope := range a.NotificationPermit.Scopes {
		state, ok := i.NotificationLimits[scope.ID]
		if !ok || state.InFlight == 0 || len(v) >= MaxNotificationLimitScopes && v[scope.ID].InFlight == 0 {
			return errNotificationLimit
		}
		expected := v[scope.ID]
		expected.InFlight++
		expected.ReleaseInterval = max(expected.ReleaseInterval, scope.Interval)
		if expected.InFlight > maxLocalExecutors {
			return errNotificationLimit
		}
		v[scope.ID] = expected
	}
	return nil
}

func (v notificationReservationInventory) validate(i image) error {
	for id, state := range i.NotificationLimits {
		expected := v[id]
		if state.InFlight != expected.InFlight || state.ReleaseInterval < expected.ReleaseInterval {
			return errNotificationLimit
		}
	}
	return nil
}
func (s *Store) NotificationLimits(ctx context.Context) (map[string]NotificationRateState, error) {
	if err := s.controllerHealthContext(ctx); err != nil {
		return nil, err
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	return maps.Clone(s.fsm.image.NotificationLimits), nil
}

// RecordNotificationFeedback keeps transport observations even when a changed
// monitor no longer accepts its action result. It invokes no provider.
func (s *Store) RecordNotificationFeedback(ctx context.Context, commands []Command) error {
	for i := range commands {
		commands[i].Kind = "notification_feedback"
		commands[i].ExecutorSession = s.executorSession
	}
	results, err := s.Submit(ctx, commands)
	if err != nil {
		return err
	}
	if len(results) != len(commands) {
		return ErrCommitUnconfirmed
	}
	for _, r := range results {
		if r.Err != nil {
			return r.Err
		}
		if !r.Allowed {
			return ErrCommitUnconfirmed
		}
	}
	return nil
}
