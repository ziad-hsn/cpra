package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisabledManagementRoutesDoNotServeDashboard(t *testing.T) {
	s := newTestServer(t)
	s.cfg.AuthToken = "route-test-token"
	mux := apiMux(s)
	s.registerSPA(mux)
	server := httptest.NewServer(s.authMiddleware(mux))
	t.Cleanup(server.Close)
	for _, path := range []string{"/api/v2/monitors", "/api/v2/job-types", "/api/v2/external-workers/poll", "/api/v2/external-workers/start", "/api/v2/external-workers/late-evidence"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+s.cfg.AuthToken)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("disabled route %s returned %d", path, response.StatusCode)
		}
	}
}
