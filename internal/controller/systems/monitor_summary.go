package systems

import (
	"github.com/ziad-hsn/cpra/internal/controller/components"
)

// classifyStatus derives a dashboard status string from monitor state flags.
func classifyStatus(state *components.MonitorState) string {
	if state.Flags&components.StateVerifying != 0 {
		return "verifying"
	}
	if state.Flags&components.StateIncidentOpen != 0 {
		return "incident"
	}
	if state.PulseFailures > 0 || state.LastError != nil {
		return "down"
	}
	if state.LastSuccessTime.IsZero() {
		return "unknown"
	}
	if state.PulseWarning != "" {
		return "degraded"
	}
	return "up"
}

// pulseTarget reads the display projection captured before credential resolution.
// Resolved job configuration must never be used to construct public responses.
func pulseTarget(cfg *components.PulseConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.TargetSummary
}
