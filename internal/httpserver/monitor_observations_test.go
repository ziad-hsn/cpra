package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMonitorObservationSDKPagesAndUnavailableTimes(t *testing.T) {
	s := newTestServer(t)
	s.cfg.AuthToken = "monitor-observation-fixture-01234567890123456789"
	index := fleetview.NewIndex([]fleetview.MonitorSummary{
		{ID: 1, MonitorID: "a", IncarnationUID: "first", Name: "API", Source: "file", Health: "unhealthy", Phase: "incident", Enabled: true, Ignored: true, PendingNotifications: 1, CancelledNotifications: 3, UnknownActions: 2, ActiveCodes: []string{"red", "green"}},
		{ID: 2, MonitorID: "b", IncarnationUID: "old-b", Name: "Database", Source: "file", Health: "healthy", Phase: "idle", Enabled: false},
	})
	s.holder.SetIndex(index)
	server := httptest.NewServer(s.authMiddleware(apiMux(s)))
	defer server.Close()
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: s.cfg.AuthToken, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	first, err := client.MonitorObservations.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || len(first.Data.Items) != 1 || first.Data.NextCursor == "" {
		t.Fatal(first, err)
	}
	if item := first.Data.Items[0]; item.Health != api.Unhealthy || !item.Ignored || item.LastCheckedAt != nil || item.LastCheckLatencyMS.Available || item.UnknownActions != 2 {
		t.Fatal(item)
	}
	if count := first.Data.Items[0].CancelledNotifications; count == nil || *count != 3 {
		t.Fatal("retained cancellation count lost in SDK observation", count)
	}
	index.Remove(2)
	index.Put(fleetview.MonitorSummary{ID: 2, MonitorID: "b", IncarnationUID: "new-b", Source: "file", Health: "unknown", Phase: "idle"})
	second, err := client.MonitorObservations.List(t.Context(), cpra.ListOptions{Limit: 1, Cursor: first.Data.NextCursor})
	if err != nil || len(second.Data.Items) != 1 || second.Data.Items[0].IncarnationUID != "old-b" || second.Data.NextCursor != "" {
		t.Fatal(second, err)
	}
	current, err := client.MonitorObservations.Get(t.Context(), "b")
	if err != nil || current.Data.IncarnationUID != "new-b" || current.ResourceVersion != "" {
		t.Fatal(current, err)
	}
	raw, _ := json.Marshal(current.Data)
	for _, forbidden := range []string{"resourceVersion", "0001-01-01", "\"spec\"", "\"metadata\""} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("fabricated configuration or time", string(raw))
		}
	}
	// No unauthenticated cursor reuse; no selector silently ignored.
	request := httptest.NewRequest(http.MethodGet, "/api/v2/observations/monitors?cursor="+first.Data.NextCursor, nil)
	response := httptest.NewRecorder()
	s.authMiddleware(apiMux(s)).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code)
	}
	if _, err := client.MonitorObservations.List(t.Context(), cpra.ListOptions{Selector: "team=test"}); err == nil {
		t.Fatal("unsupported selector accepted")
	}
	// Temporal diagnostics are UTC and omitted when absent.
	item := monitorObservation(fleetview.MonitorSummary{LastCheck: time.Date(2026, 10, 17, 12, 0, 0, 0, time.FixedZone("local", 7200)), LastFailure: fleetview.FailureDiagnostic{Code: "unexpected_http_status", Message: "Unexpected status.", HTTPStatus: 503}})
	if item.LastCheckedAt.Location() != time.UTC || *item.LastFailure.HTTPStatus != 503 {
		t.Fatal(item)
	}
}

func TestMonitorObservationProgressIsFrozenWithCursor(t *testing.T) {
	s := newTestServer(t)
	s.cfg.AuthToken = "progress-fixture-012345678901234567890123456789"
	at := time.Now().UTC().Add(-time.Minute)
	s.holder.SetIndex(fleetview.NewIndex([]fleetview.MonitorSummary{
		{ID: 1, MonitorID: "a", Enabled: true, Health: "healthy", LastObservedHealth: "healthy", LastObservedAt: at, LastCheck: at},
		{ID: 2, MonitorID: "b", Enabled: true, Health: "healthy", LastObservedHealth: "healthy", LastObservedAt: at, LastCheck: at},
	}))
	var ready atomic.Bool
	s.holder.SetProgress(ready.Load)
	server := httptest.NewServer(s.authMiddleware(apiMux(s)))
	defer server.Close()
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: s.cfg.AuthToken, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	first, err := client.MonitorObservations.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || len(first.Data.Items) != 1 || first.Data.Items[0].Health != "unknown" || first.Data.Items[0].LastObservedHealth != "healthy" || !first.Data.Items[0].LastObservedAt.Equal(at) {
		t.Fatal(first, err)
	}
	ready.Store(true)
	second, err := client.MonitorObservations.List(t.Context(), cpra.ListOptions{Limit: 1, Cursor: first.Data.NextCursor})
	if err != nil || len(second.Data.Items) != 1 || second.Data.Items[0].Health != "unknown" || !second.Data.Items[0].Availability.ObservedAt.Equal(*first.Data.Items[0].Availability.ObservedAt) {
		t.Fatal("retained page changed", second, err)
	}
	fresh, err := client.MonitorObservations.Get(t.Context(), "b")
	if err != nil || fresh.Data.Health != "healthy" {
		t.Fatal("current read stayed stale", fresh, err)
	}
}
