package management

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestIncidentObservationUsesUTCAndOmitsUnavailableTimes(t *testing.T) {
	local := time.Date(2026, 10, 20, 10, 0, 0, 0, time.FixedZone("fixture", 3*60*60))
	r := persistence.IncidentRecord{ID: "episode", MonitorID: "monitor", Revision: "revision", FirstFailureAt: local,
		NotificationsAvailable: true, Notifications: persistence.NotificationCounts{Unknown: 1}}
	view := incidentView(r)
	if view.State != "pending" || !view.FirstFailureAt.Equal(local) || view.FirstFailureAt.Location() != time.UTC {
		t.Fatal("observation invented an active incident or changed its timestamp", view)
	}
	raw, err := json.Marshal(view)
	if err != nil || strings.Contains(string(raw), "0001-") || strings.Contains(string(raw), "openedAt") || strings.Contains(string(raw), "closedAt") || !strings.Contains(string(raw), "2026-10-20T07:00:00Z") {
		t.Fatal("unavailable times or local offsets leaked into the wire observation", string(raw), err)
	}
	if view.Notifications == nil || view.Notifications.Unknown != 1 || view.Notifications.Accepted != 0 {
		t.Fatal("unknown notification was inferred as delivered")
	}
}

func TestOlderIncidentControlResponseMatchesActivityWithoutInventingTime(t *testing.T) {
	m := persistence.Monitor{ID: "monitor", IncidentID: "episode", Recovering: true, ActionVersion: persistence.ActionFormatVersion}
	view := incidentFromMonitor(m)
	if view.State != "open" || !view.OpenedAt.IsZero() || view.Notifications != nil {
		t.Fatal("older ongoing episode changed meaning in control response", view)
	}
}

func TestActionObservationPreservesOriginalAuthorizationTimes(t *testing.T) {
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.FixedZone("fixture", 3*60*60))
	a := persistence.Action{ID: "action", QueuedAt: at, NotBefore: at.Add(time.Second), Deadline: at.Add(45 * time.Second), StartedAt: at.Add(3 * time.Second)}
	view := actionView(persistence.ActionRecord{Action: a})
	if view.QueuedAt == nil || view.Deadline == nil || view.StartedAt == nil || view.FinishedAt != nil || !view.QueuedAt.Equal(at) || view.QueuedAt.Location() != time.UTC || !view.Deadline.Equal(a.Deadline) {
		t.Fatal("authorization times changed or unavailable completion invented", view)
	}
	raw, err := json.Marshal(view)
	if err != nil || strings.Contains(string(raw), "0001-") || strings.Contains(string(raw), "finishedAt") {
		t.Fatal(string(raw), err)
	}
}
