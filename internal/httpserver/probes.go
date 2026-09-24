package httpserver

import (
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// registerProbes provides the current SDK's probes in token-only deployments.
// The outer authentication middleware remains authoritative. A configured
// management pair registers these paths with its own scoped authorization.
func (s *Server) registerProbes(mux *http.ServeMux) {
	if s.cfg.Management != nil || s.cfg.ManagementAuth != nil {
		return
	}
	for _, endpoint := range []string{"healthz", "readyz"} {
		mux.HandleFunc("GET /api/v2/"+endpoint, func(w http.ResponseWriter, r *http.Request) {
			managementHeaders(w)
			if err := r.Context().Err(); err != nil {
				writeManagementError(w, err)
				return
			}
			if r.URL.RawQuery != "" {
				writeManagementError(w, managementFailure(http.StatusBadRequest, "invalidQuery", "This aggregate observation does not accept query parameters."))
				return
			}
			now := time.Now().UTC()
			if endpoint == "readyz" {
				ready, _, _, reason := s.observationReadiness(now)
				if !ready {
					writeManagementError(w, managementFailure(http.StatusServiceUnavailable, "notReady", reason))
					return
				}
			}
			writeManagementJSON(w, http.StatusOK, api.Health{Available: true, GeneratedAt: now})
		})
	}
}
