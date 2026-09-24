package runtimeconfig

import (
	"testing"
	"time"
)

func TestManualRecoveryLimitsDefaultAndValidation(t *testing.T) {
	if got := (ManualRecovery{}).Effective(); got.MinimumInterval != time.Minute || got.PerHour != 3 {
		t.Fatal(got)
	}
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []ManualRecovery{{MinimumInterval: -1}, {MinimumInterval: time.Millisecond}, {MinimumInterval: 2 * time.Hour}, {PerHour: -1}, {PerHour: 101}} {
		if err := c.Validate(); err == nil {
			t.Fatal("invalid recovery limits accepted", c)
		}
	}
	for _, c := range []ManualRecovery{{}, {MinimumInterval: time.Second, PerHour: 100}, {MinimumInterval: time.Hour, PerHour: 1}} {
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
