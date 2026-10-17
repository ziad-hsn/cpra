package httpserver

import (
	"context"
	"net/http"
	"sort"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type retainedObservations interface {
	Incident(context.Context, string) (api.Incident, error)
	IncidentSnapshotContext(context.Context) (management.IncidentView, error)
	Action(context.Context, string) (api.Action, error)
	ActionSnapshotContext(context.Context) (management.ActionView, error)
	HistorySnapshotContext(context.Context) (management.HistoryView, error)
}

type observationIdentityKey struct{}

// Only the outer authentication middleware can attach this identity. It is
// checked anew on every request; a cursor is never authentication evidence.
func observationRequest(r *http.Request, principal string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), observationIdentityKey{}, principal))
}

func observationPermissions() []string {
	permissions := []string{"GetAccess", "GetCapabilities", "GetVersion", "ExplainResource", "GetHistory", "ListIncidents", "GetIncident", "ListActions", "GetAction", "ListMonitorObservations", "GetMonitorObservation"}
	for _, route := range observationRoutes {
		permissions = append(permissions, route.operation)
	}
	sort.Strings(permissions)
	return permissions
}

func (m *managementHTTP) registerObservationDiscovery(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v2/self", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		access, err := m.authorizeRead(r, "GetAccess")
		if err != nil {
			writeManagementError(w, err)
			return
		}
		writeManagementJSON(w, http.StatusOK, access)
	})
	mux.HandleFunc("GET /api/v2/discovery", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		if _, err := m.authorizeRead(r, "GetCapabilities"); err != nil {
			writeManagementError(w, err)
			return
		}
		result := api.Capabilities{APIVersions: []string{"v1", "v2"}, PatchTypes: []string{}, Drivers: map[string][]string{"check": {}, "recovery": {}, "notification": {}},
			Resources: []string{"Version", "Schema", "Incident", "Event", "Action", "MonitorObservation"}, ResourceOperations: map[string][]string{
				"MonitorObservation": {"ListMonitorObservations", "GetMonitorObservation"}, "Version": {"GetVersion"}, "Schema": {"ExplainResource"}, "Incident": {"ListIncidents", "GetIncident"}, "Event": {"GetHistory"}, "Action": {"ListActions", "GetAction"},
			}}
		for _, capability := range jobs.Capabilities() {
			if capability.Available {
				result.Drivers[capability.Kind] = append(result.Drivers[capability.Kind], capability.Driver)
			}
		}
		for _, route := range observationRoutes {
			result.Resources = append(result.Resources, route.kind)
			result.ResourceOperations[route.kind] = []string{route.operation}
		}
		writeManagementJSON(w, http.StatusOK, result)
	})
}

func (m *managementHTTP) authorizeRead(r *http.Request, operation string) (api.AccessInfo, error) {
	if m.auth != nil {
		return m.auth.Authorize(r, operation)
	}
	principal, _ := r.Context().Value(observationIdentityKey{}).(string)
	if principal == "" {
		return api.AccessInfo{}, managementFailure(http.StatusUnauthorized, "unauthorized", "Authentication is required.")
	}
	permissions := observationPermissions()
	for _, permitted := range permissions {
		if permitted == operation {
			return api.AccessInfo{PrincipalID: principal, Role: "reader", Permissions: permissions}, r.Context().Err()
		}
	}
	return api.AccessInfo{}, managementFailure(http.StatusForbidden, "forbidden", "This operation is not available in observation mode.")
}

func (m *managementHTTP) withReadPolicy(r *http.Request, operation string, read func(api.AccessInfo, uint64) error) error {
	if m.auth != nil {
		return m.auth.WithPolicyAdmission(r, operation, read)
	}
	access, err := m.authorizeRead(r, operation)
	if err != nil {
		return err
	}
	// Shared-token and anonymous authority cannot rotate in a running file-mode
	// process. Restart replaces the cursor signing key and all retained views.
	return read(access, 1)
}
