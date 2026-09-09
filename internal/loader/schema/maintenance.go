package schema

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

type CompiledWindow struct {
	Schedule cron.Schedule
	Duration time.Duration
}

func CompileMaintenance(windows []MaintenanceWindow) ([]CompiledWindow, error) {
	out := make([]CompiledWindow, 0, len(windows))
	for _, w := range windows {
		d, err := time.ParseDuration(w.Duration)
		if err != nil || d <= 0 || d > 366*24*time.Hour {
			return nil, fmt.Errorf("maintenance duration must be positive and at most 366 days")
		}
		zone := w.Timezone
		if zone == "" {
			zone = "UTC"
		}
		if _, err := time.LoadLocation(zone); err != nil {
			return nil, fmt.Errorf("invalid maintenance timezone %q", zone)
		}
		if len(strings.Fields(w.Cron)) != 5 {
			return nil, fmt.Errorf("maintenance cron requires five fields")
		}
		schedule, err := cron.ParseStandard("CRON_TZ=" + zone + " " + w.Cron)
		if err != nil {
			return nil, fmt.Errorf("invalid maintenance cron: %w", err)
		}
		out = append(out, CompiledWindow{schedule, d})
	}
	return out, nil
}
func InMaintenance(windows []CompiledWindow, now time.Time) bool {
	for _, w := range windows {
		// Any start in (now-duration, now] means the window is active. This also
		// works for overlaps and starts before process startup, without scanning.
		start := w.Schedule.Next(now.Add(-w.Duration))
		if !start.IsZero() && !start.After(now) {
			return true
		}
	}
	return false
}
