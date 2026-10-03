//go:build externaljobs

package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func TestWorkerSLOHTTPAndPrometheusAvailability(t *testing.T) {
	for _, outcome := range []string{"success", "noData"} {
		t.Run(outcome, func(t *testing.T) {
			f := newWorkerSessionFixture(t)
			worker := workerHTTPClient(t, f)
			_, input := startWorkerHTTPCheck(t, f, worker)
			input.Status = outcome
			if _, err := worker.Result(t.Context(), input); err != nil {
				t.Fatal(err)
			}

			server := New(ServerConfig{Store: f.store, Management: f.handler.catalog, ManagementAuth: f.handler.auth,
				AuthToken: managementLegacyToken, Ready: func(context.Context) bool { return true }},
				fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
			mux := http.NewServeMux()
			server.registerAPI(mux)
			server.registerMetrics(mux)
			transport := httptest.NewTLSServer(server.corsMiddleware(server.authMiddleware(mux)))
			t.Cleanup(transport.Close)
			client, err := cpra.New(cpra.Config{BaseURL: transport.URL, AuthToken: managementOperatorToken, HTTPClient: transport.Client()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			view, err := client.SLO.Get(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !view.Data.Available || !view.Data.CoverageGap || len(view.Data.Reports) != 1 {
				t.Fatal(view.Data)
			}
			r := view.Data.Reports[0]
			wantSamples, wantMissed := int64(1), int64(0)
			if outcome == "noData" {
				wantSamples, wantMissed = 0, 1
			}
			if r.Driver != "external" || r.Pipeline != "pulse" || r.Expected != 1 || r.Samples != wantSamples || r.Missed != wantMissed ||
				r.QueueDelay.Available || r.Execution.Available || r.QueueAttainment.Available || r.TotalLatency.Available != (outcome == "success") || !r.ResultAttainment.Available {
				t.Fatal("source measurements changed at API boundary", r)
			}
			if outcome == "noData" && (r.ResultAttainment.Value != 0 || r.TotalThresholdPassed != 0) {
				t.Fatal("missing result improved attainment", r)
			}
			metrics, err := client.Metrics(t.Context())
			if err != nil || len(metrics.Data.SLO.Reports) != 1 || metrics.Data.SLO.Reports[0].Samples != wantSamples {
				t.Fatal("structured metrics disagree", err)
			}
			var prom bytes.Buffer
			if err := client.Prometheus(t.Context(), &prom); err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{
				`cpra_slo_window_expected{pipeline="pulse",driver="external"} 1`,
				fmt.Sprintf(`cpra_slo_window_samples{pipeline="pulse",driver="external"} %d`, wantSamples),
				fmt.Sprintf(`cpra_slo_window_missed{pipeline="pulse",driver="external"} %d`, wantMissed),
			} {
				if !strings.Contains(prom.String(), expected) {
					t.Fatal("missing metric", expected)
				}
			}
			for _, absent := range []string{
				`cpra_slo_queue_attainment_ratio{pipeline="pulse",driver="external"}`,
				`driver="external",stage="scheduling_queue"`, `driver="external",stage="execution"`, "private-diagnostic-canary", "private-schema-canary",
			} {
				if strings.Contains(prom.String(), absent) {
					t.Fatal("unavailable/private value exported", absent)
				}
			}
		})
	}
}
