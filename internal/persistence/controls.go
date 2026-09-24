package persistence

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxSnoozeDuration = 30 * 24 * time.Hour

var (
	ErrControlConflict   = errors.New("monitor control or incident version changed")
	ErrIncidentNotActive = errors.New("incident is closed, replaced or unavailable")
	ErrControlInvalid    = errors.New("invalid monitor control request")
)

// ControlCommand is admitted with an authenticated actor and caller-generated
// next revision. It is independent of configuration and execution revisions.
type ControlCommand struct {
	Action           string    `json:"action"`
	MonitorUID       string    `json:"monitor_uid"`
	ExpectedRevision string    `json:"expected_revision"`
	Revision         string    `json:"revision"`
	OperationID      string    `json:"operation_id"`
	IncidentID       string    `json:"incident_id,omitempty"`
	Actor            string    `json:"actor"`
	Reason           string    `json:"reason,omitempty"`
	Note             string    `json:"note,omitempty"`
	Until            time.Time `json:"until,omitempty"`
}

func (c ControlCommand) Subject() string {
	if c.Action == "acknowledge" || c.Action == "dismiss" || c.Action == "reopen" {
		return "incident"
	}
	return "control"
}

func controlText(s string) bool {
	return len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r")
}

func (c ControlCommand) validate(at time.Time) error {
	if !catalogIdentifier(c.MonitorUID, 256) || !catalogIdentifier(c.ExpectedRevision, 256) || !catalogIdentifier(c.Revision, 256) ||
		!validOperationIdentity(c.OperationID, c.Revision) || c.Revision == c.ExpectedRevision || !catalogIdentifier(c.Actor, 128) || !controlText(c.Reason) || !controlText(c.Note) {
		return ErrControlInvalid
	}
	switch c.Action {
	case "acknowledge", "dismiss", "reopen":
		if !catalogIdentifier(c.IncidentID, 256) || !c.Until.IsZero() {
			return ErrControlInvalid
		}
		if c.Action == "dismiss" && strings.TrimSpace(c.Reason) == "" {
			return ErrControlInvalid
		}
	case "snooze":
		if c.IncidentID != "" || strings.TrimSpace(c.Reason) == "" || !c.Until.After(at) || c.Until.Sub(at) > MaxSnoozeDuration {
			return ErrControlInvalid
		}
	case "unsnooze":
		if c.IncidentID != "" || !c.Until.IsZero() {
			return ErrControlInvalid
		}
	case "expire_snooze":
		if c.IncidentID != "" || c.Actor != "system" || c.Until.IsZero() || at.Before(c.Until) {
			return ErrControlInvalid
		}
	default:
		return ErrControlInvalid
	}
	return nil
}

// MonitorStatus is an observation projection with no action maps, provider
// configuration, actor notes or other large per-monitor allocations.
type MonitorStatus struct {
	ObservedGeneration                                                uint64
	ID, CatalogUID, CatalogRevision, Revision                         string
	ControlRevision, IncidentID, IncidentRevision                     string
	SnoozedUntil, LastCheck, LastSuccess, NextCheck                   time.Time
	Enabled, Removed, Incident, Recovering, Warning, LatencyAvailable bool
	Generation                                                        uint64
	TotalChecks, SuccessfulChecks                                     uint64
	LastOutcome                                                       string
	LastLatency                                                       time.Duration
	UnknownActions                                                    int
}

// IncidentRecord describes the active or latest closed incident for a monitor.
// Earlier incidents remain in retained event history.
type IncidentRecord struct {
	ID, MonitorID, MonitorUID, Revision string
	Active                              bool
	OpenedAt, ClosedAt                  time.Time
	AcknowledgedBy                      string
	AcknowledgedAt                      time.Time
	AcknowledgedNote                    string
	Dismissed                           bool
	DismissedBy                         string
	DismissedAt                         time.Time
	DismissedReason                     string
}
