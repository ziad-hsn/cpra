package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeRequestFailuresDoNotAssertServerHealth(t *testing.T) {
	for _, command := range []string{"health", "ready"} {
		for _, status := range []int{401, 403, 429} {
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "title": http.StatusText(status)})
			})
			_, _, err := fixture.run(t, nil, command)
			if err == nil || !strings.Contains(err.Error(), "query failed") || strings.Contains(err.Error(), "server unhealthy") || strings.Contains(err.Error(), "server not ready") {
				t.Fatalf("%s status %d: %v", command, status, err)
			}
		}
	}
}

func TestReadyUsesAuthenticatedReadiness(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("CPRA_AUTH_TOKEN", "probe-token")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/readyz" || r.Header.Get("Authorization") != "Bearer probe-token" {
					t.Errorf("incorrect probe request: %s", r.URL.Path)
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"available":true}`))
			}))
			defer srv.Close()
			cmd, _ := newTestCommand(t, srv.URL, "--allow-insecure-http", "ready")
			if err := cmd.Execute(); (err == nil) != (status == 200) {
				t.Fatalf("status %d error %v", status, err)
			}
		})
	}
}
