package httpserver

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/queue"
	"github.com/ziad-hsn/cpra/internal/slo"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var observationRoutes = []struct{ operation, path, kind string }{
	{"GetState", "/api/v2/state", "State"}, {"GetQueues", "/api/v2/queues", "Queue"},
	{"GetPools", "/api/v2/pools", "Pool"}, {"GetSystems", "/api/v2/systems", "System"},
	{"GetSLO", "/api/v2/slo", "SLO"}, {"GetConfig", "/api/v2/config", "RuntimeConfig"},
	{"GetReady", "/api/v2/readyz", "Readiness"}, {"GetLive", "/api/v2/healthz", "Liveness"},
	{"GetMetrics", "/api/v2/metrics", "Metrics"},
}

func (s *Server) registerManagementObservations(mux *http.ServeMux, m *managementHTTP) {
	for _, route := range observationRoutes {
		mux.HandleFunc("GET "+route.path, func(w http.ResponseWriter, r *http.Request) {
			managementHeaders(w)
			var result any
			err := m.auth.WithPolicyAdmission(r, route.operation, func(access api.AccessInfo, generation uint64) error {
				if err := r.Context().Err(); err != nil {
					return err
				}
				if route.kind == "Queue" || route.kind == "Pool" || route.kind == "System" {
					query, err := observationQuery(r)
					if err != nil {
						return err
					}
					result, err = s.observationPage(m, query, route.kind, access.PrincipalID, generation)
					return err
				}
				if r.URL.RawQuery != "" {
					return managementFailure(400, "invalidQuery", "This aggregate observation does not accept query parameters.")
				}
				now := time.Now().UTC()
				switch route.operation {
				case "GetState":
					result = s.managementState(now)
				case "GetSLO":
					result = s.managementSLO(now)
				case "GetConfig":
					result = s.managementRuntimeConfig()
				case "GetReady":
					ready, _, _, reason := s.observationReadiness(now)
					if !ready {
						return managementFailure(503, "notReady", reason)
					}
					result = api.Health{Available: true, GeneratedAt: now}
				case "GetLive":
					result = api.Health{Available: true, GeneratedAt: now}
				case "GetMetrics":
					result = s.managementMetrics(now)
				}
				return nil
			})
			if err != nil {
				writeManagementError(w, err)
				return
			}
			writeManagementJSON(w, http.StatusOK, result)
		})
	}
}

func observationMeasurement(value float64) api.Measurement {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return unavailableMeasurement("invalid_observation")
	}
	return api.Measurement{Available: true, Value: value}
}
func unavailableMeasurement(reason string) api.Measurement {
	return api.Measurement{Available: false, Reason: reason}
}
func durationMeasurement(value time.Duration) api.Measurement {
	return observationMeasurement(float64(value) / float64(time.Millisecond))
}

// observationReadiness keeps progress/admission separate from projection age.
// A provider outage never changes liveness or readiness on its own.
func (s *Server) observationReadiness(now time.Time) (ready, projectionFresh, controllerReady bool, reason string) {
	snapshot := s.holder.Get()
	projectionFresh = snapshot != nil && !snapshot.Generated.IsZero() && !snapshot.Generated.After(now) && now.Sub(snapshot.Generated) <= 30*time.Second
	ready = projectionFresh && (s.cfg.AllowEmpty || snapshot.Total > 0)
	reason = "Controller initialization or progress is unavailable."
	if s.cfg.Ready != nil {
		controllerReady = s.cfg.Ready()
		ready = controllerReady
	}
	if s.cfg.Management != nil && !s.cfg.Management.Ready() {
		ready = false
		reason = "The durable management catalog is unavailable."
	}
	if s.cfg.Store != nil && !s.cfg.Store.Status().Ready {
		ready = false
		reason = "Durable storage is unavailable."
	}
	if s.admissionStopped() {
		ready = false
		reason = "Admission has stopped while CPRa drains."
	}
	if ready {
		reason = ""
	}
	return
}

func (s *Server) managementState(now time.Time) api.State {
	ready, fresh, controllerReady, reason := s.observationReadiness(now)
	result := api.State{GeneratedAt: now, Ready: ready, Live: true, ControllerAvailable: s.cfg.Ready != nil, ControllerReady: controllerReady, ReadinessReason: reason,
		ProjectionFresh: fresh, Storage: api.StorageState{Mode: "unavailable", CommitLatency: unavailableMeasurement("not_recorded"), SnapshotDuration: unavailableMeasurement("not_recorded")}}
	if s.cfg.Ready == nil {
		result.ControllerReason = "not_recorded"
	} else if !controllerReady {
		result.ControllerReason = "controller_not_ready"
	}
	if snapshot := s.holder.Get(); snapshot != nil && !snapshot.Generated.IsZero() && !snapshot.Generated.After(now) {
		result.ProjectionAvailable = true
		result.ProjectionAgeMS = float64(now.Sub(snapshot.Generated)) / float64(time.Millisecond)
	}
	if s.cfg.Store != nil {
		status, settings := s.cfg.Store.Status(), s.cfg.Store.ObservationConfig()
		result.Storage = api.StorageState{Available: status.Ready, Mode: status.Mode, FormatVersion: int64(settings.SnapshotFormat),
			CommitIndex: int64(status.CommittedIndex), AppliedIndex: int64(status.CommittedIndex), SingleNode: status.SingleNode, Error: status.Error,
			CommitLatency: unavailableMeasurement("not_recorded"), SnapshotDuration: unavailableMeasurement("not_recorded")}
		if status.CommitLatencyMS > 0 {
			result.Storage.CommitLatency = observationMeasurement(status.CommitLatencyMS)
		}
		if status.SnapshotDurationMS > 0 {
			result.Storage.SnapshotDuration = observationMeasurement(status.SnapshotDurationMS)
		}
	}
	// No fleet-wide scan is justified to fill an unavailable aggregate. Actions
	// have their separate indexed, bounded ListActions endpoint.
	return result
}

func queueObservation(name string, source queue.Queue) api.Queue {
	result := api.Queue{Name: name, OldestPendingMS: unavailableMeasurement("oldest_pending_age_not_recorded"), Utilization: unavailableMeasurement("source_unavailable"),
		AverageWait: unavailableMeasurement("no_dequeues"), MaximumWait: unavailableMeasurement("no_dequeues")}
	if source == nil {
		result.LimitReason = "source_unavailable"
		result.OldestPendingMS = unavailableMeasurement("source_unavailable")
		return result
	}
	stats := source.Stats()
	if stats.OldestPendingAvailable {
		result.OldestPendingMS = durationMeasurement(stats.OldestPendingAge)
	} else if stats.OldestPendingReason != "" {
		result.OldestPendingMS = unavailableMeasurement(stats.OldestPendingReason)
	}
	result.Available, result.Depth, result.Capacity = true, int64(stats.QueueDepth), int64(stats.Capacity)
	result.Drops = stats.Dropped
	result.Saturated = stats.Capacity > 0 && stats.QueueDepth >= stats.Capacity
	if result.Saturated {
		result.LimitReason = "queue_capacity"
	}
	if stats.Capacity > 0 {
		result.Utilization = observationMeasurement(float64(stats.QueueDepth) / float64(stats.Capacity))
	}
	if stats.SampleWindow > 0 && !math.IsNaN(stats.EnqueueRate) && !math.IsInf(stats.EnqueueRate, 0) && !math.IsNaN(stats.DequeueRate) && !math.IsInf(stats.DequeueRate, 0) && stats.EnqueueRate >= 0 && stats.DequeueRate >= 0 {
		result.RatesAvailable, result.EnqueuedPerSecond, result.DequeuedPerSecond = true, stats.EnqueueRate, stats.DequeueRate
		result.SampleWindow = api.Duration(stats.SampleWindow.String())
	}
	if stats.Dequeued > 0 {
		result.AverageWait, result.MaximumWait = durationMeasurement(stats.AvgQueueTime), durationMeasurement(stats.MaxQueueTime)
	}
	return result
}

func poolObservation(name string, source *queue.DynamicWorkerPool) api.Pool {
	result := api.Pool{Name: name, ServiceTime: unavailableMeasurement("no_service_samples")}
	if source == nil {
		return result
	}
	stats := source.Stats()
	result.Available, result.Workers, result.Capacity, result.Target = true, int64(stats.RunningWorkers), int64(stats.CurrentCapacity), int64(stats.TargetWorkers)
	result.Minimum, result.Maximum = int64(stats.MinWorkers), int64(stats.MaxWorkers)
	result.WaitingTasks, result.PendingResults = int64(stats.WaitingTasks), int64(stats.PendingResults)
	result.TasksSubmitted, result.TasksCompleted, result.ScalingEvents = stats.TasksSubmitted, stats.TasksCompleted, stats.ScalingEvents
	result.SizingModel, result.SloCondition, result.ServiceSamples = stats.SizingModel, stats.SLOCondition, int64(stats.ServiceSamples)
	if stats.ServiceSamples > 0 {
		result.ServiceTime = durationMeasurement(stats.ServiceTime)
	}
	if stats.ScalingEvents > 0 && !stats.LastScaleTime.IsZero() {
		result.LastScaleAt = &stats.LastScaleTime
	}
	return result
}

func (s *Server) managementQueues() []api.Queue {
	return []api.Queue{queueObservation("code", s.codeQueue), queueObservation("intervention", s.intervQueue), queueObservation("pulse", s.pulseQueue)}
}
func (s *Server) managementPools() []api.Pool {
	return []api.Pool{poolObservation("code", s.codePool), poolObservation("intervention", s.intervPool), poolObservation("pulse", s.pulsePool)}
}
func (s *Server) managementSystems() ([]api.System, bool) {
	values, available := s.metrics.MetricsSnapshot(256)
	result := make([]api.System, 0, len(values))
	for _, stats := range values {
		value := api.System{Name: stats.SystemName, Available: true, Updates: stats.TotalUpdates, EntitiesProcessed: stats.TotalEntitiesProcessed,
			LastUpdateDurationMS: unavailableMeasurement("last_update_duration_not_recorded"), AverageUpdateDuration: unavailableMeasurement("no_updates"),
			MinimumUpdateDuration: unavailableMeasurement("no_updates"), MaximumUpdateDuration: unavailableMeasurement("no_updates")}
		if stats.TotalUpdates > 0 {
			value.AverageUpdateDuration = observationMeasurement(float64(stats.TotalDuration) / float64(time.Millisecond) / float64(stats.TotalUpdates))
			value.MinimumUpdateDuration, value.MaximumUpdateDuration = durationMeasurement(stats.MinUpdateDuration), durationMeasurement(stats.MaxUpdateDuration)
			if !stats.LastUpdateTime.IsZero() {
				at := stats.LastUpdateTime
				value.ProgressAt = &at
			}
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, available
}

func sloPercentiles(value slo.Percentiles) api.Percentiles {
	if value.P50 == nil || value.P95 == nil || value.P99 == nil {
		return api.Percentiles{}
	}
	for _, p := range []*float64{value.P50, value.P95, value.P99} {
		if !observationMeasurement(*p).Available {
			return api.Percentiles{}
		}
	}
	return api.Percentiles{Available: true, P50MS: *value.P50, P95MS: *value.P95, P99MS: *value.P99}
}
func attainment(value *float64) api.Measurement {
	if value == nil {
		return unavailableMeasurement("no_expected_checks")
	}
	return observationMeasurement(*value)
}
func (s *Server) managementSLO(now time.Time) api.SLOView {
	result := api.SLOView{GeneratedAt: now, Window: "0s", CoverageGap: true, Reports: []api.SLOReport{}}
	if s.cfg.Store == nil {
		return result
	}
	view := s.cfg.Store.SLOView(now)
	result.Available, result.Window, result.CoverageGap = true, api.Duration((time.Duration(view.WindowSeconds) * time.Second).String()), !view.CoverageComplete
	result.QueueTargetMS, result.ResultTargetMS = view.QueueTargetMS, view.ResultTargetMS
	if !view.GapStart.IsZero() {
		result.GapStart = &view.GapStart
	}
	if !view.GapEnd.IsZero() {
		result.GapEnd = &view.GapEnd
	}
	for _, source := range view.Reports {
		result.Reports = append(result.Reports, api.SLOReport{Pipeline: "pulse", Driver: source.Driver, Samples: int64(source.Samples), Expected: int64(source.Expected),
			Missed: int64(source.Missed), Timeouts: int64(source.Timeouts), Overdue: int64(source.Overdue), Pending: int64(source.Pending),
			QueueDelay: sloPercentiles(source.Queue), Execution: sloPercentiles(source.Execution), TotalLatency: sloPercentiles(source.Result),
			QueueThresholdPassed: int64(source.QueueMet), TotalThresholdPassed: int64(source.ResultMet), Attainment: attainment(source.ResultAttainment),
			QueueAttainment: attainment(source.QueueAttainment), ResultAttainment: attainment(source.ResultAttainment), Condition: source.Condition,
			PausedMonitors: int64(source.PausedMonitors), PausedMonitorSeconds: source.PausedMonitorSeconds})
	}
	return result
}

func (s *Server) managementRuntimeConfig() api.RuntimeConfig {
	result := api.RuntimeConfig{QueueCapacity: int64(s.publicConfig.QueueCapacity), ReadOnly: s.cfg.Management == nil}
	if s.cfg.Store == nil {
		return result
	}
	config := s.cfg.Store.ObservationConfig()
	result.Available, result.StorageMode = true, config.StorageMode
	result.HistoryRetention = api.Duration((time.Duration(config.HistoryRetentionDays) * 24 * time.Hour).String())
	result.SloWindow, result.QueueTarget, result.ResultTarget = api.Duration(config.SLOWindow.String()), api.Duration(config.QueueTarget.String()), api.Duration(config.ResultTarget.String())
	return result
}
func (s *Server) managementMetrics(now time.Time) api.Metrics {
	systems, available := s.managementSystems()
	return api.Metrics{GeneratedAt: now, Queues: s.managementQueues(), Pools: s.managementPools(), Systems: systems, SLO: s.managementSLO(now),
		QueuesAvailable: s.pulseQueue != nil && s.intervQueue != nil && s.codeQueue != nil, PoolsAvailable: s.pulsePool != nil && s.intervPool != nil && s.codePool != nil, SystemsAvailable: available}
}

func observationQuery(r *http.Request) (url.Values, error) {
	if len(r.URL.RawQuery) > 4096 {
		return nil, managementFailure(400, "invalidQuery", "The observation query is too large.")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, managementFailure(400, "invalidQuery", "The observation query is invalid.")
	}
	for key, values := range query {
		if len(values) != 1 || (key != "cursor" && key != "limit" && key != "selector" && key != "monitorID") || ((key == "selector" || key == "monitorID") && values[0] != "") {
			return nil, managementFailure(400, "invalidQuery", "Pipeline and system observations accept only one cursor and one page limit; per-monitor and label filters are unsupported.")
		}
	}
	return query, nil
}

type observationList struct {
	Items       []json.RawMessage `json:"items"`
	Available   bool              `json:"available"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Snapshot    string            `json:"snapshot,omitempty"`
	NextCursor  string            `json:"nextCursor,omitempty"`
}

func (s *Server) observationRows(kind string) ([]json.RawMessage, bool, error) {
	var values []any
	available := true
	switch kind {
	case "Queue":
		for _, item := range s.managementQueues() {
			values = append(values, item)
			available = available && item.Available
		}
	case "Pool":
		for _, item := range s.managementPools() {
			values = append(values, item)
			available = available && item.Available
		}
	case "System":
		items, ok := s.managementSystems()
		available = ok
		for _, item := range items {
			values = append(values, item)
		}
	}
	rows := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, false, management.ErrUnavailable
		}
		rows = append(rows, raw)
	}
	return rows, available, nil
}

// Fixed pipeline/system snapshots share the same principal-bound quota and TTL
// as resource/history cursors. They never retain a fleet or query live ECS state.
func (s *Server) observationPage(m *managementHTTP, query url.Values, kind, principal string, generation uint64) (observationList, error) {
	limit := 0
	if value := query.Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 500 {
			return observationList{}, managementFailure(400, "invalidLimit", "Page limit must be from 1 to 500.")
		}
		limit = n
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cursorReady {
		return observationList{}, management.ErrUnavailable
	}
	now := m.now().UTC()
	m.pruneSnapshots(now, principal, generation)
	var entry managementSnapshot
	var cursor managementCursor
	start := 0
	if token := query.Get("cursor"); token != "" {
		var err error
		cursor, err = m.decodeCursor(token)
		if err != nil {
			return observationList{}, err
		}
		var ok bool
		entry, ok = m.snapshots[cursor.View]
		if !ok || entry.principal != principal || entry.generation != generation || entry.kind != "Observation/"+kind {
			return observationList{}, managementFailure(410, "cursorExpired", "The observation cursor expired or belongs to another access context.")
		}
		if limit != 0 && limit != entry.limit {
			return observationList{}, managementFailure(400, "cursorMismatch", "Continue with the original page limit.")
		}
		start, err = strconv.Atoi(cursor.After)
		if err != nil || start < 0 || start > len(entry.observations) {
			return observationList{}, managementFailure(400, "invalidCursor", "The observation cursor is invalid.")
		}
	} else {
		if limit == 0 {
			limit = 100
		}
		rows, available, err := s.observationRows(kind)
		if err != nil {
			return observationList{}, err
		}
		entry = managementSnapshot{observations: rows, observationsAvailable: available, principal: principal, generation: generation, kind: "Observation/" + kind, limit: limit, created: now, expires: now.Add(managementCursorTTL)}
		cursor.View = uuid.NewString()
	}
	end := min(start+entry.limit, len(entry.observations))
	result := observationList{Items: entry.observations[start:end], Available: entry.observationsAvailable, GeneratedAt: entry.created, Snapshot: cursor.View}
	if end < len(entry.observations) {
		if _, exists := m.snapshots[cursor.View]; !exists {
			owned := 0
			for _, saved := range m.snapshots {
				if saved.principal == principal {
					owned++
				}
			}
			if len(m.snapshots) >= managementMaxSnapshots || owned >= managementPrincipalSnapshots {
				return observationList{}, managementFailure(429, "snapshotQuota", "Too many snapshots are retained. Reuse an existing cursor or wait for expiry.")
			}
			m.snapshots[cursor.View] = entry
		}
		result.NextCursor = m.encodeCursor(managementCursor{View: cursor.View, After: strconv.Itoa(end)})
	}
	return result, nil
}
