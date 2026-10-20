package persistence

import (
	"errors"
	"time"
)

// ActionFormatVersion freezes action budgets at admission and records explicit
// retry permission. Earlier committed envelopes keep their original semantics.
const ActionFormatVersion = 44
const actionSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-44\n"

func actionStorageFormat(version int) bool {
	return version == ActionFormatVersion || version == 45 && externalStorageFormat(version)
}

func requiresActionFormat(m Monitor) bool {
	if m.AdoptedFileIncarnation != nil || m.ActionProjection != nil || m.ReviewAssessmentKey != "" || m.ReviewPolicyID != "" || m.ReviewRevisionDigest != "" || m.InstallationID != "" || m.ActionVersion != 0 || m.Notifications != nil || len(m.Policy.NotificationTimeouts) != 0 || m.ConfigurationProblem != (FailureDiagnostic{}) || !m.ConfigurationProblemAt.IsZero() {
		return true
	}
	for _, a := range m.Actions {
		if a.Retryable || a.ReviewNotice != nil {
			return true
		}
	}
	return false
}

func (m *Monitor) upgradeActions(at time.Time, emit func(string, *Action)) {
	m.upgradeNotificationState()
	if m.ActionVersion == ActionFormatVersion {
		return
	}
	m.ActionVersion = ActionFormatVersion
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if m.OwnsAction(a) && a.Kind == "code" && a.State == Queued && a.Deadline.IsZero() {
			// No persisted original budget exists. A migration cannot turn the
			// current clock into new permission for this old notification.
			m.cancelNotification(a, "notification_deadline_unrecorded", at, emit)
		}
	}
}

func validateActionFormat(m Monitor, version int) error {
	if err := validateActionProjection(m, version); err != nil {
		return err
	}
	if err := validateNotificationState(m, version); err != nil {
		return err
	}
	if !requiresActionFormat(m) {
		return nil
	}
	if !actionStorageFormat(version) || m.ActionVersion != 0 && m.ActionVersion != ActionFormatVersion || m.InstallationID != "" && !catalogIdentifier(m.InstallationID, 256) {
		return errors.New("invalid action storage version")
	}
	if m.ConfigurationProblem != (FailureDiagnostic{}) {
		if !m.ConfigurationProblem.configurationProblem() || m.ConfigurationProblemAt.IsZero() || m.ConfigurationProblem.validate(version) != nil {
			return ErrFailureDiagnostic
		}
	} else if !m.ConfigurationProblemAt.IsZero() {
		return ErrFailureDiagnostic
	}
	for _, a := range m.Actions {
		if err := validateReviewNotice(a, version); err != nil {
			return err
		}
		if a.ReviewNotice != nil && m.ActionProjection == nil {
			if m.Notifications == nil {
				return errors.New("review notification has no alert state")
			}
			alert, ok := m.Notifications.ReviewAlerts[a.ReviewNotice.AlertID]
			if !ok || alert.ActionID != a.ReviewNotice.ActionID {
				return errors.New("review notification has no matching alert")
			}
		}
		if a.Retryable && (a.State != Failed || a.FinishedAt.IsZero()) {
			return errors.New("retry permission requires a confirmed failed action")
		}
	}
	return nil
}

func (p Policy) notificationTimeout(color string, endpoint int) time.Duration {
	if values := p.NotificationTimeouts[color]; endpoint >= 0 && endpoint < len(values) {
		return values[endpoint]
	}
	return NotificationTimeout
}

func (m Monitor) recoveryBudgetAvailable(at time.Time) bool {
	if m.ActionVersion != ActionFormatVersion || m.recoveryAttempts() == 0 {
		return true
	}
	a, ok := m.latestRecovery()
	if due := a.FinishedAt.Add(m.Policy.RecoveryCooldown); due.After(at) {
		at = due
	}
	return ok && !a.Deadline.IsZero() && at.Before(a.Deadline)
}

// Automatic continuation shares the original attempt's budget. A separate,
// audited manual recovery is a new authorization and uses queueAction directly.
func (m *Monitor) queueRecoveryAttempt(at time.Time, emit func(string, *Action)) {
	previous, exists := m.latestRecovery()
	m.queueAction("intervention", "", 0, at, func(kind string, a *Action) {
		if m.ActionVersion == ActionFormatVersion && exists && a != nil && kind == "action_queued" && a.Attempt > 1 {
			a.QueuedAt, a.Deadline = previous.QueuedAt, previous.Deadline
			m.Actions[a.ID] = *a
		}
		emit(kind, a)
	})
}
