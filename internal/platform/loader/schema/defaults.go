package schema

import "time"

// Defaults holds default values for monitor configuration.
type Defaults struct {
	Interval           time.Duration
	Timeout            time.Duration
	UnhealthyThreshold int
	HealthyThreshold   int
	Retries            int
	Enabled            bool
}

// DefaultConfig provides sensible defaults for monitors.
var DefaultConfig = Defaults{
	Interval:           30 * time.Second,
	Timeout:            5 * time.Second,
	UnhealthyThreshold: 3,
	HealthyThreshold:   1,
	Retries:            2,
	Enabled:            true,
}

// Apply fills zero values in a monitor with defaults.
func (d *Defaults) Apply(m *Monitor) {
	if m.Pulse.Interval == 0 {
		m.Pulse.Interval = d.Interval
	}
	if m.Pulse.Timeout == 0 {
		m.Pulse.Timeout = d.Timeout
	}
	if m.Pulse.UnhealthyThreshold == 0 {
		m.Pulse.UnhealthyThreshold = d.UnhealthyThreshold
	}
	if m.Pulse.HealthyThreshold == 0 {
		m.Pulse.HealthyThreshold = d.HealthyThreshold
	}
}
