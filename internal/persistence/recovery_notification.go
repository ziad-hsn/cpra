package persistence

import (
	"errors"
	"math"
	"time"
)

const RecoveryNotificationFormatVersion = 33
const recoveryNotificationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-33\n"

const (
	NotifyRecoveryExhausted = "recovery_exhausted"
	NotifyRecoveryFailed    = "recovery_failed"
)

var ErrRecoveryNotificationInvalid = errors.New("invalid recovery notification")

// RecoveryFailure pins the confirmed result that caused a notification. It
// contains no provider response, target, credentials or executable state.
type RecoveryFailure struct {
	ActionID    string    `json:"action_id"`
	Driver      string    `json:"driver"`
	Attempt     int       `json:"attempt"`
	MaxAttempts int       `json:"max_attempts"`
	Trigger     string    `json:"trigger"`
	FailedAt    time.Time `json:"failed_at"`
}

func recoveryNotificationStorageFormat(version int) bool {
	return version == RecoveryNotificationFormatVersion || agentObservationStorageFormat(version) || version > RecoveryNotificationFormatVersion && externalStorageFormat(version)
}

func recoveryNotificationPolicyFormat(p Policy) int {
	if len(p.NotificationTriggers) != 0 || p.RecoveryDriver != "" {
		return RecoveryNotificationFormatVersion
	}
	return FormatVersion
}

func validateRecoveryNotificationPolicy(p Policy) error {
	if p.RecoveryDriver != "" && (!p.Intervention || !catalogIdentifier(p.RecoveryDriver, 128)) {
		return ErrRecoveryNotificationInvalid
	}
	for color, trigger := range p.NotificationTriggers {
		if color != "red" || !p.Intervention || !catalogIdentifier(p.RecoveryDriver, 128) ||
			(trigger != NotifyRecoveryExhausted && trigger != NotifyRecoveryFailed) {
			return ErrRecoveryNotificationInvalid
		}
	}
	return nil
}

func (v RecoveryFailure) validate() error {
	if !catalogIdentifier(v.ActionID, 256) || !catalogIdentifier(v.Driver, 128) || v.Attempt < 1 || v.Attempt > math.MaxInt32 ||
		v.MaxAttempts < 1 || v.MaxAttempts > math.MaxInt32 || v.FailedAt.IsZero() ||
		(v.Trigger != NotifyRecoveryExhausted && v.Trigger != NotifyRecoveryFailed) ||
		(v.Trigger == NotifyRecoveryExhausted && v.Attempt < v.MaxAttempts) {
		return ErrRecoveryNotificationInvalid
	}
	return nil
}

func validateRecoveryNotificationAction(version int, a Action) error {
	if a.RecoveryFailure == nil {
		return nil
	}
	v := a.RecoveryFailure
	if !recoveryNotificationStorageFormat(version) || a.Kind != "code" || a.Color != "red" ||
		a.IncidentID == "" || a.ID == v.ActionID || a.NotBefore.Before(v.FailedAt) {
		return ErrRecoveryNotificationInvalid
	}
	return v.validate()
}

func (m *Monitor) queueRecoveryFailure(action Action, at time.Time, emit func(string, *Action)) {
	// Only the original committed intervention result can create this intent.
	// Replayed results, notifications, operator reviews and late evidence cannot.
	if action.Kind != "intervention" || action.State != Failed || !m.OwnsAction(action) ||
		action.Revision != m.Revision || action.IncidentID == "" || action.IncidentID != m.notificationIncident() {
		return
	}
	trigger := m.Policy.NotificationTriggers["red"]
	if trigger == "" || trigger == NotifyRecoveryExhausted && action.Attempt < m.Policy.recoveryLimit() {
		return
	}
	failure := RecoveryFailure{ActionID: action.ID, Driver: m.Policy.RecoveryDriver, Attempt: action.Attempt, MaxAttempts: m.Policy.recoveryLimit(),
		Trigger: trigger, FailedAt: action.FinishedAt}
	m.queueNotification("red", at, &failure, emit)
}
