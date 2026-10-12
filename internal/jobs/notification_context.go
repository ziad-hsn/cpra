package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func bindNotificationContext(job Job, dispatch *Dispatch) {
	if dispatch.Kind != "code" {
		return
	}
	incident := dispatch.NotificationCorrelation
	if incident == "" {
		incident = dispatch.IncidentID
	}
	if incident == "" || dispatch.EndpointID == "" {
		return
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{dispatch.MonitorID, dispatch.CatalogUID, incident, dispatch.EndpointID}, "\x00")))
	key := "cpra:" + hex.EncodeToString(digest[:])
	for range 8 {
		switch target := job.(type) {
		case *CodePagerDutyJob:
			target.CorrelationKey = key
			return
		case *CodeOpsgenieJob:
			target.CorrelationKey = key
			return
		case interface{ Unwrap() Job }:
			job = target.Unwrap()
		default:
			return
		}
	}
}
