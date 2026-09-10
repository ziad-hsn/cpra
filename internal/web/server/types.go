package server

import (
	"time"

	"cpra/internal/controller"
	"cpra/internal/durable"
	"cpra/internal/queue"
	"cpra/internal/web/snapshot"
)

// ServerConfig configures the web server.
type ServerConfig struct {
	Store            *durable.Store `json:"-"`
	Addr             string         `json:"addr"`
	ReadTimeout      time.Duration  `json:"read_timeout"`
	WriteTimeout     time.Duration  `json:"write_timeout"`
	CORSAllowOrigins []string       `json:"cors_allow_origins"`
	HistoryInterval  time.Duration  `json:"history_interval"`
	HistoryCapacity  int            `json:"history_capacity"`
	// AuthToken, when non-empty, requires an Authorization: Bearer <token>
	// header on every request. Empty disables auth (the default).
	AuthToken string `json:"-"`
}

// PublicConfig is a redacted projection of controller.Config safe to expose.
type PublicConfig struct {
	QueueCapacity  uint64        `json:"queue_capacity"`
	BatchSize      int           `json:"batch_size"`
	AlertCooldown  time.Duration `json:"alert_cooldown"`
	RecoveryBypass bool          `json:"recovery_bypass"`
	UseAdaptive    bool          `json:"use_adaptive_queue"`
	QueueType      string        `json:"queue_type"`
}

// overviewResp is the /api/v1/overview payload.
type overviewResp struct {
	Generated   time.Time      `json:"generated"`
	Total       int            `json:"total"`
	Disabled    int            `json:"disabled"`
	ByStatus    map[string]int `json:"by_status"`
	ByPulseType map[string]int `json:"by_pulse_type"`
	ByCode      map[string]int `json:"by_code"`
	UpPercent   float64        `json:"up_percent"`
	IndexCapped bool           `json:"index_capped"`
}

// monitorsResp is the /api/v1/monitors payload.
type monitorsResp struct {
	Generated time.Time            `json:"generated"`
	Page      int                  `json:"page"`
	Size      int                  `json:"size"`
	Total     int                  `json:"total"`
	Filters   map[string]string    `json:"filters,omitempty"`
	Monitors  []MonitorSummaryJSON `json:"monitors"`
}

// MonitorSummaryJSON mirrors snapshot.MonitorSummary but is defined here to
// keep the JSON tags co-located with the API layer. The snapshot package is a
// leaf with no internal imports; we re-export its shape for the API.
type MonitorSummaryJSON = snapshot.MonitorSummary

// queueStatsJSON wraps queue.Stats with a name label for the API.
type queueStatsJSON struct {
	Name string `json:"name"`
	queue.Stats
}

// poolStatsJSON wraps queue.WorkerPoolStats with a name label for the API.
type poolStatsJSON struct {
	Name string `json:"name"`
	queue.WorkerPoolStats
}

// systemsResp is the /api/v1/systems payload.
type systemsResp struct {
	Systems   map[string]*controller.SystemMetrics `json:"systems"`
	Aggregate controller.AggregateMetrics          `json:"aggregate"`
}

// incidentsResp is the /api/v1/incidents payload.
type incidentsResp struct {
	Generated time.Time            `json:"generated"`
	Count     int                  `json:"count"`
	Incidents []MonitorSummaryJSON `json:"incidents"`
}
