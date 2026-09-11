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
				if r.URL.Path != "/api/v1/readyz" || r.Header.Get("Authorization") != "Bearer probe-token" {
					t.Errorf("incorrect probe request: %s", r.URL.Path)
					w.WriteHeader(401)
					return
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()
			cmd, _ := newTestCommand(t, srv.URL, "ready")
			if err := cmd.Execute(); (err == nil) != (status == 200) {
				t.Fatalf("status %d error %v", status, err)
			}
		})
	}
}
