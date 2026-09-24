package httpserver

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (m *managementHTTP) registerControls(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v2/incidents", m.handleIncidents)
	mux.HandleFunc("GET /api/v2/incidents/{id}", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		var incident api.Incident
		err := m.withObservationRead(r, "GetIncident", func(ctx context.Context) error {
			var err error
			incident, err = m.catalog.Incident(ctx, r.PathValue("id"))
			return err
		})
		if err != nil {
			writeManagementError(w, err)
			return
		}
		incidentHeaders(w, incident)
		writeManagementJSON(w, http.StatusOK, incident)
	})
	for _, route := range []struct{ path, action, operation string }{
		{"/api/v2/incidents/{id}/acknowledge", "acknowledge", "AcknowledgeIncident"},
		{"/api/v2/incidents/{id}/dismiss", "dismiss", "DismissIncident"},
		{"/api/v2/incidents/{id}/reopen", "reopen", "ReopenIncident"},
		{"/api/v2/monitors/{id}/snooze", "snooze", "SnoozeMonitor"},
		{"/api/v2/monitors/{id}/unsnooze", "unsnooze", "UnsnoozeMonitor"},
	} {
		mux.HandleFunc("POST "+route.path, func(w http.ResponseWriter, r *http.Request) { m.handleControl(w, r, route.action, route.operation) })
	}
}

func incidentHeaders(w http.ResponseWriter, incident api.Incident) {
	w.Header().Set("ETag", `"`+incident.Revision+`"`)
	w.Header().Set("X-Resource-Version", incident.Revision)
}

func (m *managementHTTP) handleControl(w http.ResponseWriter, r *http.Request, action, operation string) {
	managementHeaders(w)
	if _, err := m.auth.Authorize(r, operation); err != nil {
		writeManagementError(w, err)
		return
	}
	if !m.ready() {
		writeManagementError(w, mutationUnavailable())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	select {
	case m.mutations <- struct{}{}:
		defer func() { <-m.mutations }()
	default:
		w.Header().Set("Retry-After", "1")
		writeManagementError(w, managementFailure(429, "admissionBusy", "Management validation capacity is busy. Retry after the stated interval."))
		return
	}
	version, err := managementVersionPrecondition(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	raw, err := managementBody(w, r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	defer clear(raw)
	var request api.ControlRequest
	if api.StrictDecode(raw, &request) != nil {
		writeManagementError(w, managementFailure(400, "invalidControl", "The body must contain one control request without duplicate or unknown fields."))
		return
	}
	if request.Revision != version {
		writeManagementError(w, managementFailure(400, "preconditionMismatch", "The body revision must equal the strong If-Match revision."))
		return
	}
	prepared, err := m.catalog.PrepareControl(ctx, action, r.PathValue("id"), request)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var result management.ControlResult
	err = m.admit(ctx, func() error {
		return m.auth.WithAdmission(r, operation, func(access api.AccessInfo) error {
			var err error
			result, err = m.catalog.CommitControlAs(ctx, prepared, access.PrincipalID)
			if id := prepared.OperationID(); id != "" {
				w.Header().Set("X-Operation-ID", id)
			}
			return err
		})
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	w.Header().Set("X-CPRa-Admission", "committed")
	w.Header().Set("X-Commit-Index", strconv.FormatUint(result.CommittedIndex, 10))
	if action == "snooze" || action == "unsnooze" {
		writeManagementJSON(w, http.StatusOK, result.Operation)
		return
	}
	incidentHeaders(w, result.Incident)
	writeManagementJSON(w, http.StatusOK, result.Incident)
}

func (m *managementHTTP) handleIncidents(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	if _, err := m.auth.Authorize(r, "ListIncidents"); err != nil {
		writeManagementError(w, err)
		return
	}
	query, err := incidentListQuery(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var result api.IncidentList
	err = m.withSnapshotRead(r, "ListIncidents", "Incident", query, func(ctx context.Context, read *snapshotRead) error {
		entry := &read.entry
		if read.fresh {
			view, err := m.catalog.IncidentSnapshot()
			if err != nil {
				return err
			}
			entry.incidents = view
		}
		items, after, err := entry.incidents.Page(ctx, entry.monitorID, read.cursor.After, entry.limit)
		if err != nil {
			return err
		}
		items, after, err = boundSnapshotPage(items, after, func(item api.Incident) string { return item.ID })
		read.after = after
		result = api.IncidentList{Items: items, GeneratedAt: entry.created, Snapshot: read.cursor.View}
		if after != "" {
			result.NextCursor = m.encodeCursor(managementCursor{View: read.cursor.View, After: after})
		}
		return err
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	writeManagementJSON(w, http.StatusOK, result)
}

func incidentListQuery(r *http.Request) (url.Values, error) {
	if len(r.URL.RawQuery) > 4096 {
		return nil, managementFailure(400, "invalidQuery", "The query is too large.")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, managementFailure(400, "invalidQuery", "The query is invalid.")
	}
	for key, values := range query {
		if len(values) != 1 {
			return nil, managementFailure(400, "invalidQuery", "Duplicate query parameters are not accepted.")
		}
		switch key {
		case "cursor", "limit", "monitorID":
		case "selector":
			if values[0] != "" {
				return nil, managementFailure(501, "featureUnavailable", "Incident label filtering is not enabled. Use monitorID for exact monitor selection.")
			}
		default:
			return nil, managementFailure(400, "invalidQuery", "The query contains an unsupported parameter.")
		}
	}
	return query, nil
}
