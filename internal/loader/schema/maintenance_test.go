package schema

import (
	"testing"
	"time"
)

func TestMaintenanceTimezoneAndStartupInsideWindow(t *testing.T) {
	windows, err := CompileMaintenance([]MaintenanceWindow{{Cron: "0 9 * * *", Duration: "2h", Timezone: "Africa/Cairo"}})
	if err != nil {
		t.Fatal(err)
	}
	zone, _ := time.LoadLocation("Africa/Cairo")
	start := time.Date(2026, 9, 8, 9, 0, 0, 0, zone)
	for _, tc := range []struct {
		offset time.Duration
		active bool
	}{{-time.Minute, false}, {0, true}, {90 * time.Minute, true}, {2 * time.Hour, false}} {
		if got := InMaintenance(windows, start.Add(tc.offset)); got != tc.active {
			t.Fatalf("offset %s active=%v", tc.offset, got)
		}
	}
}
func TestMaintenanceRejectsInvalidConfiguration(t *testing.T) {
	for _, w := range []MaintenanceWindow{{Cron: "bad", Duration: "1h"}, {Cron: "0 9 * * *", Duration: "-1h"}, {Cron: "0 9 * * *", Duration: "1h", Timezone: "invalid/zone"}} {
		if _, err := CompileMaintenance([]MaintenanceWindow{w}); err == nil {
			t.Fatal("accepted invalid maintenance")
		}
	}
}
