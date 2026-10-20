package httpserver

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/internal/controller"
	"github.com/ziad-hsn/cpra/internal/queue"
)

func TestDriverMetricsExposeBoundedCapacityAndSeconds(t *testing.T) {
	s := newTestServer(t)
	s.pulseQueue = &observationQueue{value: queue.Stats{Drivers: []queue.DriverQueueStats{{Driver: "http", RequestedCapacity: 12, ReservedCapacity: 4, Pending: 3, Active: 4, OldestPendingAge: 1500 * time.Millisecond, OldestActiveAge: 2500 * time.Millisecond, Rejections: 8, Timeouts: 2}, {Driver: "tcp"}}}}
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, sample := range []string{
		`cpra_driver_requested_capacity{pipeline="pulse",driver="http"} 12`,
		`cpra_driver_reserved_capacity{pipeline="pulse",driver="http"} 4`,
		`cpra_driver_oldest_pending_seconds{pipeline="pulse",driver="http"} 1.5`,
		`cpra_driver_oldest_inflight_seconds{pipeline="pulse",driver="http"} 2.5`,
		`cpra_driver_timeouts_total{pipeline="pulse",driver="http"} 2`,
	} {
		if !strings.Contains(body, sample+"\n") {
			t.Errorf("missing bounded diagnostic %s", sample)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "cpra_driver_") {
			if strings.Count(line, "=") != 2 || !strings.Contains(line, `{pipeline="pulse",driver=`) {
				t.Fatal("driver metric has unbounded labels", line)
			}
			if strings.Contains(line, "oldest_") && strings.Contains(line, `driver="tcp"`) {
				t.Fatal("idle driver fabricated timing")
			}
		}
	}
}

func TestPrometheusLabelsUseWireEscapes(t *testing.T) {
	name := "controller\t\r\x00\\\"\n\u0085世界"
	expected := "{system=\"controller\t\r\x00\\\\\\\"\\n\u0085世界\"}"
	if got := renderLabels([]kv{{"system", name}}); got != expected {
		t.Fatalf("label does not follow the Prometheus wire escaping contract: %q", got)
	}
	s := newTestServer(t)
	s.metrics = controller.NewMetricsAggregator()
	s.metrics.RegisterSystem(name)
	response := httptest.NewRecorder()
	s.handleMetrics(response, httptest.NewRequest("GET", "/metrics", nil))
	if !utf8.Valid(response.Body.Bytes()) || !strings.Contains(response.Body.String(), "cpra_system_updates_by_system_total"+expected+" 0\n") {
		t.Fatal("system label was lost or escaped incorrectly in the real scrape")
	}
	// Capturable verification evidence for an independent Prometheus parser;
	// ordinary non-verbose tests do not print the synthetic scrape.
	t.Logf("prometheus_fixture_base64=%s", base64.StdEncoding.EncodeToString(response.Body.Bytes()))
}
