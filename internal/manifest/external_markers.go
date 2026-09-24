//go:build externaljobs

package manifest

// ExternalPulseConfig carries only a binding key. It is not an executable job.
type ExternalPulseConfig struct{ Key ExternalRuntimeKey }

func (*ExternalPulseConfig) isPulseConfigs() {}
func (c *ExternalPulseConfig) Copy() PulseConfig {
	if c == nil {
		return (*ExternalPulseConfig)(nil)
	}
	out := *c
	return &out
}

// ExternalInterventionConfig carries a recovery slot's binding key.
type ExternalInterventionConfig struct{ Key ExternalRuntimeKey }

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
