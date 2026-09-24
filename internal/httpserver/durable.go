package httpserver

import (
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
	page, err := s.cfg.Store.History().Page(monitorID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		if errors.Is(err, persistence.ErrHistoryUnavailable) {
			writeErr(w, http.StatusServiceUnavailable, "event history unavailable")
		} else {
			writeErr(w, http.StatusBadRequest, "invalid history request")
		}
		return
	}
	writeJSON(w, 200, page)
}
func (s *Server) handleSLO(w http.ResponseWriter, _ *http.Request) {
	if s.cfg.Store == nil {
		writeErr(w, 503, "SLO measurements unavailable")
		return
	}
	writeJSON(w, 200, s.cfg.Store.SLOView(time.Now()))
}
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Store == nil {
		writeErr(w, 503, "durable state unavailable")
		return
	}
	measurements := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/sched/goroutines:goroutines"}}
	metrics.Read(measurements)
	response := map[string]any{"storage": s.cfg.Store.Status(), "storage_usage": s.cfg.Store.Usage(), "process": map[string]uint64{"heap_bytes": measurements[0].Value.Uint64(), "goroutines": measurements[1].Value.Uint64()}, "actions": []persistence.Action{}}
	if id := r.URL.Query().Get("monitor_id"); id != "" {
		m, ok := s.cfg.Store.Get(id)
		if !ok {
			writeErr(w, 404, "monitor not found")
			return
		}
		actions := make([]persistence.Action, 0, len(m.Actions))
		for _, a := range m.Actions {
			actions = append(actions, a)
		}
		slices.SortFunc(actions, func(a, b persistence.Action) int { return strings.Compare(a.ID, b.ID) })
		response["monitor_id"], response["revision"], response["actions"] = id, m.Revision, actions
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
