package controller

import (
	"cpra/internal/runtime/queue"
)

// QueueManager manages the three job queues (pulse, intervention, code).
// It encapsulates queue lifecycle operations and provides aggregated statistics.
//
// This reduces the Controller's responsibilities by extracting queue management
// into a dedicated component.
type QueueManager struct {
	pulse        queue.Queue
	intervention queue.Queue
	code         queue.Queue
}

// NewQueueManager creates a new QueueManager with the provided queues.
func NewQueueManager(pulse, intervention, code queue.Queue) *QueueManager {
	return &QueueManager{
		pulse:        pulse,
		intervention: intervention,
		code:         code,
	}
}

// Pulse returns the pulse queue.
func (m *QueueManager) Pulse() queue.Queue {
	return m.pulse
}

// Intervention returns the intervention queue.
func (m *QueueManager) Intervention() queue.Queue {
	return m.intervention
}

// Code returns the code queue.
func (m *QueueManager) Code() queue.Queue {
	return m.code
}

// CloseAll closes all managed queues.
// This should be called during graceful shutdown after worker pools have drained.
func (m *QueueManager) CloseAll() {
	if m.pulse != nil {
		m.pulse.Close()
	}
	if m.intervention != nil {
		m.intervention.Close()
	}
	if m.code != nil {
		m.code.Close()
	}
}

// DrainAll removes any remaining jobs from all managed queues.
// Intended for shutdown cleanup after worker pools are stopped.
func (m *QueueManager) DrainAll() {
	drain := func(q queue.Queue) {
		if q == nil {
			return
		}
		for {
			job, err := q.Dequeue()
			if err != nil || job == nil {
				return
			}
		}
	}
	drain(m.pulse)
	drain(m.intervention)
	drain(m.code)
}

// QueueManagerStats holds aggregated statistics for all managed queues.
type QueueManagerStats struct {
	Pulse        queue.Stats
	Intervention queue.Stats
	Code         queue.Stats
	TotalDepth   int
	TotalDropped int64
}

// Stats returns aggregated statistics for all managed queues.
func (m *QueueManager) Stats() QueueManagerStats {
	pulseStats := m.pulse.Stats()
	intStats := m.intervention.Stats()
	codeStats := m.code.Stats()

	return QueueManagerStats{
		Pulse:        pulseStats,
		Intervention: intStats,
		Code:         codeStats,
		TotalDepth:   pulseStats.QueueDepth + intStats.QueueDepth + codeStats.QueueDepth,
		TotalDropped: pulseStats.Dropped + intStats.Dropped + codeStats.Dropped,
	}
}

// TotalPendingJobs returns the total number of jobs waiting across all queues.
func (m *QueueManager) TotalPendingJobs() int {
	return m.Stats().TotalDepth
}
