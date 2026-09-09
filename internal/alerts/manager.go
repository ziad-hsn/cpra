package alerts

import (
	"time"

	"cpra/internal/controller/components"

	"github.com/mlange-42/ark/ecs"
)

// Manager coordinates alert dispatch decisions and stamps per-color status on
// the ECS world. It implements Policy by delegating to its configured policy,
// and adds the state-mutating helpers used by systems.
//
// All Manager methods are expected to be called from within ECS system ticks;
// they must not be invoked from goroutines outside systems, so that all world
// mutations stay inside the ECS loop.
type Manager struct {
	policy       Policy
	statusMapper *ecs.Map1[components.CodeStatus]
	cooldown     time.Duration
}

// NewManager creates a Manager using the given policy and cooldown window.
func NewManager(world *ecs.World, policy Policy, cooldown time.Duration) *Manager {
	return &Manager{
		policy:       policy,
		statusMapper: ecs.NewMap1[components.CodeStatus](world),
		cooldown:     cooldown,
	}
}

// ShouldDispatch implements Policy by delegating to the configured policy.
func (m *Manager) ShouldDispatch(ent ecs.Entity, color string, now time.Time) (bool, string) {
	return m.policy.ShouldDispatch(ent, color, now)
}

// Request evaluates whether an alert may be dispatched immediately. When it
// may, immediate is true and notBefore is the zero time. When it may not
// (cooldown active), immediate is false and notBefore is the earliest time the
// alert may be released; the caller should defer the alert until then.
func (m *Manager) Request(ent ecs.Entity, color string, now time.Time) (immediate bool, notBefore time.Time, reason string) {
	ok, reason := m.policy.ShouldDispatch(ent, color, now)
	if ok {
		return true, time.Time{}, reason
	}

	status := m.statusMapper.Get(ent)
	if status != nil {
		if cs := status.Status[color]; cs != nil && !cs.NotBefore.IsZero() {
			return false, cs.NotBefore, reason
		}
	}
	return false, now.Add(m.cooldown), reason
}

// OnEnqueued stamps the per-color status after an alert has been enqueued,
// starting the cooldown window.
func (m *Manager) OnEnqueued(ent ecs.Entity, color string, now time.Time) {
	status := m.statusMapper.Get(ent)
	if status == nil {
		return
	}
	cs := status.Status[color]
	if cs == nil {
		return
	}
	cs.NotBefore = now.Add(m.cooldown)
	cs.LastAlertTime = now
}

// OnResult updates the per-color status after an alert job completes. On
// failure the cooldown is cleared so a retry is not suppressed.
func (m *Manager) OnResult(ent ecs.Entity, color string, err error, now time.Time) {
	status := m.statusMapper.Get(ent)
	if status == nil {
		return
	}
	cs := status.Status[color]
	if cs == nil {
		return
	}
	if err != nil {
		cs.SetFailure(err)
		cs.NotBefore = time.Time{}
	} else {
		cs.SetSuccess(now)
	}
}
