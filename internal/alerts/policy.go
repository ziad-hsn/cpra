// Package alerts provides a composable alert policy and manager layer that
// enables debounce/cooldown, suppression, and recovery overrides without
// modifying job transports.
//
// The package is intentionally small: systems depend only on the Policy-shaped
// surface, while the Manager adds the state-stamping helpers used by systems.
package alerts

import (
	"time"

	"cpra/internal/controller/components"

	"github.com/mlange-42/ark/ecs"
)

// Policy decides whether an alert of the given color should be dispatched for
// an entity at the given time. It returns a boolean decision and a
// human-readable reason for observability.
type Policy interface {
	ShouldDispatch(ent ecs.Entity, color string, now time.Time) (bool, string)
}

// DefaultPolicy is the default alert policy. It enforces a per-color cooldown
// window (read from the entity's CodeStatus.NotBefore) and, when enabled,
// bypasses the cooldown for recovery colors so that "green" recovery notices
// are always dispatched immediately.
type DefaultPolicy struct {
	statusMapper   *ecs.Map1[components.CodeStatus]
	recoveryBypass bool
}

// NewDefaultPolicy creates a DefaultPolicy backed by the given world.
func NewDefaultPolicy(world *ecs.World, recoveryBypass bool) *DefaultPolicy {
	return &DefaultPolicy{
		statusMapper:   ecs.NewMap1[components.CodeStatus](world),
		recoveryBypass: recoveryBypass,
	}
}

// ShouldDispatch implements Policy.
func (p *DefaultPolicy) ShouldDispatch(ent ecs.Entity, color string, now time.Time) (bool, string) {
	if p.recoveryBypass && isRecoveryColor(color) {
		return true, "recovery bypass"
	}

	status := p.statusMapper.Get(ent)
	if status == nil {
		return true, "no code status"
	}
	cs := status.Status[color]
	if cs == nil {
		return true, "no status for color"
	}
	if !cs.NotBefore.IsZero() && now.Before(cs.NotBefore) {
		return false, "cooldown active until " + cs.NotBefore.Format(time.RFC3339)
	}
	return true, "cooldown elapsed"
}

// isRecoveryColor reports whether the color represents a recovery notice that
// should bypass cooldown suppression.
func isRecoveryColor(color string) bool {
	return color == "green"
}
