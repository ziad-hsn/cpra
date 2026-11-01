//go:build externaljobs

package manifest

import (
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"time"
)

// ExternalPulseConfig carries only a binding key. It is not an executable job.
type ExternalPulseConfig struct {
	Key             ExternalRuntimeKey
	FailureContract *api.ExternalFailureContract `json:"-"`
}

func (*ExternalPulseConfig) isPulseConfigs() {}
func (c *ExternalPulseConfig) Copy() PulseConfig {
	if c == nil {
		return (*ExternalPulseConfig)(nil)
	}
	out := *c
	if c.FailureContract != nil {
		contract := *c.FailureContract
		contract.Codes = append([]string(nil), contract.Codes...)
		out.FailureContract = &contract
	}
	return &out
}

// ExternalInterventionConfig carries a recovery slot's binding key.
type ExternalInterventionConfig struct {
	Key    ExternalRuntimeKey
	Budget time.Duration `json:"-"`
}

func (*ExternalInterventionConfig) GetTargetType() string { return "external" }
func (c *ExternalInterventionConfig) Copy() InterventionTarget {
	if c == nil {
		return (*ExternalInterventionConfig)(nil)
	}
	out := *c
	return &out
}

// ExternalNotificationConfig carries a notification slot's binding key.
type ExternalNotificationConfig struct{ Key ExternalRuntimeKey }

func (*ExternalNotificationConfig) IsCodeNotification() {}
func (c *ExternalNotificationConfig) Copy() CodeNotification {
	if c == nil {
		return (*ExternalNotificationConfig)(nil)
	}
	out := *c
	return &out
}
