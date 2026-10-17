package persistence

import (
	"errors"
	"time"
)

// LifecycleFormatVersion separates observed health from held external effects.
// Earlier envelopes retain their historical transition rules during replay.
const LifecycleFormatVersion = 42

const lifecycleSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-42\n"

func lifecycleStorageFormat(version int) bool {
	return version == LifecycleFormatVersion || version == 43 && externalStorageFormat(version)
}

func requiresLifecycleFormat(m Monitor) bool {
	if m.LastFailure.requiresLifecycleFormat() {
		return true
	}
	if m.Policy.RecoveryTimeout != 0 || m.Policy.PeriodicMaintenance || m.EnabledControl != "" || m.Policy.AutoIgnore != nil || m.Ignored || !m.IgnoredAt.IsZero() || m.IgnoredBy != "" || m.IgnoreReason != "" || m.IgnoreCause != "" || m.IgnoreHealthyChecks != 0 || len(m.IgnoreIncidents) != 0 || m.IgnoreCountedIncident != "" {
		return true
	}
	if m.ProviderCorrelationVersion != 0 || m.LifecycleVersion != 0 || m.FileIncarnation != "" || m.NotificationAttempted != "" || !m.RecoveryCheckAfter.IsZero() {
		return true
	}
	for _, a := range m.Actions {
		if a.Cause.requiresLifecycleFormat() || a.Failure.requiresLifecycleFormat() {
			return true
		}
		if a.ProviderCorrelationVersion != 0 || a.FileIncarnation != "" || a.Control != nil {
			return true
		}
	}
	return false
}

// upgradeLifecycle runs only in a committed transition. Known starts can
// establish exposure, including a retry whose previous start was committed.
// An initial queued attempt and guesses about provider success cannot.
func (m *Monitor) upgradeLifecycle() {
	if m.LifecycleVersion == LifecycleFormatVersion {
		return
	}
	m.LifecycleVersion = LifecycleFormatVersion
	for _, a := range m.Actions {
		if m.OwnsAction(a) && a.Kind == "code" &&
			(a.Color == "yellow" || a.Color == "red" || a.Color == "cyan") &&
			(!a.StartedAt.IsZero() || a.Attempt > 1) && a.NotificationCorrelation != "" && a.NotificationCorrelation == m.NotificationCorrelation {
			m.NotificationAttempted = a.NotificationCorrelation
		}
	}
	// An exposed pre-42 episode keeps its original remote incident identity.
	// Only unsent episodes can adopt the new destination-based correlation.
	if m.NotificationAttempted == "" {
		m.ProviderCorrelationVersion = 1
		for id, a := range m.Actions {
			if m.OwnsAction(a) && a.Kind == "code" && a.State == Queued && a.NotificationCorrelation == m.NotificationCorrelation {
				a.ProviderCorrelationVersion = 1
				m.Actions[id] = a
			}
		}
	}

}

func validateLifecycle(m Monitor, version int) error {
	if !requiresLifecycleFormat(m) {
		return nil
	}
	if !lifecycleStorageFormat(version) || m.LifecycleVersion != LifecycleFormatVersion ||
		m.ProviderCorrelationVersion < 0 || m.ProviderCorrelationVersion > 1 ||
		m.FileIncarnation != "" && !catalogIdentifier(m.FileIncarnation, 256) ||
		m.NotificationAttempted != "" && !catalogIdentifier(m.NotificationAttempted, 256) ||
		!m.RecoveryCheckAfter.IsZero() && (m.RecoveryCheckAfter.Year() < 1 || m.RecoveryCheckAfter.Year() > 9999) {
		return errors.New("invalid monitor lifecycle state")
	}
	if err := m.validateIgnore(); err != nil {
		return err
	}
	for _, a := range m.Actions {
		if a.ProviderCorrelationVersion < 0 || a.ProviderCorrelationVersion > 1 || a.ProviderCorrelationVersion != 0 && a.Kind != "code" {
			return errors.New("invalid provider correlation version")
		}
		if a.Control != nil && (a.Kind != "code" || a.Color != "gray" || !catalogIdentifier(a.Control.Revision, 256) || a.Control.At.IsZero() || a.Control.At.Year() < 1 || a.Control.At.Year() > 9999) {
			return errors.New("invalid control notification")
		}
		if a.FileIncarnation != "" && !catalogIdentifier(a.FileIncarnation, 256) ||
			(a.State == Queued || a.State == Started) && !m.OwnsAction(a) {
			return errors.New("invalid action incarnation")
		}
	}
	return nil
}

// OwnsAction distinguishes retained historical evidence from the active
// configuration, including remove/re-add in a file-configured installation.
func (m Monitor) OwnsAction(a Action) bool {
	return a.CatalogUID == m.CatalogUID && a.FileIncarnation == m.FileIncarnation
}

func (m Monitor) incarnationIdentity() string {
	if m.FileIncarnation != "" {
		return m.CatalogUID + "/" + m.FileIncarnation
	}
	return m.CatalogUID
}

func actionMonitorIdentity(monitorID string, a Action) string {
	if a.CatalogUID != "" {
		return a.CatalogUID
	}
	if a.FileIncarnation != "" {
		return a.FileIncarnation
	}
	return identity("file-original/" + monitorID)
}

// IncarnationID identifies the monitor instance that admitted this action,
// including the deterministic original identity used by older file stores.
func (a ActionRecord) IncarnationID() string { return actionMonitorIdentity(a.MonitorID, a.Action) }

// IncarnationID returns the same monitor identity used by admitted actions.
func (m Monitor) IncarnationID() string {
	return actionMonitorIdentity(m.ID, Action{CatalogUID: m.CatalogUID, FileIncarnation: m.FileIncarnation})
}

func (m Monitor) greenEligible() bool {
	return m.LifecycleVersion != LifecycleFormatVersion ||
		m.NotificationAttempted != "" && m.NotificationAttempted == m.NotificationCorrelation
}

func (m Monitor) currentIntervention(a Action) bool {
	return m.OwnsAction(a) && a.IncidentID == m.IncidentID &&
		m.Recovering && m.IncidentClosedAt.IsZero()
}

func (m Monitor) pendingCurrentIntervention() bool {
	for _, a := range m.Actions {
		if a.Kind == "intervention" && m.currentIntervention(a) && (a.State == Queued || a.State == Started) {
			return true
		}
	}
	return false
}

func (m *Monitor) cancelQueuedInterventions(at time.Time, outcome string, emit func(string, *Action)) {
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if m.OwnsAction(a) && a.Kind == "intervention" && a.State == Queued {
			a.State, a.Outcome, a.FinishedAt = Cancelled, outcome, at
			m.Actions[id] = a
			emit("action_cancelled", &a)
		}
	}
}

func (m *Monitor) setNotificationCorrelation(value string) {
	if m.NotificationCorrelation != value && m.LifecycleVersion == LifecycleFormatVersion {
		m.ProviderCorrelationVersion = 1
	}
	m.NotificationCorrelation = value
}
