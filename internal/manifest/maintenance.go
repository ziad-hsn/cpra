package manifest

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type CompiledWindow struct {
	Schedule cron.Schedule
	Duration time.Duration
}

func CompileMaintenance(windows []MaintenanceWindow) ([]CompiledWindow, error) {
	out := make([]CompiledWindow, 0, len(windows))
	for index, w := range windows {
		d, err := time.ParseDuration(w.Duration)
		if err != nil || d <= 0 || d > 366*24*time.Hour {
			return nil, api.InvalidField(fmt.Sprintf("maintenance[%d].duration", index), "maintenanceDuration")
		}
		zone := w.Timezone
		if zone == "" {
			zone = "UTC"
		}
		if _, err := time.LoadLocation(zone); err != nil {
			return nil, api.InvalidField(fmt.Sprintf("maintenance[%d].timezone", index), "timezone")
		}
		if len(strings.Fields(w.Cron)) != 5 {
			return nil, api.InvalidField(fmt.Sprintf("maintenance[%d].cron", index), "cron")
		}
		schedule, err := cron.ParseStandard("CRON_TZ=" + zone + " " + w.Cron)
		if err != nil {
			return nil, api.InvalidField(fmt.Sprintf("maintenance[%d].cron", index), "cron")
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

// MaintenanceBetween includes a window that covered any part of a queued
// delivery's lifetime, even if the owner was stopped for that entire window.
func MaintenanceBetween(windows []CompiledWindow, from, through time.Time) bool {
	if from.IsZero() || through.Before(from) {
		return false
	}
	if InMaintenance(windows, from) {
		return true
	}
	for _, window := range windows {
		start := window.Schedule.Next(from)
		if !start.IsZero() && !start.After(through) {
			return true
		}
	}
	return false
}
