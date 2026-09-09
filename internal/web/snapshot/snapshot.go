// Package snapshot holds the read-only, point-in-time projection of CPRA
// fleet state that the web server serves to the dashboard.
//
// It is a leaf package (no internal imports) so it can be depended on by both
// the ECS systems package (which produces snapshots) and the web server
// package (which consumes them) without creating an import cycle.
package snapshot

import (
	"sync"
	"time"
)

// MonitorSummary is a lightweight projection of a single monitor used by the
// dashboard list and detail endpoints. It is built inside an ECS system tick
// (see systems.BatchStatsSnapshotSystem) and never mutated after publication,
// so it is safe to read concurrently from HTTP handler goroutines.
type MonitorSummary struct {
	Warning             string    `json:"warning,omitempty"`
	ID                  uint32    `json:"id" yaml:"id"`
	Name                string    `json:"name" yaml:"name"`
	PulseType           string    `json:"pulse_type" yaml:"pulse_type"`
	Status              string    `json:"status" yaml:"status"` // up | down | verifying | incident | disabled
	Incident            bool      `json:"incident" yaml:"incident"`
	PendingCode         string    `json:"pending_code" yaml:"pending_code"`
	ConsecutiveFailures int       `json:"consecutive_failures" yaml:"consecutive_failures"`
	LastCheck           time.Time `json:"last_check" yaml:"last_check"`
	LastSuccess         time.Time `json:"last_success" yaml:"last_success"`
	NextCheck           time.Time `json:"next_check" yaml:"next_check"`
	ActiveCodes         []string  `json:"active_codes" yaml:"active_codes"`
	// Target is a human-readable endpoint for display (URL for http, host:port for
	// tcp/udp/grpc, host for icmp/dns/docker). Empty when not derivable.
	Target     string  `json:"target"`      // endpoint host/url for display
	IntervalMs int64   `json:"interval_ms"` // pulse interval in ms
	Uptime     float64 `json:"uptime"`      // rolling success ratio 0..1 over observed checks
	LatencyMs  float64 `json:"latency_ms"`  // last observed latency ms (0 if unknown)
}

// StatsSnapshot is an immutable point-in-time projection of fleet state.
// A new instance is produced on each snapshot tick and atomically swapped into
// the Holder, so readers always observe a consistent snapshot.
type StatsSnapshot struct {
	// Generated is when this snapshot was produced (inside the ECS tick).
	Generated time.Time `json:"generated"`

	// Aggregate counts.
	Total       int            `json:"total"`
	Disabled    int            `json:"disabled"`
	ByStatus    map[string]int `json:"by_status"`     // up/down/verifying/incident/disabled
	ByPulseType map[string]int `json:"by_pulse_type"` // http/tcp/icmp
	ByCode      map[string]int `json:"by_code"`       // color -> entities with that pending code

	// Monitors is the summary index used for listing and detail lookups.
	// It may be empty when the index exceeds MaxIndex (aggregates-only mode).
	Monitors []MonitorSummary `json:"monitors,omitempty"`

	// ByID maps entity ID -> index into Monitors, for O(1) detail lookups.
	// Present only when Monitors is populated.
	ByID map[uint32]int `json:"-"`
}

// Holder is a thread-safe container for the latest StatsSnapshot. Systems
// publish via Set; HTTP handlers read via Get.
type Holder struct {
	mu   sync.RWMutex
	snap *StatsSnapshot
}

// NewHolder returns an empty Holder.
func NewHolder() *Holder { return &Holder{} }

// Get returns the current snapshot (may be nil before the first tick).
// The returned pointer is safe to read concurrently; callers must not mutate it.
func (h *Holder) Get() *StatsSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.snap
}

// Set replaces the current snapshot. Called from the snapshot system tick.
func (h *Holder) Set(s *StatsSnapshot) {
	h.mu.Lock()
	h.snap = s
	h.mu.Unlock()
}
