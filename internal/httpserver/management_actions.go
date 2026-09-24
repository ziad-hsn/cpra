package httpserver

import (
	"context"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (m *managementHTTP) registerActions(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v2/actions", m.handleActions)
	mux.HandleFunc("GET /api/v2/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		managementHeaders(w)
		var action api.Action
		err := m.withObservationRead(r, "GetAction", func(ctx context.Context) error {
			var err error
			action, err = m.catalog.Action(ctx, r.PathValue("id"))
			return err
		})
		if err != nil {
			writeManagementError(w, err)
			return
		}
		actionHeaders(w, action)
		writeManagementJSON(w, http.StatusOK, action)
	})
	mux.HandleFunc("POST /api/v2/actions/{id}/review", func(w http.ResponseWriter, r *http.Request) { m.handleActionControl(w, r, "review", "ReviewAction") })
	mux.HandleFunc("POST /api/v2/monitors/{id}/recover", func(w http.ResponseWriter, r *http.Request) {
		m.handleActionControl(w, r, "recover", "RecoverMonitor")
	})
}
func actionHeaders(w http.ResponseWriter, action api.Action) {
	w.Header().Set("ETag", `"`+action.ReviewRevision+`"`)
	w.Header().Set("X-Resource-Version", action.ReviewRevision)
}

func (m *managementHTTP) handleActionControl(w http.ResponseWriter, r *http.Request, action, operation string) {
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
	prepared, err := m.catalog.PrepareAction(ctx, action, r.PathValue("id"), request)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var result management.ActionResult
	err = m.admit(ctx, func() error {
		return m.auth.WithAdmission(r, operation, func(access api.AccessInfo) error {
			var err error
			result, err = m.catalog.CommitActionAs(ctx, prepared, access.PrincipalID)
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
	if action == "recover" {
		writeManagementJSON(w, http.StatusOK, result.Operation)
		return
	}
	actionHeaders(w, result.Action)
	writeManagementJSON(w, http.StatusOK, result.Action)
}

func (m *managementHTTP) handleActions(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	if _, err := m.auth.Authorize(r, "ListActions"); err != nil {
		writeManagementError(w, err)
		return
	}
	query, err := actionListQuery(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	var result api.ActionList
	err = m.withSnapshotRead(r, "ListActions", "Action", query, func(ctx context.Context, read *snapshotRead) error {
		entry := &read.entry
		if read.fresh {
			view, err := m.catalog.ActionSnapshot()
			if err != nil {
				return err
			}
			entry.actions = view
		}
		items, after, err := entry.actions.Page(ctx, entry.monitorID, read.cursor.After, entry.limit)
		if err != nil {
			return err
		}
		items, after, err = boundSnapshotPage(items, after, func(item api.Action) string { return item.ID })
		read.after = after
		result = api.ActionList{Items: items, GeneratedAt: entry.created, Snapshot: read.cursor.View}
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

func actionListQuery(r *http.Request) (url.Values, error) {
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
				return nil, managementFailure(501, "featureUnavailable", "Action label filtering is not enabled. Use monitorID for exact monitor selection.")
			}
		default:
			return nil, managementFailure(400, "invalidQuery", "The query contains an unsupported parameter.")
		}
	}
	return query, nil
}
