package systems

import (
	"errors"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestMonitorAvailabilityPreservesRealObservationDuringPreparationFailure(t *testing.T) {
	world := ecs.NewWorld()
	ent := releaseMonitor(t, &world, 1, 1)
	system := newIncidentSystem(t, &world, nil, nil)
	healthy := applyIncidentPulse(t, system, ent, jobs.Result{})
	applyIncidentPulse(t, system, ent, jobs.Result{Err: &jobs.ConfigurationError{Code: "ca_file_missing"}})
	row, _ := system.index.Get(ent.ID())
	if row.Health != "unknown" || row.Status != "unknown" || row.LastObservedHealth != "healthy" || !row.LastObservedAt.Equal(healthy.LastCheck) || !row.LastCheck.Equal(healthy.LastCheck) {
		t.Fatal("preparation replaced or misrepresented a real observation", row)
	}
	if row.Availability.State != "unavailable" || row.Availability.Reason != "configurationProblem" || row.Availability.ObservedAt.IsZero() {
		t.Fatal("preparation availability not explicit", row.Availability)
	}
	counts := system.index.Overview()
	if counts.Unavailable != 1 || counts.ByStatus["up"] != 0 || counts.Pending != 0 {
		t.Fatal("unavailable check counted operational or not yet observed", counts)
	}
	applyIncidentPulse(t, system, ent, jobs.Result{})
	row, _ = system.index.Get(ent.ID())
	if row.Health != "healthy" || row.Availability.State != "available" || row.ConfigurationProblem.Code != "" || system.index.Overview().Unavailable != 0 {
		t.Fatal("actual successful check did not clear preparation unavailability", row)
	}
}

func TestMonitorMaintenanceProjectionAdvancesWithoutNewChecks(t *testing.T) {
	world := ecs.NewWorld()
	ent := releaseMonitor(t, &world, 1, 1)
	system := newIncidentSystem(t, &world, nil, nil)
	m := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("target failed")})
	windows, err := manifest.CompileMaintenance([]manifest.MaintenanceWindow{{Cron: "0 * * * *", Duration: "5m", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	system.states.Get(ent).Maintenance = windows
	before := time.Date(2026, 11, 1, 9, 59, 0, 0, time.UTC)
	system.index.Put(system.summaryAt(ent, m, before))
	if system.index.Overview().UnsuppressedUnhealthy != 1 {
		t.Fatal("pre-window outage missing")
	}
	start := before.Add(time.Minute)
	system.refreshMonitorAvailability(start)
	row, _ := system.index.Get(ent.ID())
	counts := system.index.Overview()
	if !row.Maintenance || !row.NotificationsSuppressed || row.Health != "unhealthy" || row.Availability.State != "available" || counts.Maintenance != 1 || counts.SuppressedUnhealthy != 1 || counts.UnsuppressedUnhealthy != 0 || !row.LastCheck.Equal(m.LastCheck) {
		t.Fatal("maintenance was hidden or fabricated a check", row, counts)
	}
	end := start.Add(5 * time.Minute)
	system.refreshMonitorAvailability(end)
	row, _ = system.index.Get(ent.ID())
	if row.Maintenance || row.NotificationsSuppressed || !row.SuppressionObservedAt.Equal(end) || system.index.Overview().UnsuppressedUnhealthy != 1 {
		t.Fatal("maintenance did not end at owner deadline", row)
	}
	if system.observationDeadlines.Len() != 1 {
		t.Fatal("recurring windows accumulated per-monitor deadlines")
	}
}

func TestMonitorAvailabilityPausedAndPendingAreDistinct(t *testing.T) {
	world := ecs.NewWorld()
	ent := releaseMonitor(t, &world, 1, 1)
	system := newIncidentSystem(t, &world, nil, nil)
	m, _ := system.store.Get(system.states.Get(ent).MonitorID)
	at := time.Now().UTC()
	row := system.summaryAt(ent, m, at)
	if row.Availability.State != "pending" || row.Availability.Reason != "notObserved" {
		t.Fatal(row.Availability)
	}
	m.Policy.Enabled = false
	row = system.summaryAt(ent, m, at)
	if row.Availability.State != "paused" || row.Availability.Reason != "disabled" || !row.NotificationsSuppressed {
		t.Fatal(row.Availability)
	}
	m.Policy.Enabled, m.SnoozedUntil = true, at.Add(-time.Second)
	row = system.summaryAt(ent, m, at)
	if row.Availability.State != "paused" || row.Availability.Reason != "snoozed" {
		t.Fatal("wall clock implicitly cleared committed snooze", row.Availability)
	}
}

func TestMaintenanceBoundaryIncludesAbsoluteWindowEnd(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	windows := []persistence.MaintenanceWindow{{Start: at, End: at.Add(time.Minute)}}
	if got := nextMaintenanceBoundary(windows, nil, at); !got.Equal(windows[0].End) {
		t.Fatal(got)
	}
	if got := nextMaintenanceBoundary(windows, nil, windows[0].End); !got.IsZero() {
		t.Fatal("expired boundary retained", got)
	}
}
