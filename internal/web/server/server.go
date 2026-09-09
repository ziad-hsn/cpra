// Package server exposes a read-only HTTP API, a Prometheus /metrics endpoint,
// and the embedded dashboard SPA for CPRA. It never mutates ECS state: all
// fleet data is read from a precomputed snapshot.StatsSnapshot produced
// inside an ECS system tick, plus the controller's thread-safe
// MetricsAggregator and the queue/worker-pool Stats() accessors.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpra/internal/controller"
	"cpra/internal/queue"
	"cpra/internal/web/snapshot"
)

//go:embed all:assets
var assetsFS embed.FS

// Server is the read-only CPRA web server.
type Server struct {
	cfg             ServerConfig
	holder          *snapshot.Holder
	metrics         *controller.MetricsAggregator
	pulseQueue      queue.Queue
	intervQueue     queue.Queue
	codeQueue       queue.Queue
	pulsePool       *queue.DynamicWorkerPool
	intervPool      *queue.DynamicWorkerPool
	codePool        *queue.DynamicWorkerPool
	publicConfig    PublicConfig
	indexHTML       []byte
	srv             *http.Server
	queueHistory    map[string]*queue.History[queue.Stats]
	poolHistory     map[string]*queue.History[queue.WorkerPoolStats]
	historyStop     chan struct{}
	wg              sync.WaitGroup
	samplerOnce     sync.Once
	samplerStopOnce sync.Once
}

// New creates a Server wired to the given holder, metrics aggregator, queues,
// pools, and a redacted config snapshot.
func New(cfg ServerConfig, holder *snapshot.Holder, metrics *controller.MetricsAggregator, pq, iq, cq queue.Queue, pp, ip, cp *queue.DynamicWorkerPool, pc PublicConfig) *Server {
	if cfg.Addr == "" {
		cfg.Addr = "localhost:8060"
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 10 * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 30 * time.Second
	}
	// Pre-load the SPA index.html so we can serve it directly for the root and
	// SPA fallback routes without triggering net/http's index-canonical redirect.
	idxHTML, err := assetsFS.ReadFile("assets/index.html")
	if err != nil {
		controller.SystemLogger.Warn("web: failed to read embedded index.html: %v", err)
	}

	historyCapacity := cfg.HistoryCapacity
	if historyCapacity <= 0 {
		historyCapacity = 900 // 15 minutes at the default 1s interval
	}

	return &Server{
		cfg:          cfg,
		holder:       holder,
		metrics:      metrics,
		pulseQueue:   pq,
		intervQueue:  iq,
		codeQueue:    cq,
		pulsePool:    pp,
		intervPool:   ip,
		codePool:     cp,
		publicConfig: pc,
		indexHTML:    idxHTML,
		queueHistory: map[string]*queue.History[queue.Stats]{
			"pulse":        queue.NewHistory[queue.Stats](historyCapacity),
			"intervention": queue.NewHistory[queue.Stats](historyCapacity),
			"code":         queue.NewHistory[queue.Stats](historyCapacity),
		},
		poolHistory: map[string]*queue.History[queue.WorkerPoolStats]{
			"pulse":        queue.NewHistory[queue.WorkerPoolStats](historyCapacity),
			"intervention": queue.NewHistory[queue.WorkerPoolStats](historyCapacity),
			"code":         queue.NewHistory[queue.WorkerPoolStats](historyCapacity),
		},
	}
}

// Start binds the listener synchronously (so bind errors are returned to the
// caller) and then serves in a goroutine.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	s.registerAPI(mux)
	s.registerMetrics(mux)
	s.registerSPA(mux)

	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("web server listen on %s: %w", s.cfg.Addr, err)
	}

	var handler http.Handler = mux
	if s.cfg.AuthToken != "" {
		handler = s.authMiddleware(handler)
	}

	handler = s.corsMiddleware(handler)
	s.srv = &http.Server{
		Handler:      handler,
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
	}

	s.startHistorySampler()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			controller.SystemLogger.Warn("Web server error: %v", err)
		}
	}()

	controller.SystemLogger.Info("Web server listening at http://%s (API: /api/v1, metrics: /metrics)", s.cfg.Addr)
	return nil
}

// Stop gracefully shuts down the server, draining in-flight requests and
// waiting for the serve and sampler goroutines to exit.
func (s *Server) Stop() {
	if s.srv == nil {
		return
	}
	s.stopHistorySampler()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.WriteTimeout)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		controller.SystemLogger.Warn("Web server shutdown: %v", err)
	}
	s.wg.Wait()
	controller.SystemLogger.Info("Web server stopped")
}

// startHistorySampler launches a goroutine that periodically records queue and
// worker-pool statistics into the history ring buffers. It is idempotent: a
// second call is a no-op, so Start cannot spawn duplicate samplers.
func (s *Server) startHistorySampler() {
	s.samplerOnce.Do(func() {
		interval := s.cfg.HistoryInterval
		if interval <= 0 {
			interval = time.Second
		}
		s.historyStop = make(chan struct{})
		stop := s.historyStop
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					s.recordHistory()
				case <-stop:
					return
				}
			}
		}()
	})
}

// stopHistorySampler stops the history sampler goroutine, if running.
func (s *Server) stopHistorySampler() {
	if s.historyStop != nil {
		s.samplerStopOnce.Do(func() { close(s.historyStop) })
	}
}

// recordHistory samples the current queue and pool stats into the ring buffers.
func (s *Server) recordHistory() {
	if s.pulseQueue != nil {
		s.queueHistory["pulse"].Record(s.pulseQueue.Stats())
	}
	if s.intervQueue != nil {
		s.queueHistory["intervention"].Record(s.intervQueue.Stats())
	}
	if s.codeQueue != nil {
		s.queueHistory["code"].Record(s.codeQueue.Stats())
	}
	if s.pulsePool != nil {
		s.poolHistory["pulse"].Record(s.pulsePool.Stats())
	}
	if s.intervPool != nil {
		s.poolHistory["intervention"].Record(s.intervPool.Stats())
	}
	if s.codePool != nil {
		s.poolHistory["code"].Record(s.codePool.Stats())
	}
}

// ---------- middleware ----------

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	allow := s.cfg.CORSAllowOrigins
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && len(allow) > 0 {
			for _, o := range allow {
				if o == "*" {
					// Wildcard: allow any origin with the literal wildcard value.
					w.Header().Set("Access-Control-Allow-Origin", "*")
					w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
					w.Header().Set("Access-Control-Max-Age", "300")
					break
				}
				if o == origin {
					// Echo the actual request origin (not the allowlist entry) so
					// credentialed requests and shared caches behave correctly.
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
					w.Header().Set("Access-Control-Max-Age", "300")
					break
				}
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware enforces an Authorization: Bearer token when AuthToken is
// configured, so the read-only API can be protected before binding to a
// non-loopback address.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	token := s.cfg.AuthToken
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		valid := strings.HasPrefix(auth, "Bearer ") && subtleTimeCompare(strings.TrimPrefix(auth, "Bearer "), token)
		if user, password, ok := r.BasicAuth(); ok && user == "cpra" {
			valid = subtleTimeCompare(password, token)
		}
		if !valid {
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" {
				w.Header().Set("WWW-Authenticate", "Bearer")
			} else {
				w.Header().Set("WWW-Authenticate", `Basic realm="CPRa", charset="UTF-8"`)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")

		next.ServeHTTP(w, r)
	})
}

// subtleTimeCompare is a constant-time string comparison to avoid leaking the
// token length via early-exit timing.
func subtleTimeCompare(a, b string) bool {
	ah, bh := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}

// ---------- routing ----------

func (s *Server) registerAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/overview", s.handleOverview)
	mux.HandleFunc("/api/v1/monitors", s.handleMonitorsList)
	mux.HandleFunc("/api/v1/monitors/", s.handleMonitorDetail)
	mux.HandleFunc("/api/v1/incidents", s.handleIncidents)
	mux.HandleFunc("/api/v1/systems", s.handleSystems)
	mux.HandleFunc("/api/v1/queues", s.handleQueues)
	mux.HandleFunc("/api/v1/queues/history", s.handleQueuesHistory)
	mux.HandleFunc("/api/v1/pools", s.handlePools)
	mux.HandleFunc("/api/v1/pools/history", s.handlePoolsHistory)
	mux.HandleFunc("/api/v1/config", s.handleConfig)
	mux.HandleFunc("/api/v1/healthz", s.handleHealthz)
	mux.HandleFunc("/api/v1/readyz", s.handleReadyz)
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		controller.SystemLogger.Warn("web: json encode error: %v", err)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parseUintParam(path, prefix string) (uint32, bool) {
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.TrimPrefix(rest, "/")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return 0, false
	}
	// Reject any trailing path segments: the id must be the entire remainder.
	if strings.Contains(rest, "/") {
		return 0, false
	}
	n, err := strconv.ParseUint(rest, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

// ---------- handlers ----------

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	snap := s.holder.Get()
	if snap == nil || snap.Total == 0 || time.Since(snap.Generated) > 30*time.Second {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleOverview(w http.ResponseWriter, _ *http.Request) {
	snap := s.holder.Get()
	if snap == nil {
		writeJSON(w, http.StatusOK, overviewResp{
			ByStatus:    map[string]int{},
			ByPulseType: map[string]int{},
			ByCode:      map[string]int{},
		})
		return
	}
	up := snap.ByStatus["up"]
	total := snap.Total
	var upPct float64
	if total > 0 {
		upPct = 100.0 * float64(up) / float64(total)
	}
	writeJSON(w, http.StatusOK, overviewResp{
		Generated:   snap.Generated,
		Total:       snap.Total,
		Disabled:    snap.Disabled,
		ByStatus:    snap.ByStatus,
		ByPulseType: snap.ByPulseType,
		ByCode:      snap.ByCode,
		UpPercent:   upPct,
		IndexCapped: snap.Monitors == nil && snap.Total > 0,
	})
}

func (s *Server) handleMonitorsList(w http.ResponseWriter, r *http.Request) {
	snap := s.holder.Get()
	if snap == nil || (snap.Total > 0 && snap.Monitors == nil) {
		writeErr(w, http.StatusServiceUnavailable, "monitor details unavailable; consult overview aggregates")
		return
	}
	if snap.Monitors == nil {
		writeJSON(w, http.StatusOK, monitorsResp{
			Page: 1, Size: 0, Total: 0,
			Monitors: []MonitorSummaryJSON{},
		})
		return
	}
	q := r.URL.Query()
	status := strings.ToLower(strings.TrimSpace(q.Get("status")))
	pulseType := strings.ToLower(strings.TrimSpace(q.Get("type")))
	code := strings.ToLower(strings.TrimSpace(q.Get("code")))
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(q.Get("size"))
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}

	filtered := make([]MonitorSummaryJSON, 0, len(snap.Monitors))
	for _, m := range snap.Monitors {
		if status != "" && m.Status != status {
			continue
		}
		if pulseType != "" && m.PulseType != pulseType {
			continue
		}
		if code != "" && m.PendingCode != code {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(m.Name), search) {
			continue
		}
		filtered = append(filtered, m)
	}

	total := len(filtered)
	start := total
	if page-1 <= total/size {
		start = (page - 1) * size
	}
	end := total
	if size < total-start {
		end = start + size
	}
	pageItems := filtered[start:end]
	if pageItems == nil {
		pageItems = []MonitorSummaryJSON{}
	}
	filters := map[string]string{}
	if status != "" {
		filters["status"] = status
	}
	if pulseType != "" {
		filters["type"] = pulseType
	}
	if code != "" {
		filters["code"] = code
	}
	if search != "" {
		filters["q"] = search
	}
	writeJSON(w, http.StatusOK, monitorsResp{
		Generated: snap.Generated,
		Page:      page,
		Size:      size,
		Total:     total,
		Filters:   filters,
		Monitors:  pageItems,
	})
}

func (s *Server) handleMonitorDetail(w http.ResponseWriter, r *http.Request) {
	// ServeMux routes the exact "/api/v1/monitors" to handleMonitorsList, so
	// only the trailing-slash form reaches us here; delegate it to the list.
	if r.URL.Path == "/api/v1/monitors/" {
		s.handleMonitorsList(w, r)
		return
	}
	id, ok := parseUintParam(r.URL.Path, "/api/v1/monitors")
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid monitor id")
		return
	}
	snap := s.holder.Get()
	if snap == nil || snap.ByID == nil {
		writeErr(w, http.StatusNotFound, "monitor not found (snapshot unavailable)")
		return
	}
	idx, found := snap.ByID[id]
	if !found {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("monitor %d not found", id))
		return
	}
	if idx >= len(snap.Monitors) {
		writeErr(w, http.StatusNotFound, "monitor index out of range")
		return
	}
	writeJSON(w, http.StatusOK, snap.Monitors[idx])
}

func (s *Server) handleIncidents(w http.ResponseWriter, _ *http.Request) {
	snap := s.holder.Get()
	if snap == nil || (snap.Total > 0 && snap.Monitors == nil) {
		writeErr(w, http.StatusServiceUnavailable, "incident details unavailable; consult overview aggregates")
		return
	}
	if snap.Monitors == nil {
		writeJSON(w, http.StatusOK, incidentsResp{Incidents: []MonitorSummaryJSON{}})
		return
	}
	incidents := make([]MonitorSummaryJSON, 0)
	for _, m := range snap.Monitors {
		if m.Incident || m.Status == "incident" {
			incidents = append(incidents, m)
		}
	}
	writeJSON(w, http.StatusOK, incidentsResp{
		Generated: snap.Generated,
		Count:     len(incidents),
		Incidents: incidents,
	})
}

func (s *Server) handleSystems(w http.ResponseWriter, _ *http.Request) {
	var all map[string]*controller.SystemMetrics
	var agg controller.AggregateMetrics
	if s.metrics != nil {
		all = s.metrics.GetAllMetrics()
		agg = s.metrics.GetAggregateMetrics()
	}
	writeJSON(w, http.StatusOK, systemsResp{Systems: all, Aggregate: agg})
}

// safeQueueStats returns q's stats, or a zero value when q is nil.
func safeQueueStats(q queue.Queue) queue.Stats {
	if q == nil {
		return queue.Stats{}
	}
	return q.Stats()
}

// safePoolStats returns p's stats, or a zero value when p is nil.
func safePoolStats(p *queue.DynamicWorkerPool) queue.WorkerPoolStats {
	if p == nil {
		return queue.WorkerPoolStats{}
	}
	return p.Stats()
}

func (s *Server) handleQueues(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]queueStatsJSON{
		"pulse":        {Name: "pulse", Stats: safeQueueStats(s.pulseQueue)},
		"intervention": {Name: "intervention", Stats: safeQueueStats(s.intervQueue)},
		"code":         {Name: "code", Stats: safeQueueStats(s.codeQueue)},
	})
}

func (s *Server) handlePools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]poolStatsJSON{
		"pulse":        {Name: "pulse", WorkerPoolStats: safePoolStats(s.pulsePool)},
		"intervention": {Name: "intervention", WorkerPoolStats: safePoolStats(s.intervPool)},
		"code":         {Name: "code", WorkerPoolStats: safePoolStats(s.codePool)},
	})
}

// handleQueuesHistory returns the rolling window of queue statistics, keyed by
// queue name. Each sample is {timestamp, value} where value is a queue.Stats.
func (s *Server) handleQueuesHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string][]queue.Sample[queue.Stats]{
		"pulse":        s.queueHistory["pulse"].Snapshot(),
		"intervention": s.queueHistory["intervention"].Snapshot(),
		"code":         s.queueHistory["code"].Snapshot(),
	})
}

// handlePoolsHistory returns the rolling window of worker-pool statistics,
// keyed by pool name. Each sample is {timestamp, value} where value is a
// queue.WorkerPoolStats.
func (s *Server) handlePoolsHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string][]queue.Sample[queue.WorkerPoolStats]{
		"pulse":        s.poolHistory["pulse"].Snapshot(),
		"intervention": s.poolHistory["intervention"].Snapshot(),
		"code":         s.poolHistory["code"].Snapshot(),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.publicConfig)
}

// ---------- SPA ----------

func (s *Server) registerSPA(mux *http.ServeMux) {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		controller.SystemLogger.Warn("web: assets sub-fs error: %v", err)
		return
	}
	fileServer := http.FileServer(http.FS(sub))
	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(s.indexHTML)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Unknown API paths should 404, not fall through to the SPA.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		cleanPath := strings.TrimPrefix(r.URL.Path, "/")
		if cleanPath == "" {
			serveIndex(w)
			return
		}
		// Serve a real embedded asset (JS/CSS/favicon) via the file server; it
		// never touches index.html so it avoids the index-canonical redirect.
		if _, err := fs.Stat(sub, cleanPath); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// Everything else is a client-side SPA route: return the index HTML so
		// the router can take over (history-API fallback).
		serveIndex(w)
	})
}

// ---------- Prometheus /metrics (manual text exposition, no extra deps) ----------

func (s *Server) registerMetrics(mux *http.ServeMux) {
	mux.HandleFunc("/metrics", s.handleMetrics)
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b metricWriter

	snap := s.holder.Get()
	if snap != nil {
		writeGauge(&b, "cpra_monitors", "Total number of monitors.", nil, float64(snap.Total))
		for status, n := range snap.ByStatus {
			writeGauge(&b, "cpra_monitors_by_status", "Monitors by status.", []kv{{"status", status}}, float64(n))
		}
		for pt, n := range snap.ByPulseType {
			writeGauge(&b, "cpra_monitors_by_pulse_type", "Monitors by pulse type.", []kv{{"type", pt}}, float64(n))
		}
		for color, n := range snap.ByCode {
			writeGauge(&b, "cpra_monitors_by_code", "Monitors by pending alert code.", []kv{{"color", color}}, float64(n))
		}
	}

	queueStats := map[string]queue.Stats{
		"pulse":        safeQueueStats(s.pulseQueue),
		"intervention": safeQueueStats(s.intervQueue),
		"code":         safeQueueStats(s.codeQueue),
	}
	for name, qs := range queueStats {
		lbl := []kv{{"queue", name}}
		writeGauge(&b, "cpra_queue_depth", "Current queue depth.", lbl, float64(qs.QueueDepth))
		writeGauge(&b, "cpra_queue_capacity", "Queue capacity.", lbl, float64(qs.Capacity))
		writeCounter(&b, "cpra_queue_enqueued_total", "Total jobs enqueued.", lbl, float64(qs.Enqueued))
		writeCounter(&b, "cpra_queue_dequeued_total", "Total jobs dequeued.", lbl, float64(qs.Dequeued))
		writeCounter(&b, "cpra_queue_dropped_total", "Total jobs dropped.", lbl, float64(qs.Dropped))
		writeGauge(&b, "cpra_queue_arrival_rate", "Queue arrival rate (jobs/s).", lbl, qs.EnqueueRate)
		writeGauge(&b, "cpra_queue_service_rate", "Queue service rate (jobs/s).", lbl, qs.DequeueRate)
		writeGauge(&b, "cpra_queue_avg_wait_seconds", "Average queue wait (s).", lbl, qs.AvgQueueTime.Seconds())
		writeGauge(&b, "cpra_queue_max_wait_seconds", "Max queue wait (s).", lbl, qs.MaxQueueTime.Seconds())
		writeGauge(&b, "cpra_queue_avg_job_latency_seconds", "Average job latency (s).", lbl, qs.AvgJobLatency.Seconds())
	}

	poolStats := map[string]queue.WorkerPoolStats{
		"pulse":        safePoolStats(s.pulsePool),
		"intervention": safePoolStats(s.intervPool),
		"code":         safePoolStats(s.codePool),
	}
	for name, ps := range poolStats {
		lbl := []kv{{"pool", name}}
		writeGauge(&b, "cpra_workers_running", "Running workers.", lbl, float64(ps.RunningWorkers))
		writeGauge(&b, "cpra_workers_capacity", "Worker pool capacity.", lbl, float64(ps.CurrentCapacity))
		writeGauge(&b, "cpra_workers_target", "Target worker count.", lbl, float64(ps.TargetWorkers))
		writeGauge(&b, "cpra_workers_waiting", "Waiting tasks.", lbl, float64(ps.WaitingTasks))
		writeCounter(&b, "cpra_tasks_submitted_total", "Total tasks submitted.", lbl, float64(ps.TasksSubmitted))
		writeCounter(&b, "cpra_tasks_completed_total", "Total tasks completed.", lbl, float64(ps.TasksCompleted))
		writeCounter(&b, "cpra_scaling_events_total", "Total scaling events.", lbl, float64(ps.ScalingEvents))
	}

	if s.metrics != nil {
		agg := s.metrics.GetAggregateMetrics()
		writeCounter(&b, "cpra_system_updates_total", "Total system updates across all systems.", nil, float64(agg.TotalUpdates))
		writeCounter(&b, "cpra_system_entities_processed_total", "Total entities processed across all systems.", nil, float64(agg.TotalEntitiesProcessed))
		writeGauge(&b, "cpra_system_entities_per_second", "Aggregate entities processed per second.", nil, agg.EntitiesPerSecond)
		writeGauge(&b, "cpra_system_updates_per_second", "Aggregate system updates per second.", nil, agg.UpdatesPerSecond)

		all := s.metrics.GetAllMetrics()
		names := make([]string, 0, len(all))
		for n := range all {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			m := all[n]
			lbl := []kv{{"system", n}}
			avg := 0.0
			if m.TotalUpdates > 0 {
				avg = m.TotalDuration.Seconds() / float64(m.TotalUpdates)
			}
			writeGauge(&b, "cpra_system_update_duration_seconds_avg", "Average system update duration (s).", lbl, avg)
			writeGauge(&b, "cpra_system_update_duration_seconds_max", "Max system update duration (s).", lbl, m.MaxUpdateDuration.Seconds())
			writeGauge(&b, "cpra_system_update_duration_seconds_min", "Min system update duration (s).", lbl, m.MinUpdateDuration.Seconds())
			writeCounter(&b, "cpra_system_entities_processed_by_system_total", "Entities processed by system.", lbl, float64(m.TotalEntitiesProcessed))
			writeCounter(&b, "cpra_system_updates_by_system_total", "Updates by system.", lbl, float64(m.TotalUpdates))
		}
	}

	_, _ = w.Write([]byte(b.String()))
}

type kv struct{ k, v string }

type metricWriter struct {
	families map[string]*strings.Builder
}

func (b *metricWriter) add(name, help, kind string, lbls []kv, val float64) {
	if b.families == nil {
		b.families = make(map[string]*strings.Builder)
	}
	family := b.families[name]
	if family == nil {
		family = &strings.Builder{}
		b.families[name] = family
		fmt.Fprintf(family, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	}
	fmt.Fprintf(family, "%s%s %s\n", name, renderLabels(lbls), formatFloat(val))
}

func (b *metricWriter) String() string {
	names := make([]string, 0, len(b.families))
	for name := range b.families {
		names = append(names, name)
	}
	sort.Strings(names)
	var output strings.Builder
	for _, name := range names {
		output.WriteString(b.families[name].String())
	}
	return output.String()
}

func writeGauge(b *metricWriter, name, help string, lbls []kv, val float64) {
	b.add(name, help, "gauge", lbls, val)
}

func writeCounter(b *metricWriter, name, help string, lbls []kv, val float64) {
	b.add(name, help, "counter", lbls, val)
}

func renderLabels(lbls []kv) string {
	if len(lbls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lbls))
	for _, l := range lbls {
		parts = append(parts, fmt.Sprintf("%s=%q", l.k, l.v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
