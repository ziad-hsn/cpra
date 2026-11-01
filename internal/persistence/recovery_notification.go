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
	TerminationReason string    `json:"termination_reason,omitempty"`
	ActionID          string    `json:"action_id"`
	Driver            string    `json:"driver"`
	Attempt           int       `json:"attempt"`
	MaxAttempts       int       `json:"max_attempts"`
	Trigger           string    `json:"trigger"`
	FailedAt          time.Time `json:"failed_at"`
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
		if color != "red" || !p.Intervention || p.RecoveryRules.IsZero() && !catalogIdentifier(p.RecoveryDriver, 128) ||
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
		(v.Trigger == NotifyRecoveryExhausted && v.Attempt < v.MaxAttempts && v.TerminationReason != "permanent_rejection" && v.TerminationReason != "execution_deadline") ||
		(v.TerminationReason != "" && v.TerminationReason != "attempt_limit" && v.TerminationReason != "execution_deadline" && v.TerminationReason != "permanent_rejection") {
		return ErrRecoveryNotificationInvalid
	}
	return nil
}

func validateRecoveryNotificationAction(version int, a Action) error {
	if a.RecoveryFailure == nil {
		return nil
	}
	v := a.RecoveryFailure
	if v.TerminationReason != "" && !notificationConditionStorageFormat(version) {
		return ErrRecoveryNotificationInvalid
	}
	if !recoveryNotificationStorageFormat(version) || a.Kind != "code" || a.Color != "red" ||
		a.IncidentID == "" || a.ID == v.ActionID || a.NotBefore.Before(v.FailedAt) {
		return ErrRecoveryNotificationInvalid
	}
	return v.validate()
}

func (m *Monitor) queueRecoveryFailure(action Action, at time.Time, emit func(string, *Action)) {
	// Only the original committed intervention result can create this intent.
	// Replayed results, notifications, operator reviews and late evidence cannot.
	if action.Kind != "intervention" || action.State != Failed && !(m.notificationConditionsEnabled() && action.State == Cancelled && action.TerminationReason == "execution_deadline") || !m.OwnsAction(action) ||
		action.Revision != m.Revision || action.IncidentID == "" || action.IncidentID != m.notificationIncident() {
		return
	}
	trigger := m.Policy.NotificationTriggers["red"]
	if trigger == "" || trigger == NotifyRecoveryExhausted && action.Attempt < m.Policy.recoveryLimit() && (!m.notificationConditionsEnabled() || action.TerminationReason == "") {
		return
	}
	driver := m.Policy.RecoveryDriver
	if action.Rule != (RecoverySelection{}) {
		driver = action.Rule.Target.Driver
	}
	failure := RecoveryFailure{ActionID: action.ID, Driver: driver, Attempt: action.Attempt, MaxAttempts: m.Policy.recoveryLimit(),
		Trigger: trigger, FailedAt: action.FinishedAt, TerminationReason: action.TerminationReason}
	m.queueNotification("red", at, &failure, emit)
}

// A delayed observation can discover budget exhaustion after the definite result
// was committed. Record that policy conclusion without changing provider facts.
func (m *Monitor) assessRecoveryTermination(at time.Time, emit func(string, *Action)) {
	if !m.notificationConditionsEnabled() {
		return
	}
	a, ok := m.latestRecovery()
	if !ok || a.Kind != "intervention" || !m.OwnsAction(a) || a.IncidentID != m.IncidentID || a.Revision != m.Revision {
		return
	}
	if a.State == Cancelled && a.TerminationReason == "execution_deadline" {
		m.queueRecoveryFailure(a, at, emit)
		return
	}
	if a.State != Failed || a.TerminationReason != "" {
		return
	}
	if a.Deadline.IsZero() || at.Before(a.Deadline) {
		return
	}
	a.TerminationReason = "execution_deadline"
	if m.Actions == nil {
		m.Actions = make(map[string]Action)
	}
	m.Actions[a.ID] = a
	emit("recovery_exhausted", &a)
	m.queueRecoveryFailure(a, at, emit)
}
