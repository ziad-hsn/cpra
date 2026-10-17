package persistence

import (
	"fmt"
	"time"
)

// ControlNotice identifies an enabled/disabled transition, never target health.
type ControlNotice struct {
	Revision string    `json:"revision"`
	Enabled  bool      `json:"enabled"`
	At       time.Time `json:"at"`
}

// ControlNotification grants only the enabled-state/maintenance/Ignore
// exceptions. Incarnation, configuration, snooze, deadline and start checks
// remain required at admission.
func (m Monitor) ControlNotification(a Action) bool {
	return m.LifecycleVersion == LifecycleFormatVersion && a.Kind == "code" && a.Color == "gray" && a.Control != nil &&
		m.OwnsAction(a) && a.Control.Revision == m.EnabledControl && a.Control.Enabled == m.Policy.Enabled
}

func (m *Monitor) queueEnabledNotice(at time.Time, emit func(string, *Action)) {
	m.EnabledControl = identity(fmt.Sprintf("enabled/%s/%s/%s/%t/%s", m.ID, m.incarnationIdentity(), m.Revision, m.Policy.Enabled, at.Format(time.RFC3339Nano)))
	if m.snoozed(at) {
		return
	}
	// This is a control transition, so ordinary severity cooldown, dismissal,
	// Ignore and maintenance suppression do not apply.
	for n := 0; n < m.Policy.Endpoints["gray"]; n++ {
		m.queueAction("code", "gray", n, at, func(kind string, action *Action) {
			if action != nil && kind == "action_queued" {
				action.ProviderCorrelationVersion = 1
				action.Control = &ControlNotice{Revision: m.EnabledControl, Enabled: m.Policy.Enabled, At: at}
				action.NotificationEpisode = m.EnabledControl
				action.IncidentID, action.NotificationCorrelation = "", m.EnabledControl
				m.Actions[action.ID] = *action
			}
			emit(kind, action)
		})
	}
}
