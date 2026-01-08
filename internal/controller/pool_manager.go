package controller

import (
	"context"

	"cpra/internal/runtime/queue"
)

// PoolManager manages the three worker pools (pulse, intervention, code).
// It encapsulates worker pool lifecycle operations and provides aggregated statistics.
//
// This reduces the Controller's responsibilities by extracting pool management
// into a dedicated component.
type PoolManager struct {
	pulse        *queue.DynamicWorkerPool
	intervention *queue.DynamicWorkerPool
	code         *queue.DynamicWorkerPool
}

// NewPoolManager creates a new PoolManager with the provided worker pools.
func NewPoolManager(pulse, intervention, code *queue.DynamicWorkerPool) *PoolManager {
	return &PoolManager{
		pulse:        pulse,
		intervention: intervention,
		code:         code,
	}
}

// Pulse returns the pulse worker pool.
func (m *PoolManager) Pulse() *queue.DynamicWorkerPool {
	return m.pulse
}

// Intervention returns the intervention worker pool.
func (m *PoolManager) Intervention() *queue.DynamicWorkerPool {
	return m.intervention
}

// Code returns the code worker pool.
func (m *PoolManager) Code() *queue.DynamicWorkerPool {
	return m.code
}

// SetContext sets the context for all managed worker pools.
// This should be called before StartAll() to enable graceful cancellation.
func (m *PoolManager) SetContext(ctx context.Context) {
	if m.pulse != nil {
		m.pulse.SetContext(ctx)
	}
	if m.intervention != nil {
		m.intervention.SetContext(ctx)
	}
	if m.code != nil {
		m.code.SetContext(ctx)
	}
}

// StartAll starts all managed worker pools.
// Worker pools begin processing jobs from their respective queues.
func (m *PoolManager) StartAll() {
	if m.pulse != nil {
		m.pulse.Start()
	}
	if m.intervention != nil {
		m.intervention.Start()
	}
	if m.code != nil {
		m.code.Start()
	}
}

// DrainAll drains all managed worker pools.
// This waits for in-flight jobs to complete before returning.
// Order: pulse -> intervention -> code (follows dependency chain).
func (m *PoolManager) DrainAll(ctx context.Context) {
	if m.pulse != nil {
		m.pulse.DrainAndStop(ctx)
	}
	if m.intervention != nil {
		m.intervention.DrainAndStop(ctx)
	}
	if m.code != nil {
		m.code.DrainAndStop(ctx)
	}
}

// PoolManagerStats holds aggregated statistics for all managed worker pools.
type PoolManagerStats struct {
	Pulse          queue.WorkerPoolStats
	Intervention   queue.WorkerPoolStats
	Code           queue.WorkerPoolStats
	TotalWorkers   int
	TotalSubmitted int64
	TotalCompleted int64
}

// Stats returns aggregated statistics for all managed worker pools.
func (m *PoolManager) Stats() PoolManagerStats {
	var pulseStats, intStats, codeStats queue.WorkerPoolStats
	if m.pulse != nil {
		pulseStats = m.pulse.Stats()
	}
	if m.intervention != nil {
		intStats = m.intervention.Stats()
	}
	if m.code != nil {
		codeStats = m.code.Stats()
	}

	return PoolManagerStats{
		Pulse:          pulseStats,
		Intervention:   intStats,
		Code:           codeStats,
		TotalWorkers:   pulseStats.RunningWorkers + intStats.RunningWorkers + codeStats.RunningWorkers,
		TotalSubmitted: pulseStats.TasksSubmitted + intStats.TasksSubmitted + codeStats.TasksSubmitted,
		TotalCompleted: pulseStats.TasksCompleted + intStats.TasksCompleted + codeStats.TasksCompleted,
	}
}

// GetRouters returns the result routers for all worker pools.
// This is needed for wiring up ECS systems to process results.
func (m *PoolManager) GetRouters() (*queue.ResultRouter, *queue.ResultRouter, *queue.ResultRouter) {
	var pulseRouter, intRouter, codeRouter *queue.ResultRouter
	if m.pulse != nil {
		pulseRouter = m.pulse.GetRouter()
	}
	if m.intervention != nil {
		intRouter = m.intervention.GetRouter()
	}
	if m.code != nil {
		codeRouter = m.code.GetRouter()
	}
	return pulseRouter, intRouter, codeRouter
}
