package httpserver

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/internal/controller"
)

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
