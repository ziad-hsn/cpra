package httpauth

import (
	"net/http"
	"strings"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// AuthorizeObservation authenticates a named principal for an exact compatible
// v1 GET route. Mapping a read to its v2 permission does not expose v2 mutations
// through v1 or weaken transport/origin checks. Query values never enter errors.
func (a *Authorizer) AuthorizeObservation(r *http.Request) (api.AccessInfo, error) {
	if r == nil || r.URL == nil || r.Method != http.MethodGet {
		return api.AccessInfo{}, forbidden()
	}
	operation, path := "", ""
	switch r.URL.Path {
	case "/api/v1/history":
		operation, path = "GetHistory", "/api/v2/history"
	case "/api/v1/slo":
		operation, path = "GetSLO", "/api/v2/slo"
	case "/api/v1/state":
		operation, path = "GetState", "/api/v2/state"
	case "/api/v1/overview", "/api/v1/monitors":
		operation, path = "ListMonitors", "/api/v2/monitors"
	case "/api/v1/incidents":
		operation, path = "ListIncidents", "/api/v2/incidents"
	case "/api/v1/systems":
		operation, path = "GetSystems", "/api/v2/systems"
	case "/api/v1/queues", "/api/v1/queues/history":
		operation, path = "GetQueues", "/api/v2/queues"
	case "/api/v1/pools", "/api/v1/pools/history":
		operation, path = "GetPools", "/api/v2/pools"
	case "/api/v1/config":
		operation, path = "GetConfig", "/api/v2/config"
	case "/api/v1/healthz":
		operation, path = "GetLive", "/api/v2/healthz"
	case "/api/v1/readyz":
		operation, path = "GetReady", "/api/v2/readyz"
	case "/metrics":
		operation, path = "GetMetrics", "/api/v2/metrics"
	default:
		id, found := strings.CutPrefix(r.URL.Path, "/api/v1/monitors/")
		if !found || len(id) == 0 || len(id) > 10 {
			return api.AccessInfo{}, forbidden()
		}
		for _, c := range id {
			if c < '0' || c > '9' {
				return api.AccessInfo{}, forbidden()
			}
		}
		operation, path = "GetMonitor", "/api/v2/monitors/"+id
	}
	mapped := r.Clone(r.Context())
	mapped.URL.Path, mapped.URL.RawPath = path, ""
	return a.Authorize(mapped, operation)
}
