package httpserver

import (
	"context"
	"errors"
	"net/http"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	monitorID := r.URL.Query().Get("monitor_id")
	if strings.HasPrefix(monitorID, "collection/") || strings.HasPrefix(monitorID, "collection-activation/") ||
		strings.HasPrefix(monitorID, "collection-validation/") || strings.HasPrefix(monitorID, "collection-execution/") {
		writeErr(w, http.StatusBadRequest, "invalid history request")
		return
	}
	if s.cfg.Store == nil {
		writeErr(w, 503, "event history unavailable")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeErr(w, 400, "history limit must be between 1 and 500")
			return
		}
		limit = n
	}
	page, err := s.cfg.Store.History().PageContext(r.Context(), monitorID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		if errors.Is(err, persistence.ErrHistoryUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeErr(w, http.StatusServiceUnavailable, "event history unavailable")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid history request")
		}
		return
	}
	writeJSON(w, 200, page)
}
func (s *Server) handleSLO(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Store == nil {
		writeErr(w, 503, "SLO measurements unavailable")
		return
	}
	view, err := s.cfg.Store.SLOViewContext(r.Context(), time.Now())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "SLO measurements unavailable")
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Store == nil {
		writeErr(w, 503, "durable state unavailable")
		return
	}
	status, err := s.cfg.Store.StatusContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "durable state unavailable")
		return
	}
	usage, usageErr := s.cfg.Store.UsageContext(r.Context())
	if usageErr != nil && r.Context().Err() != nil {
		writeErr(w, http.StatusServiceUnavailable, "durable state unavailable")
		return
	}
	measurements := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/sched/goroutines:goroutines"}}
	metrics.Read(measurements)
	response := map[string]any{"storage": status, "storage_usage": usage, "process": map[string]uint64{"heap_bytes": measurements[0].Value.Uint64(), "goroutines": measurements[1].Value.Uint64()}, "actions": []persistence.Action{}}
	if id := r.URL.Query().Get("monitor_id"); id != "" {
		m, ok, err := s.cfg.Store.GetContext(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "durable state unavailable")
			return
		}
		if !ok {
			writeErr(w, 404, "monitor not found")
			return
		}
		actions := make([]persistence.Action, 0, len(m.Actions))
		for _, a := range m.Actions {
			a.AgentConnection = nil // Internal ciphertext and key identities are not an observation API.
			actions = append(actions, a)
		}
		slices.SortFunc(actions, func(a, b persistence.Action) int { return strings.Compare(a.ID, b.ID) })
		response["monitor_id"], response["revision"], response["actions"] = id, m.Revision, actions
		if m.LastFailure.Code != "" {
			response["last_failure"] = m.LastFailure
		}
		if m.ConfigurationProblem.Code != "" {
			response["configuration_problem"] = m.ConfigurationProblem
			response["configuration_problem_at"] = m.ConfigurationProblemAt.UTC()
		}
	}
	writeJSON(w, 200, response)
}

func (s *Server) handleIndexedMonitors(w http.ResponseWriter, r *http.Request, index *fleetview.Index) {
	q := r.URL.Query()
	filters := map[string]string{}
	for _, key := range []string{"status", "type", "code", "q"} {
		if v := strings.ToLower(strings.TrimSpace(q.Get(key))); v != "" {
			filters[key] = v
		}
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(q.Get("size"))
	if size <= 0 {
		size = 50
	}
	size = min(size, 500)
	var match func(fleetview.MonitorSummary) bool
	if len(filters) > 0 {
		match = func(m fleetview.MonitorSummary) bool {
			return (filters["status"] == "" || m.Status == filters["status"]) && (filters["type"] == "" || m.PulseType == filters["type"]) && (filters["code"] == "" || m.PendingCode == filters["code"]) && (filters["q"] == "" || strings.Contains(strings.ToLower(m.Name), filters["q"]))
		}
	}
	total := index.Overview().Total
	offset := total
	if page-1 <= total/size {
		offset = (page - 1) * size
	}
	rows, count := index.Page(offset, size, match)
	writeJSON(w, 200, monitorsResp{Generated: index.Overview().Generated, Page: page, Size: size, Total: count, Filters: filters, Monitors: rows})
}
