package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NotificationSequence is private admission bookkeeping, never a metric label.
// A sequence spans opening/update/resolution and safe attempts for that remote
// incident. Old provider keys keep their original serialization domain.
func (d *Dispatch) NotificationSequence() string {
	if d.Kind != "code" {
		return ""
	}
	incarnation := d.CatalogUID
	if incarnation == "" {
		incarnation = d.FileIncarnation
	}
	incident := d.NotificationCorrelation
	if incident == "" {
		incident = d.IncidentID
	}
	if d.ControlChanged {
		incident = "control"
	}
	if d.ReviewNotification != nil {
		incident = "review/" + d.ReviewNotification.AlertID
	}
	if incident == "" {
		return ""
	} // Missing historical identity remains serialized.
	key := strings.Join([]string{d.Driver, d.MonitorID, incarnation, incident}, "\x00")
	job := d.Job
	for range 8 {
		switch target := job.(type) {
		case *CodePagerDutyJob:
			if d.ProviderKey != "" {
				key = "pagerduty/" + d.ProviderKey
			} else if d.ProviderCorrelationVersion == 0 {
				hash := sha256.Sum256([]byte(strings.Join([]string{d.MonitorID, d.CatalogUID, incident, d.EndpointID}, "\x00")))
				key = "pagerduty/cpra:" + hex.EncodeToString(hash[:])
			}
			job = nil
		case *CodeVictorOpsJob:
			if d.ProviderCorrelationVersion == 0 {
				key = "victorops/" + target.EntityID
			}
			job = nil
		case interface{ Unwrap() Job }:
			job = target.Unwrap()
			continue
		}
		break
	}
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}
