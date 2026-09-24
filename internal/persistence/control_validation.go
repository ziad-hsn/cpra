package persistence

import "errors"

func validateMonitorControls(m Monitor) error {
	for _, id := range []string{m.CatalogRevision, m.ControlRevision, m.IncidentID, m.IncidentRevision} {
		if id != "" && !catalogIdentifier(id, 256) {
			return errors.New("invalid durable control identity")
		}
	}
	if !controlText(m.SnoozeReason) || !controlText(m.AcknowledgedNote) || !controlText(m.DismissedReason) {
		return errors.New("invalid durable control text")
	}
	for _, actor := range []string{m.SnoozedBy, m.AcknowledgedBy, m.DismissedBy} {
		if actor != "" && !catalogIdentifier(actor, 128) {
			return errors.New("invalid durable control actor")
		}
	}
	if m.SnoozedUntil.IsZero() != (m.SnoozedBy == "") || (!m.SnoozedUntil.IsZero() && (m.SnoozeReason == "" || m.ControlRevision == "")) {
		return errors.New("invalid durable snooze")
	}
	if m.IncidentID == "" {
		if m.IncidentRevision != "" || m.IncidentSequence != 0 || !m.IncidentOpenedAt.IsZero() || !m.IncidentClosedAt.IsZero() || m.AcknowledgedBy != "" || m.Dismissed {
			return errors.New("durable triage has no incident")
		}
	} else if m.IncidentRevision == "" || m.IncidentSequence == 0 {
		return errors.New("durable incident lacks its version")
	}
	if m.AcknowledgedAt.IsZero() != (m.AcknowledgedBy == "") {
		return errors.New("invalid durable acknowledgment")
	}
	if m.Dismissed != (m.DismissedBy != "") || m.Dismissed != (!m.DismissedAt.IsZero()) || (m.Dismissed && m.DismissedReason == "") {
		return errors.New("invalid durable dismissal")
	}
	for _, a := range m.Actions {
		if a.IncidentID != "" && !catalogIdentifier(a.IncidentID, 256) {
			return errors.New("invalid action incident identity")
		}
	}
	return nil
}
