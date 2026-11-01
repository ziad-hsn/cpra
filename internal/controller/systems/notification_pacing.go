package systems

import (
	"context"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func notificationPermit(scopes []jobs.NotificationRateScope) *persistence.NotificationPermit {
	if len(scopes) == 0 {
		return nil
	}
	permit := &persistence.NotificationPermit{}
	for _, s := range scopes {
		permit.Scopes = append(permit.Scopes, persistence.NotificationRateScope{ID: s.ID, Interval: s.Interval, Global: s.Global, Serial: s.Serial})
	}
	return permit
}
func (s *DurableSystem) restoreNotificationRates(ctx context.Context) error {
	q, ok := s.queues["code"].(interface {
		SetNotificationRates(map[string]jobs.NotificationRateState)
	})
	if !ok {
		return nil
	}
	states, err := s.store.NotificationLimits(ctx)
	if err != nil {
		return err
	}
	values := make(map[string]jobs.NotificationRateState, len(states))
	for id, state := range states {
		values[id] = jobs.NotificationRateState{NotBefore: state.NotBefore, Spacing: state.Spacing, SpacingUntil: state.SpacingUntil}
	}
	q.SetNotificationRates(values)
	return nil
}
func (s *DurableSystem) recordNotificationFeedback(ctx context.Context, batch []jobs.Result) error {
	var commands []persistence.Command
	for _, r := range batch {
		if r.RateFeedback == nil {
			continue
		}
		a, found, err := s.store.ActionContext(ctx, r.ActionID)
		if err != nil {
			return err
		}
		if !found || a.NotificationPermit == nil || a.StartedAt.IsZero() || a.Revision != r.Revision {
			continue
		}
		feedback := &persistence.NotificationFeedback{}
		for _, update := range r.RateFeedback.Updates {
			feedback.Updates = append(feedback.Updates, persistence.NotificationRateUpdate{ID: update.ID, State: persistence.NotificationRateState{NotBefore: update.State.NotBefore, Spacing: update.State.Spacing, SpacingUntil: update.State.SpacingUntil}})
		}
		commands = append(commands, persistence.Command{MonitorID: r.MonitorID, Revision: r.Revision, ActionID: r.ActionID, At: r.RateFeedback.At, NotificationFeedback: feedback})
	}
	if len(commands) == 0 {
		return nil
	}
	if err := s.store.RecordNotificationFeedback(ctx, commands); err != nil {
		return err
	}
	return s.restoreNotificationRates(ctx)
}
