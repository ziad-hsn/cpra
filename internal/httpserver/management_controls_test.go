package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Configure only serializable policy and submit observations directly. No job,
// worker or provider is constructed by these real-TLS server contract tests.
func configureControlMonitor(t *testing.T, f *managementFixture, id string) (api.Resource, persistence.Monitor, persistence.CatalogGuard) {
	t.Helper()
	r := f.create(t, managementResource("Monitor", id, api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{
		Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://provider-not-invoked.invalid"}`)},
	}}))
	guard := persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: persistence.CatalogKey{Kind: "Monitor", ID: id}, UID: r.Metadata.UID, Revision: r.Metadata.ResourceVersion}}}
	m := persistence.Monitor{ID: id, CatalogUID: r.Metadata.UID, Revision: "execution-" + id, Name: id,
		Policy: persistence.Policy{Interval: time.Minute, Enabled: true, Unhealthy: 1, Healthy: 1, Endpoints: map[string]int{"red": 1}}}
	results, err := f.server.cfg.Store.Submit(context.Background(), []persistence.Command{{Kind: "configure", MonitorID: id, Revision: m.Revision, At: time.Now().UTC(), Config: &m, Guard: &guard}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatalf("configure: %+v %v", results, err)
	}
	return r, *results[0].Monitor, guard
}

func controlPulse(t *testing.T, f *managementFixture, m persistence.Monitor, guard persistence.CatalogGuard, outcome string) persistence.Monitor {
	t.Helper()
	at := time.Now().UTC()
	results, err := f.server.cfg.Store.Submit(context.Background(), []persistence.Command{{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, At: at,
		Generation: m.Generation + 1, CheckControlRevision: m.ControlRevision, Outcome: outcome, Guard: &guard, ExecutionStart: at.Add(-12 * time.Millisecond), ExecutionEnd: at}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
		t.Fatalf("pulse: %+v %v", results, err)
	}
	return *results[0].Monitor
}

func TestManagementIncidentControlsRealSDK(t *testing.T) {
	f := newManagementFixture(t, true)
	_, monitor, guard := configureControlMonitor(t, f, "service")
	monitor = controlPulse(t, f, monitor, guard, "failure")
	if monitor.IncidentID == "" {
		t.Fatal("incident identity not committed")
	}
	ctx := context.Background()
	got, err := f.sdk.Incidents.Get(ctx, monitor.IncidentID)
	if err != nil || got.Data.State != "open" || got.Data.Revision == "" {
		t.Fatalf("get incident: %+v %v", got, err)
	}
	initial := got.Data.Revision
	ack, err := f.sdk.Incidents.Acknowledge(ctx, monitor.IncidentID, api.ControlRequest{Revision: initial, Note: "Investigating the deployment"})
	if err != nil || ack.Data.AcknowledgedBy != "operator" || ack.Data.AcknowledgedAt.IsZero() || ack.OperationID == "" {
		t.Fatalf("acknowledge: %+v %v", ack, err)
	}
	receipt, err := f.catalog.Operation(ctx, ack.OperationID)
	if err != nil || receipt.State != "committed" || (receipt.Applied == nil || *receipt.Applied != 0) {
		t.Fatalf("premature owner completion: %+v %v", receipt, err)
	}
	if _, err := f.sdk.Incidents.Dismiss(ctx, monitor.IncidentID, api.ControlRequest{Revision: initial, Reason: "Expected"}); err == nil {
		t.Fatal("stale triage version accepted")
	}
	dismissed, err := f.sdk.Incidents.Dismiss(ctx, monitor.IncidentID, api.ControlRequest{Revision: ack.Data.Revision, Reason: "Known deployment issue"})
	if err != nil || !dismissed.Data.Dismissed || dismissed.Data.AcknowledgedBy != "operator" {
		t.Fatalf("dismiss: %+v %v", dismissed, err)
	}
	current, _ := f.server.cfg.Store.Get(monitor.ID)
	for _, action := range current.Actions {
		if action.Kind == "code" && action.State == persistence.Queued {
			t.Fatal("dismissed notification remained queued")
		}
	}
	reopened, err := f.sdk.Incidents.Reopen(ctx, monitor.IncidentID, api.ControlRequest{Revision: dismissed.Data.Revision})
	if err != nil || reopened.Data.Dismissed || reopened.Data.AcknowledgedBy != "operator" {
		t.Fatalf("reopen: %+v %v", reopened, err)
	}
	current, _ = f.server.cfg.Store.Get(monitor.ID)
	for _, action := range current.Actions {
		if action.Kind == "code" && action.State == persistence.Queued {
			t.Fatal("reopen replayed cancelled notification")
		}
	}
	current = controlPulse(t, f, current, guard, "success")
	closed, err := f.sdk.Incidents.Get(ctx, monitor.IncidentID)
	if err != nil || closed.Data.State != "closed" || closed.Data.ClosedAt.IsZero() {
		t.Fatalf("closed incident: %+v %v", closed, err)
	}
	if _, err := f.sdk.Incidents.Reopen(ctx, monitor.IncidentID, api.ControlRequest{Revision: closed.Data.Revision}); err == nil {
		t.Fatal("closed incident reopened")
	}
	page, err := f.sdk.Incidents.List(ctx, cpra.ListOptions{MonitorID: monitor.ID})
	if err != nil || len(page.Data.Items) != 1 || page.Data.Items[0].State != "closed" {
		t.Fatalf("monitor incident index: %+v %v", page, err)
	}
}

func TestManagementSnoozeVersionAndOperationContract(t *testing.T) {
	f := newManagementFixture(t, true)
	resource, monitor, _ := configureControlMonitor(t, f, "service")
	ctx := context.Background()
	got, err := f.sdk.Monitors.Get(ctx, monitor.ID)
	if err != nil || got.Data.Status.ControlRevision == "" || got.Data.Status.LastCheckLatencyMS.Available {
		t.Fatalf("status: %+v %v", got, err)
	}
	if got.Data.Status.ControlRevision == resource.Metadata.ResourceVersion {
		t.Fatal("control version reused catalog resource version")
	}
	if got.Data.Status.ObservedGeneration != 0 {
		t.Fatal("configure commit falsely established owner application")
	}
	initial := got.Data.Status.ControlRevision
	response, err := f.sdk.Monitors.Snooze(ctx, monitor.ID, api.ControlRequest{Revision: initial, Duration: "10m", Reason: "Database maintenance"})
	if err != nil || response.Data.State != "committed" || (response.Data.Applied == nil || *response.Data.Applied != 0) || response.OperationID != response.Data.ID {
		t.Fatalf("snooze: %+v %v", response, err)
	}
	got, err = f.sdk.Monitors.Get(ctx, monitor.ID)
	if err != nil || got.Data.Status.ControlRevision == initial || !got.Data.Status.SnoozedUntil.After(time.Now()) || got.Data.Metadata.ResourceVersion != resource.Metadata.ResourceVersion {
		t.Fatalf("snoozed status: %+v %v", got, err)
	}
	if err := f.catalog.CompleteOperation(ctx, response.Data.ID, true); err != nil {
		t.Fatal(err)
	}
	operation, err := f.sdk.Operations.Get(ctx, response.Data.ID)
	if err != nil || (operation.Data.Applied == nil || *operation.Data.Applied != 1) || operation.Data.State != "completed" {
		t.Fatalf("owner completion: %+v %v", operation, err)
	}
	if _, err := f.sdk.Monitors.Unsnooze(ctx, monitor.ID, api.ControlRequest{Revision: initial}); err == nil {
		t.Fatal("stale unsnooze accepted")
	}
	resumed, err := f.sdk.Monitors.Unsnooze(ctx, monitor.ID, api.ControlRequest{Revision: got.Data.Status.ControlRevision})
	if err != nil || resumed.Data.State != "committed" {
		t.Fatalf("unsnooze: %+v %v", resumed, err)
	}
	got, err = f.sdk.Monitors.Get(ctx, monitor.ID)
	if err != nil || !got.Data.Status.SnoozedUntil.IsZero() {
		t.Fatalf("unsnooze status: %+v %v", got, err)
	}
}

func TestManagementControlsRejectInvalidAndUnauthorizedRequests(t *testing.T) {
	f := newManagementFixture(t, true)
	_, monitor, _ := configureControlMonitor(t, f, "service")
	version := monitor.ControlRevision
	valid := `{"revision":"` + version + `","duration":"1m","reason":"maintenance"}`
	for _, test := range []struct {
		name, token, body string
		headers           map[string]string
		status            int
	}{
		{"reader", managementReaderToken, valid, map[string]string{"If-Match": `"` + version + `"`}, 403},
		{"missing precondition", managementOperatorToken, valid, nil, 428},
		{"weak precondition", managementOperatorToken, valid, map[string]string{"If-Match": `W/"` + version + `"`}, 400},
		{"raw precondition", managementOperatorToken, valid, map[string]string{"If-Match": version}, 400},
		{"mismatched body", managementOperatorToken, valid, map[string]string{"If-Match": `"another"`}, 400},
		{"forged actor", managementOperatorToken, strings.TrimSuffix(valid, "}") + `,"actor":"another-person"}`, map[string]string{"If-Match": `"` + version + `"`}, 400},
		{"duplicate revision", managementOperatorToken, strings.TrimSuffix(valid, "}") + `,"revision":"other"}`, map[string]string{"If-Match": `"` + version + `"`}, 400},
		{"missing reason", managementOperatorToken, `{"revision":"` + version + `","duration":"1m"}`, map[string]string{"If-Match": `"` + version + `"`}, 422},
		{"duration over 30 days", managementOperatorToken, strings.Replace(valid, "1m", "721h", 1), map[string]string{"If-Match": `"` + version + `"`}, 422},
		{"negative duration", managementOperatorToken, strings.Replace(valid, "1m", "-1s", 1), map[string]string{"If-Match": `"` + version + `"`}, 422},
		{"foreign origin", managementOperatorToken, valid, map[string]string{"If-Match": `"` + version + `"`, "Origin": "https://evil.invalid"}, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, raw := f.request(t, http.MethodPost, "/api/v2/monitors/service/snooze", test.token, []byte(test.body), test.headers)
			if response.StatusCode != test.status {
				t.Fatalf("%d: %s", response.StatusCode, raw)
			}
			current, _ := f.server.cfg.Store.Get(monitor.ID)
			if current.ControlRevision != version || !current.SnoozedUntil.IsZero() {
				t.Fatal("rejected control mutated state")
			}
		})
	}
	if err := f.server.StopAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, _ := f.request(t, http.MethodPost, "/api/v2/monitors/service/snooze", managementOperatorToken, []byte(valid), map[string]string{"If-Match": `"` + version + `"`})
	if response.StatusCode != 503 {
		t.Fatal("draining accepted a control", response.StatusCode)
	}
	response, _ = f.request(t, http.MethodGet, "/api/v2/monitors/service", managementOperatorToken, nil, nil)
	if response.StatusCode != 200 {
		t.Fatal("diagnostics lost during drain", response.StatusCode)
	}
}

func TestManagementIncidentPaginationFreezesAndBindsAccess(t *testing.T) {
	f := newManagementFixture(t, true)
	for _, id := range []string{"one", "two", "three"} {
		_, m, guard := configureControlMonitor(t, f, id)
		controlPulse(t, f, m, guard, "failure")
	}
	ctx := context.Background()
	first, err := f.sdk.Incidents.List(ctx, cpra.ListOptions{Limit: 1})
	if err != nil || len(first.Data.Items) != 1 || first.Data.NextCursor == "" {
		t.Fatalf("page one: %+v %v", first, err)
	}
	_, m, guard := configureControlMonitor(t, f, "later")
	controlPulse(t, f, m, guard, "failure")
	next, err := f.sdk.Incidents.List(ctx, cpra.ListOptions{Limit: 1, Cursor: first.Data.NextCursor})
	if err != nil || len(next.Data.Items) != 1 || next.Data.Items[0].MonitorID == "later" {
		t.Fatalf("frozen page: %+v %v", next, err)
	}
	again, err := f.sdk.Incidents.List(ctx, cpra.ListOptions{Limit: 1, Cursor: first.Data.NextCursor})
	if err != nil || again.Data.Items[0].ID != next.Data.Items[0].ID {
		t.Fatal("cursor retry changed", err)
	}
	path := "/api/v2/incidents?cursor=" + url.QueryEscape(first.Data.NextCursor) + "&limit=1"
	response, _ := f.request(t, http.MethodGet, path, managementReaderToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("cursor shared between principals", response.StatusCode)
	}
	response, _ = f.request(t, http.MethodGet, path+"&monitorID=later", managementOperatorToken, nil, nil)
	if response.StatusCode != 400 {
		t.Fatal("cursor changed filter", response.StatusCode)
	}
	response, _ = f.request(t, http.MethodGet, "/api/v2/incidents?selector=team%3Dops", managementOperatorToken, nil, nil)
	if response.StatusCode != 501 {
		t.Fatal("unsupported selector silently ignored", response.StatusCode)
	}
}
