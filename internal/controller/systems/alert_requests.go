package systems

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/alerts"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"time"
)

func requestCode(ent ecs.Entity, state *components.MonitorState, color string, cfg *components.CodeConfig, sched *CodeScheduler, mgr *alerts.Manager) {
	if cfg == nil || sched == nil || cfg.Configs[color] == nil || !cfg.Configs[color].Dispatch {
		return
	}
	now := time.Now()
	// Maintenance suppresses events, including recovery notices, at trigger and
	// dispatch. Checks continue; automatic interventions are suppressed as well.
	if manifest.InMaintenance(state.Maintenance, now) {
		return
	}
	for _, request := range state.PendingAlerts {
		if request.Color == color {
			return
		}
	}
	before := time.Time{}
	if mgr != nil {
		_, before, _ = mgr.Request(ent, color, now)
	}
	state.PendingAlerts = append(state.PendingAlerts, components.AlertRequest{Color: color, NotBefore: before})
	state.PendingCode = state.PendingAlerts[0].Color
	state.SetCodeNeeded(true)
	sched.EnqueueReady([]ecs.Entity{ent})
}
