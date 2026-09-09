package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaginationOverflow(t *testing.T) {
	s := newTestServer(t)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("valid positive page must not panic: %v", r)
		}
	}()
	req := httptest.NewRequest("GET", "/api/v1/monitors?page=9223372036854775807&size=50", nil)
	rec := httptest.NewRecorder()
	s.handleMonitorsList(rec, req)
}

func TestMetricsUseUniqueMetadataAndGroupedFamilies(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest("GET", "/metrics", nil))
	helps, types := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "#" {
			continue
		}
		var seen map[string]bool
		switch fields[1] {
		case "HELP":
			seen = helps
		case "TYPE":
			seen = types
		default:
			continue
		}
		if seen[fields[2]] {
			t.Fatalf("duplicate metric metadata: %s", line)
		}
		seen[fields[2]] = true
	}
	if len(helps) == 0 || len(helps) != len(types) {
		t.Fatal("missing metric metadata")
	}
	for _, name := range []string{"cpra_queue_depth", "cpra_workers_capacity"} {
		if count := strings.Count(rec.Body.String(), name+"{"); count != 3 {
			t.Fatalf("metric %s lost series: %d", name, count)
		}
	}
}

func TestCappedIncidentCount(t *testing.T) {
	s := newTestServer(t)
	snap := s.holder.Get()
	snap.Total = 1000001
	snap.ByStatus = map[string]int{"incident": 7, "up": 999994}
	snap.Monitors = nil
	snap.ByID = nil
	rec := httptest.NewRecorder()
	s.handleIncidents(rec, httptest.NewRequest("GET", "/api/v1/incidents", nil))
	t.Logf("active incidents from aggregates=%d HTTP=%d response=%s", snap.ByStatus["incident"], rec.Code, rec.Body.String())
	if rec.Code != 503 {
		t.Error("capped index must report unavailable")
	}
	rec = httptest.NewRecorder()
	s.handleMonitorsList(rec, httptest.NewRequest("GET", "/api/v1/monitors", nil))
	if rec.Code != 503 {
		t.Fatal("capped monitors reported a complete empty list")
	}
}
