package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"cpra/internal/controller"
)

// StatusResponse is the HTTP response payload for the CPRA status endpoint.
type StatusResponse struct {
	Service       string                   `json:"service"`
	Version       string                   `json:"version"`
	Timestamp     time.Time                `json:"timestamp"`
	UptimeSeconds float64                  `json:"uptime_seconds"`
	Queues        map[string]QueueSummary  `json:"queues"`
	Workers       map[string]WorkerSummary `json:"workers"`
	Entities      EntitiesSummary          `json:"entities"`
}

// QueueSummary captures a compact view of queue stats.
type QueueSummary struct {
	Depth        int           `json:"depth"`
	Capacity     int           `json:"capacity"`
	Enqueued     int64         `json:"enqueued"`
	Dequeued     int64         `json:"dequeued"`
	Dropped      int64         `json:"dropped"`
	EnqueueRate  float64       `json:"enqueue_rate"`
	DequeueRate  float64       `json:"dequeue_rate"`
	AvgWait      time.Duration `json:"avg_wait"`
	RollingP95   time.Duration `json:"p95_wait"`
	SampleWindow time.Duration `json:"sample_window"`
}

// WorkerSummary captures a compact view of worker pool stats.
type WorkerSummary struct {
	Running        int       `json:"running"`
	Idle           int       `json:"idle"`
	Capacity       int       `json:"capacity"`
	Target         int       `json:"target"`
	Waiting        int       `json:"waiting"`
	PendingResults int       `json:"pending_results"`
	LastScaleTime  time.Time `json:"last_scale_time"`
	ScalingEvents  int64     `json:"scaling_events"`
}

// EntitiesSummary captures a compact view of ECS entity stats.
type EntitiesSummary struct {
	Used     int `json:"used"`
	Recycled int `json:"recycled"`
	Total    int `json:"total"`
	Capacity int `json:"capacity"`
}

type statusHandler struct {
	ctrl           *controller.Controller
	serviceName    string
	serviceVersion string
	startedAt      time.Time
}

// NewStatusHandler returns an HTTP handler that exposes CPRA runtime status.
func NewStatusHandler(ctrl *controller.Controller, serviceName, serviceVersion string, startedAt time.Time) http.Handler {
	return &statusHandler{
		ctrl:           ctrl,
		serviceName:    serviceName,
		serviceVersion: serviceVersion,
		startedAt:      startedAt,
	}
}

func (h *statusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.ctrl == nil {
		http.Error(w, "controller unavailable", http.StatusServiceUnavailable)
		return
	}

	stats := h.ctrl.Stats()
	entities := EntitiesSummary{}
	if stats.World != nil {
		entities = EntitiesSummary{
			Used:     stats.World.Entities.Used,
			Recycled: stats.World.Entities.Recycled,
			Total:    stats.World.Entities.Total,
			Capacity: stats.World.Entities.Capacity,
		}
	}

	resp := StatusResponse{
		Service:       h.serviceName,
		Version:       h.serviceVersion,
		Timestamp:     time.Now().UTC(),
		UptimeSeconds: time.Since(h.startedAt).Seconds(),
		Queues: map[string]QueueSummary{
			"pulse": {
				Depth:        stats.PulseQueue.QueueDepth,
				Capacity:     stats.PulseQueue.Capacity,
				Enqueued:     stats.PulseQueue.Enqueued,
				Dequeued:     stats.PulseQueue.Dequeued,
				Dropped:      stats.PulseQueue.Dropped,
				EnqueueRate:  stats.PulseQueue.EnqueueRate,
				DequeueRate:  stats.PulseQueue.DequeueRate,
				AvgWait:      stats.PulseQueue.AvgQueueTime,
				RollingP95:   stats.PulseQueue.RollingP95Wait,
				SampleWindow: stats.PulseQueue.SampleWindow,
			},
			"intervention": {
				Depth:        stats.InterventionQueue.QueueDepth,
				Capacity:     stats.InterventionQueue.Capacity,
				Enqueued:     stats.InterventionQueue.Enqueued,
				Dequeued:     stats.InterventionQueue.Dequeued,
				Dropped:      stats.InterventionQueue.Dropped,
				EnqueueRate:  stats.InterventionQueue.EnqueueRate,
				DequeueRate:  stats.InterventionQueue.DequeueRate,
				AvgWait:      stats.InterventionQueue.AvgQueueTime,
				RollingP95:   stats.InterventionQueue.RollingP95Wait,
				SampleWindow: stats.InterventionQueue.SampleWindow,
			},
			"code": {
				Depth:        stats.CodeQueue.QueueDepth,
				Capacity:     stats.CodeQueue.Capacity,
				Enqueued:     stats.CodeQueue.Enqueued,
				Dequeued:     stats.CodeQueue.Dequeued,
				Dropped:      stats.CodeQueue.Dropped,
				EnqueueRate:  stats.CodeQueue.EnqueueRate,
				DequeueRate:  stats.CodeQueue.DequeueRate,
				AvgWait:      stats.CodeQueue.AvgQueueTime,
				RollingP95:   stats.CodeQueue.RollingP95Wait,
				SampleWindow: stats.CodeQueue.SampleWindow,
			},
		},
		Workers: map[string]WorkerSummary{
			"pulse": {
				Running:        stats.PulseWorkers.RunningWorkers,
				Idle:           calcIdle(stats.PulseWorkers.RunningWorkers, stats.PulseWorkers.CurrentCapacity),
				Capacity:       stats.PulseWorkers.CurrentCapacity,
				Target:         stats.PulseWorkers.TargetWorkers,
				Waiting:        stats.PulseWorkers.WaitingTasks,
				PendingResults: stats.PulseWorkers.PendingResults,
				LastScaleTime:  stats.PulseWorkers.LastScaleTime,
				ScalingEvents:  stats.PulseWorkers.ScalingEvents,
			},
			"intervention": {
				Running:        stats.InterventionWorkers.RunningWorkers,
				Idle:           calcIdle(stats.InterventionWorkers.RunningWorkers, stats.InterventionWorkers.CurrentCapacity),
				Capacity:       stats.InterventionWorkers.CurrentCapacity,
				Target:         stats.InterventionWorkers.TargetWorkers,
				Waiting:        stats.InterventionWorkers.WaitingTasks,
				PendingResults: stats.InterventionWorkers.PendingResults,
				LastScaleTime:  stats.InterventionWorkers.LastScaleTime,
				ScalingEvents:  stats.InterventionWorkers.ScalingEvents,
			},
			"code": {
				Running:        stats.CodeWorkers.RunningWorkers,
				Idle:           calcIdle(stats.CodeWorkers.RunningWorkers, stats.CodeWorkers.CurrentCapacity),
				Capacity:       stats.CodeWorkers.CurrentCapacity,
				Target:         stats.CodeWorkers.TargetWorkers,
				Waiting:        stats.CodeWorkers.WaitingTasks,
				PendingResults: stats.CodeWorkers.PendingResults,
				LastScaleTime:  stats.CodeWorkers.LastScaleTime,
				ScalingEvents:  stats.CodeWorkers.ScalingEvents,
			},
		},
		Entities: entities,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(resp); err != nil {
		http.Error(w, "failed to encode status response", http.StatusInternalServerError)
	}
}

func calcIdle(running, capacity int) int {
	if capacity <= running {
		return 0
	}
	return capacity - running
}
