package client

import (
	"time"

	"cpra/internal/queue"
	"cpra/internal/web/snapshot"
)

// Overview is the /api/v1/overview payload.
type Overview struct {
	Generated   time.Time      `json:"generated"`
	Total       int            `json:"total"`
	Disabled    int            `json:"disabled"`
	ByStatus    map[string]int `json:"by_status"`
	ByPulseType map[string]int `json:"by_pulse_type"`
	ByCode      map[string]int `json:"by_code"`
	UpPercent   float64        `json:"up_percent"`
	IndexCapped bool           `json:"index_capped"`
}

// MonitorsList is the /api/v1/monitors payload.
type MonitorsList struct {
	Generated time.Time                 `json:"generated"`
	Page      int                       `json:"page"`
	Size      int                       `json:"size"`
	Total     int                       `json:"total"`
	Filters   map[string]string         `json:"filters,omitempty"`
	Monitors  []snapshot.MonitorSummary `json:"monitors"`
}

// Incidents is the /api/v1/incidents payload.
type Incidents struct {
	Generated time.Time                 `json:"generated"`
	Count     int                       `json:"count"`
	Incidents []snapshot.MonitorSummary `json:"incidents"`
}

// Systems is the /api/v1/systems payload.
type Systems struct {
	Systems   map[string]SystemMetrics `json:"systems"`
	Aggregate AggregateMetrics         `json:"aggregate"`
}

// SystemMetrics mirrors the per-system performance metrics exposed by the API.
type SystemMetrics struct {
	LastUpdateTime         time.Time     `json:"last_update_time"`
	StartTime              time.Time     `json:"start_time"`
	SystemName             string        `json:"system_name"`
	TotalUpdates           int64         `json:"total_updates"`
	TotalEntitiesProcessed int64         `json:"total_entities_processed"`
	TotalBatchesCreated    int64         `json:"total_batches_created"`
	TotalDuration          time.Duration `json:"total_duration"`
	MaxUpdateDuration      time.Duration `json:"max_update_duration"`
	MinUpdateDuration      time.Duration `json:"min_update_duration"`
}

// AggregateMetrics mirrors the aggregate performance metrics exposed by the API.
type AggregateMetrics struct {
	StartTime              time.Time     `json:"start_time"`
	MinUpdateDuration      time.Duration `json:"min_update_duration"`
	TotalEntitiesProcessed int64         `json:"total_entities_processed"`
	TotalBatchesCreated    int64         `json:"total_batches_created"`
	TotalDuration          time.Duration `json:"total_duration"`
	MaxUpdateDuration      time.Duration `json:"max_update_duration"`
	SystemCount            int           `json:"system_count"`
	AvgUpdateDuration      time.Duration `json:"avg_update_duration"`
	AvgEntitiesPerUpdate   float64       `json:"avg_entities_per_update"`
	AvgBatchesPerUpdate    float64       `json:"avg_batches_per_update"`
	EntitiesPerSecond      float64       `json:"entities_per_second"`
	UpdatesPerSecond       float64       `json:"updates_per_second"`
	TotalUpdates           int64         `json:"total_updates"`
}

// QueueStats is a named queue statistics snapshot (the /api/v1/queues entry).
type QueueStats struct {
	Name string `json:"name"`
	queue.Stats
}

// PoolStats is a named worker-pool statistics snapshot (the /api/v1/pools entry).
type PoolStats struct {
	Name string `json:"name"`
	queue.WorkerPoolStats
}

// QueuesHistory is the /api/v1/queues/history payload, keyed by queue name.
type QueuesHistory map[string][]queue.Sample[queue.Stats]

// PoolsHistory is the /api/v1/pools/history payload, keyed by pool name.
type PoolsHistory map[string][]queue.Sample[queue.WorkerPoolStats]

// RuntimeConfig is the /api/v1/config payload (a redacted projection of the
// controller configuration).
type RuntimeConfig struct {
	QueueCapacity  uint64        `json:"queue_capacity"`
	BatchSize      int           `json:"batch_size"`
	AlertCooldown  time.Duration `json:"alert_cooldown"`
	RecoveryBypass bool          `json:"recovery_bypass"`
	UseAdaptive    bool          `json:"use_adaptive_queue"`
	QueueType      string        `json:"queue_type"`
}
