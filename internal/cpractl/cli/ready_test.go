package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
