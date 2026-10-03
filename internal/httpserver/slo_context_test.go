package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestSLOHTTPAndScrapeRejectCanceledRequest(t *testing.T) {
	config := runtimeconfig.Default()
	config.Storage.Mode = "memory"
	store, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := newTestServer(t)
	server.cfg.Store = store
	for _, route := range []struct {
		path    string
		handler http.HandlerFunc
	}{{"/api/v1/slo", server.handleSLO}, {"/metrics", server.handleMetrics}} {
		t.Run(route.path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			response := httptest.NewRecorder()
			route.handler(response, httptest.NewRequest("GET", route.path, nil).WithContext(ctx))
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "cpra_") {
				t.Fatalf("cancelled request returned observations: %d %s", response.Code, response.Body.String())
			}
			response = httptest.NewRecorder()
			route.handler(response, httptest.NewRequest("GET", route.path, nil))
			if response.Code != http.StatusOK {
				t.Fatal("cancellation changed later scrape availability", response.Code)
			}
		})
	}
}
