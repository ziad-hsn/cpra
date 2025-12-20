// Package controller provides the watchdog supervisor for CPRA system health monitoring.
//
// The Watchdog monitors all CPRA components (Controller, ECS App, Worker Pools, Queues)
// and logs health status. It runs as a standalone goroutine monitored by main via heartbeat.
//
// v1 is observation-only (logging). Future versions may add automatic restart capability.
package controller

import (
	"context"
	"sync/atomic"
	"time"

	"cpra/internal/queue"

	"github.com/mlange-42/ark-tools/resource"
	"github.com/mlange-42/ark/ecs"
	"go.uber.org/zap"
)

// ComponentStatus represents the health state of a monitored component.
type ComponentStatus int

const (
	StatusHealthy ComponentStatus = iota
	StatusDegraded
	StatusUnhealthy
)

// WatchdogConfig configures the watchdog behavior.
type WatchdogConfig struct {
	Enabled            bool          // Enable watchdog monitoring
	CheckInterval      time.Duration // How often to check health (default: 10s)
	TickStallThreshold int           // App ticks without progress before unhealthy (default: 20)
	PoolStallThreshold int           // Checks without pool progress before unhealthy (default: 6)
	RestartEnabled     bool          // Enable auto-restart (default: false for v1)
}

// DefaultWatchdogConfig returns a default configuration for the watchdog.
func DefaultWatchdogConfig() WatchdogConfig {
	return WatchdogConfig{
		Enabled:            true,
		CheckInterval:      10 * time.Second,
		TickStallThreshold: 20, // 2 seconds at TPS=10
		PoolStallThreshold: 6,  // 60s of checks (6 × 10s)
		RestartEnabled:     false,
	}
}

// poolState tracks the health state of a worker pool.
type poolState struct {
	lastCompleted int64
	stallCount    int
	lastStatus    ComponentStatus
}

// Watchdog monitors all CPRA system components for health.
type Watchdog struct {
	controller *Controller
	logger     *zap.SugaredLogger
	config     WatchdogConfig
	heartbeat  chan<- struct{}

	// State tracking
	lastAppTick    int64
	tickStallCount int
	poolStates     map[string]*poolState

	// Controller status tracking
	lastControllerStatus ComponentStatus

	ctx    context.Context
	cancel context.CancelFunc

	running atomic.Bool
}

// NewWatchdog creates a new watchdog for the given controller.
func NewWatchdog(ctrl *Controller, config WatchdogConfig, heartbeat chan<- struct{}, logger *zap.SugaredLogger) *Watchdog {
	return &Watchdog{
		controller: ctrl,
		logger:     logger,
		config:     config,
		heartbeat:  heartbeat,
		poolStates: map[string]*poolState{
			"pulse":        {},
			"intervention": {},
			"code":         {},
		},
	}
}

// Run starts the watchdog health check loop. Blocks until context is cancelled.
func (w *Watchdog) Run(ctx context.Context) error {
	if w.running.Swap(true) {
		return nil // Already running
	}
	defer w.running.Store(false)

	w.ctx, w.cancel = context.WithCancel(ctx)
	ticker := time.NewTicker(w.config.CheckInterval)
	defer ticker.Stop()

	w.logger.Infof("Watchdog started (interval=%v)", w.config.CheckInterval)

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Infof("Watchdog stopped")
			return nil
		case <-ticker.C:
			w.checkAllComponents()
			w.sendHeartbeat()
		}
	}
}

// Stop signals the watchdog to stop.
func (w *Watchdog) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

// sendHeartbeat sends a heartbeat signal to main.
func (w *Watchdog) sendHeartbeat() {
	select {
	case w.heartbeat <- struct{}{}:
	default:
		// Channel full, skip (main will notice missing heartbeats)
	}
}

// checkAllComponents runs all health checks.
func (w *Watchdog) checkAllComponents() {
	w.checkController()
	w.checkApp()
	w.checkPool("pulse", w.controller.pulsePool)
	w.checkPool("intervention", w.controller.interventionPool)
	w.checkPool("code", w.controller.codePool)
	w.checkQueue("pulse", w.controller.pulseQueue)
	w.checkQueue("intervention", w.controller.interventionQueue)
	w.checkQueue("code", w.controller.codeQueue)
}

// checkController verifies the controller is running.
func (w *Watchdog) checkController() {
	running := w.controller.running.Load()
	ctxActive := w.controller.ctx != nil && w.controller.ctx.Err() == nil

	var status ComponentStatus
	if running && ctxActive {
		status = StatusHealthy
	} else if !running && !ctxActive {
		// Intentionally stopped - not unhealthy
		status = StatusHealthy
	} else {
		status = StatusUnhealthy
	}

	// Log only on status change
	if status != w.lastControllerStatus {
		if status == StatusUnhealthy {
			w.logger.Warnw("Controller unhealthy", "running", running, "ctx_active", ctxActive)
		} else if w.lastControllerStatus == StatusUnhealthy {
			w.logger.Infow("Controller recovered", "running", running)
		}
		w.lastControllerStatus = status
	}
}

// checkApp monitors ark-tools app tick progress.
func (w *Watchdog) checkApp() {
	// Get current tick from resource
	tick := ecs.GetResource[resource.Tick](w.controller.world)
	if tick == nil {
		return
	}
	currentTick := tick.Tick

	// Compare with last recorded tick
	if w.lastAppTick == currentTick && w.controller.running.Load() {
		w.tickStallCount++
		if w.tickStallCount == w.config.TickStallThreshold {
			w.logger.Warnw("App tick stalled",
				"tick", currentTick,
				"stall_count", w.tickStallCount,
				"threshold", w.config.TickStallThreshold)
		}
	} else {
		if w.tickStallCount >= w.config.TickStallThreshold {
			w.logger.Infow("App tick resumed", "tick", currentTick)
		}
		w.tickStallCount = 0
		w.lastAppTick = currentTick
	}
}

// checkPool monitors a worker pool for stalls.
func (w *Watchdog) checkPool(name string, pool *queue.DynamicWorkerPool) {
	if pool == nil {
		return
	}

	stats := pool.Stats()
	state := w.poolStates[name]
	if state == nil {
		state = &poolState{}
		w.poolStates[name] = state
	}

	// Detect stall: tasks submitted but no completion progress
	hasPending := stats.TasksSubmitted > stats.TasksCompleted
	noProgress := stats.TasksCompleted == state.lastCompleted

	var status ComponentStatus
	if hasPending && noProgress {
		state.stallCount++
		if state.stallCount >= w.config.PoolStallThreshold {
			status = StatusUnhealthy
		} else if state.stallCount >= w.config.PoolStallThreshold/2 {
			status = StatusDegraded
		} else {
			status = StatusHealthy
		}
	} else {
		state.stallCount = 0
		status = StatusHealthy
	}

	// Also check for zero workers with pending work
	if stats.RunningWorkers == 0 && hasPending {
		status = StatusUnhealthy
	}

	// Log only on status change
	if status != state.lastStatus {
		switch status {
		case StatusUnhealthy:
			w.logger.Warnw("Pool unhealthy",
				"pool", name,
				"running_workers", stats.RunningWorkers,
				"pending", stats.TasksSubmitted-stats.TasksCompleted,
				"stall_count", state.stallCount)
		case StatusDegraded:
			w.logger.Warnw("Pool degraded",
				"pool", name,
				"pending", stats.TasksSubmitted-stats.TasksCompleted,
				"stall_count", state.stallCount)
		case StatusHealthy:
			if state.lastStatus != StatusHealthy {
				w.logger.Infow("Pool recovered", "pool", name)
			}
		}
		state.lastStatus = status
	}

	state.lastCompleted = stats.TasksCompleted
}

// checkQueue monitors a queue for capacity issues.
func (w *Watchdog) checkQueue(name string, q queue.Queue) {
	if q == nil {
		return
	}

	stats := q.Stats()
	if stats.Capacity <= 0 {
		return
	}

	utilization := float64(stats.QueueDepth) / float64(stats.Capacity)

	// Log only when approaching capacity (>90%)
	if utilization > 0.9 {
		w.logger.Warnw("Queue near capacity",
			"queue", name,
			"depth", stats.QueueDepth,
			"capacity", stats.Capacity,
			"utilization", utilization)
	}
}
