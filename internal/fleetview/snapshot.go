// Package fleetview holds the read-only, point-in-time projection of CPRA
// fleet state that the web server serves to the dashboard.
//
// It is a leaf package (no internal imports) so it can be depended on by both
// the ECS systems package (which produces snapshots) and the web server
// package (which consumes them) without creating an import cycle.
package fleetview

import (
	"sync"
	"time"
)

// MonitorSummary is a lightweight projection of a single monitor used by the
// dashboard list and detail endpoints. The controller publishes it after applying
// committed state and never mutates it after publication,
// so it is safe to read concurrently from HTTP handler goroutines.
type MonitorSummary struct {
	IncarnationUID                   string                  `json:"incarnation_uid"`
	ConfigurationRevision            string                  `json:"configuration_revision"`
	CatalogRevision                  string                  `json:"-"`
	ControlRevision                  string                  `json:"control_revision"`
	StateGeneration                  uint64                  `json:"-"`
	IncidentRevision                 string                  `json:"-"`
	Source                           string                  `json:"source"`
	Health                           string                  `json:"health"`
	LastObservedHealth               string                  `json:"last_observed_health"`
	LastObservedAt                   time.Time               `json:"last_observed_at,omitzero"`
	Availability                     MonitorAvailability     `json:"availability"`
	Maintenance                      bool                    `json:"maintenance"`
	NotificationsSuppressed          bool                    `json:"notifications_suppressed"`
	SuppressionObservedAt            time.Time               `json:"suppression_observed_at,omitzero"`
	ConfigurationWarnings            []string                `json:"configuration_warnings,omitempty"`
	NotificationConditions           []NotificationCondition `json:"notification_conditions"`
	NotificationConditionsTruncated  bool                    `json:"notification_conditions_truncated"`
	NotificationConditionsObservedAt time.Time               `json:"notification_conditions_observed_at,omitzero"`
	NotificationConditionsSequence   string                  `json:"notification_conditions_sequence,omitempty"`
	Phase                            string                  `json:"phase"`
	Enabled                          bool                    `json:"enabled"`
	Ignored                          bool                    `json:"ignored"`
	SnoozedUntil                     time.Time               `json:"snoozed_until,omitzero"`
	IncidentID                       string                  `json:"incident_id,omitempty"`
	PendingNotifications             int                     `json:"pending_notifications"`
	CompletedNotifications           int                     `json:"completed_notifications"`
	FailedNotifications              int                     `json:"failed_notifications"`
	UnknownNotifications             int                     `json:"unknown_notifications"`
	CertificateVerificationDisabled  bool                    `json:"certificate_verification_disabled"`
	LastFailure                      FailureDiagnostic       `json:"last_failure,omitzero"`
	ConfigurationProblem             FailureDiagnostic       `json:"configuration_problem,omitzero"`
	ConfigurationProblemAt           time.Time               `json:"configuration_problem_at,omitzero"`
	CheckAdmission                   string                  `json:"check_admission,omitempty"`
	MissedChecks                     uint64                  `json:"missed_checks"`
	QueueRejections                  uint64                  `json:"queue_rejections"`
	MonitorID                        string                  `json:"monitor_id" yaml:"monitor_id"`
	LatencyAvailable                 bool                    `json:"latency_available"`
	ActionsNeedingReview             int                     `json:"actions_needing_review"`
	UnknownActions                   int                     `json:"unknown_actions"`
	Warning                          string                  `json:"warning,omitempty"`
	ID                               uint32                  `json:"id" yaml:"id"`
	Name                             string                  `json:"name" yaml:"name"`
	PulseType                        string                  `json:"pulse_type" yaml:"pulse_type"`
	Status                           string                  `json:"status" yaml:"status"` // up | down | verifying | incident | disabled
	Incident                         bool                    `json:"incident" yaml:"incident"`
	PendingCode                      string                  `json:"pending_code" yaml:"pending_code"`
	ConsecutiveFailures              int                     `json:"consecutive_failures" yaml:"consecutive_failures"`
	LastCheck                        time.Time               `json:"last_check,omitzero" yaml:"last_check"`
	LastSuccess                      time.Time               `json:"last_success,omitzero" yaml:"last_success"`
	NextCheck                        time.Time               `json:"next_check,omitzero" yaml:"next_check"`
	ActiveCodes                      []string                `json:"active_codes" yaml:"active_codes"`
	// Target is a human-readable endpoint for display (URL for http, host:port for
	// tcp/udp/grpc, host for icmp/dns/docker). Empty when not derivable.
	Target     string  `json:"target"`      // endpoint host/url for display
	IntervalMs int64   `json:"interval_ms"` // pulse interval in ms
	Uptime     float64 `json:"uptime"`      // rolling success ratio 0..1 over observed checks
	LatencyMs  float64 `json:"latency_ms"`  // last observed latency ms (0 if unknown)
}

// MonitorAvailability is the controller's assessment of whether this monitor
// can produce observations. ObservedAt is the assessment time, not a check time.
// Maintenance suppresses ordinary actions while leaving checks available.
type MonitorAvailability struct {
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitzero"`
}

// NotificationCondition is independent of execution attempts. Suppressed
// closures and migration holds can exist without a new executable action.
type NotificationCondition struct {
	ConditionID      string    `json:"conditionID"`
	Kind             string    `json:"kind"`
	Color            string    `json:"color"`
	State            string    `json:"state"`
	Reason           string    `json:"reason,omitempty"`
	OriginalActionID string    `json:"originalActionID,omitempty"`
	PendingAt        time.Time `json:"pendingAt,omitzero"`
	RecoveredAt      time.Time `json:"recoveredAt,omitzero"`
	MigrationReason  string    `json:"migrationReason,omitempty"`
}

// FailureDiagnostic contains only bounded classified observations, never raw
// provider responses. Value fields keep published rows immutable.
type FailureDiagnostic struct {
	Code                     string    `json:"code,omitempty"`
	Message                  string    `json:"message,omitempty"`
	HTTPStatus               int       `json:"httpStatus,omitempty"`
	GRPCStatus               string    `json:"grpcStatus,omitempty"`
	GRPCCode                 string    `json:"grpcCode,omitempty"`
	CertificateNotAfter      time.Time `json:"certificateNotAfter,omitzero"`
	CertificateDaysRemaining int       `json:"certificateDaysRemaining,omitempty"`
}

// StatsSnapshot is an immutable point-in-time projection of fleet state.
// Index.Overview copies aggregate counters under its read lock, so readers
// observe consistent counts without building a full monitor list.
type StatsSnapshot struct {
	// Generated is when the controller last refreshed this projection.
	Generated time.Time `json:"generated"`

	// Aggregate counts.
	Total                 int            `json:"total"`
	Disabled              int            `json:"disabled"`
	Unavailable           int            `json:"unavailable"`
	Paused                int            `json:"paused"`
	Pending               int            `json:"pending"`
	Maintenance           int            `json:"maintenance"`
	SuppressedUnhealthy   int            `json:"suppressed_unhealthy"`
	UnsuppressedUnhealthy int            `json:"unsuppressed_unhealthy"`
	ByStatus              map[string]int `json:"by_status"`     // up/down/verifying/incident/disabled
	ByPulseType           map[string]int `json:"by_pulse_type"` // http/tcp/icmp
	ByCode                map[string]int `json:"by_code"`       // color -> entities with that pending code

	// Monitors is the summary index used for listing and detail lookups.
	// The live Index serves bounded pages separately from aggregate responses.
	Monitors []MonitorSummary `json:"monitors,omitempty"`

	// ByID maps entity ID -> index into Monitors, for O(1) detail lookups.
	// Present only when Monitors is populated.
	ByID map[uint32]int `json:"-"`
}

// Holder is a thread-safe container for the latest StatsSnapshot. Systems
// publish via Set; HTTP handlers read via Get.
type Holder struct {
	mu    sync.RWMutex
	snap  *StatsSnapshot
	index *Index
}

// NewHolder returns an empty Holder.
func NewHolder() *Holder { return &Holder{} }

// Get returns the current snapshot (may be nil before the first tick).
// The returned pointer is safe to read concurrently; callers must not mutate it.
func (h *Holder) Get() *StatsSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.index != nil {
		return h.index.Overview()
	}
	return h.snap
}

// Set replaces a static snapshot. Live controllers publish through SetIndex.
func (h *Holder) Set(s *StatsSnapshot) {
	h.mu.Lock()
	h.snap = s
	h.mu.Unlock()
}

func (h *Holder) SetIndex(index *Index) { h.mu.Lock(); h.index = index; h.mu.Unlock() }
func (h *Holder) Index() *Index         { h.mu.RLock(); defer h.mu.RUnlock(); return h.index }
