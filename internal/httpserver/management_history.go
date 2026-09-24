package httpserver

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (m *managementHTTP) registerHistory(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v2/history", m.handleHistoryV2)
}

func (m *managementHTTP) handleHistoryV2(w http.ResponseWriter, r *http.Request) {
	managementHeaders(w)
	if _, err := m.auth.Authorize(r, "GetHistory"); err != nil {
		writeManagementError(w, err)
		return
	}
	query, err := historyListQuery(r)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var result api.EventList
	err = m.withSnapshotRead(r, "GetHistory", "History", query, func(ctx context.Context, read *snapshotRead) error {
		entry := &read.entry
		if read.fresh {
			view, err := m.catalog.HistorySnapshot()
			if err != nil {
				return err
			}
			entry.history = view
		}
		items, after, err := entry.history.Page(ctx, entry.monitorID, read.cursor.After, entry.limit)
		if err != nil {
			return err
		}
		items, after, err = boundSnapshotPage(items, after, func(item api.Event) string { return item.ID })
		read.after = after
		result = api.EventList{Items: items, GeneratedAt: entry.created, Snapshot: read.cursor.View}
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

func historyListQuery(r *http.Request) (url.Values, error) {
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
				return nil, managementFailure(501, "featureUnavailable", "Timeline label filtering is not enabled. Use monitorID for exact monitor selection.")
			}
		default:
			return nil, managementFailure(400, "invalidQuery", "The query contains an unsupported parameter.")
		}
	}
	if query.Get("monitorID") == "" {
		return nil, managementFailure(400, "monitorRequired", "Select one monitorID for its event timeline.")
	}
	return query, nil
}
