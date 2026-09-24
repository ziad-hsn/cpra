package runtimeconfig

import (
	"fmt"
	"time"
)

// ManualRecovery bounds operator-requested recovery in addition to the existing
// per-incident automatic attempt budget. Zero fields retain compatible defaults.
type ManualRecovery struct {
	MinimumInterval time.Duration `yaml:"minimum_interval" json:"minimum_interval"`
	PerHour         int           `yaml:"per_hour" json:"per_hour"`
}

func (c ManualRecovery) Effective() ManualRecovery {
	if c.MinimumInterval == 0 {
		c.MinimumInterval = time.Minute
	}
	if c.PerHour == 0 {
		c.PerHour = 3
	}
	return c
}
func (c ManualRecovery) Validate() error {
	c = c.Effective()
	if c.MinimumInterval < time.Second || c.MinimumInterval > time.Hour || c.PerHour < 1 || c.PerHour > 100 {
		return fmt.Errorf("manual_recovery requires minimum_interval between 1s and 1h and per_hour between 1 and 100")
	}
	return nil
}
