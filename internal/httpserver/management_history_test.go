package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestManagementHistoryRealSDKPreservesOriginalAudit(t *testing.T) {
	f := newManagementFixture(t, true)
	_, m, g := configureControlMonitor(t, f, "service")
	m = controlPulse(t, f, m, g, "failure")
	ctx := context.Background()
	ack, err := f.sdk.Incidents.Acknowledge(ctx, m.IncidentID, api.ControlRequest{Revision: m.IncidentRevision, Note: "Investigating deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sdk.Incidents.Dismiss(ctx, m.IncidentID, api.ControlRequest{Revision: ack.Data.Revision, Reason: "Known maintenance"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.sdk.History(ctx, cpra.ListOptions{MonitorID: m.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	ackSeen, dismissSeen, cancelSeen := false, false, false
	for _, event := range page.Data.Items {
		if seen[event.ID] || event.MonitorID != m.ID {
			t.Fatal("duplicate or wrong monitor event")
		}
		seen[event.ID] = true
		switch event.Kind {
		case "control_acknowledge":
			ackSeen = event.Actor == "operator" && event.Note == "Investigating deployment" && event.IncidentID == m.IncidentID
		case "control_dismiss":
			dismissSeen = event.Actor == "operator" && event.Reason == "Known maintenance" && event.IncidentID == m.IncidentID
		case "action_cancelled":
			_, ok := m.Actions[event.ActionID]
			cancelSeen = ok && event.Actor == "operator" && event.IncidentID == m.IncidentID && event.ActionKind == "code" && event.Color == "red" && event.Endpoint != nil && *event.Endpoint == 0 && event.Outcome == "dismiss" && event.ExecutionRevision == m.Revision
		}
		if strings.HasPrefix(event.Kind, "configuration_") && event.ExecutionRevision != "" {
			t.Fatal("catalog revision mislabeled as execution revision")
		}
	}
	if !ackSeen || !dismissSeen || !cancelSeen {
		t.Fatalf("audit fields lost: %+v", page.Data.Items)
	}
	raw, _ := json.Marshal(page.Data)
	for _, private := range []string{"provider-not-invoked.invalid", "operation\"", "catalog_uid", "policy\""} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private durable payload exposed", private)
		}
	}
	response, v1 := f.request(t, http.MethodGet, "/api/v1/history?monitor_id=service", managementOperatorToken, nil, nil)
	if response.StatusCode != 200 || !strings.Contains(string(v1), `"events"`) || !strings.Contains(string(v1), `"note":"Investigating deployment"`) {
		t.Fatal("v1 history contract changed", response.StatusCode, string(v1))
	}
}

func TestManagementHistoryCursorBindsPrincipalFilterAndRetention(t *testing.T) {
	f := newManagementFixture(t, true)
	_, m, g := configureControlMonitor(t, f, "service")
	m = controlPulse(t, f, m, g, "failure")
	ctx := context.Background()
	first, err := f.sdk.History(ctx, cpra.ListOptions{MonitorID: m.ID, Limit: 1})
	if err != nil || first.Data.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if _, err := f.sdk.Incidents.Acknowledge(ctx, m.IncidentID, api.ControlRequest{Revision: m.IncidentRevision, Note: "after initial snapshot"}); err != nil {
		t.Fatal(err)
	}
	second, err := f.sdk.History(ctx, cpra.ListOptions{MonitorID: m.ID, Limit: 1, Cursor: first.Data.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.sdk.History(ctx, cpra.ListOptions{MonitorID: m.ID, Limit: 1, Cursor: first.Data.NextCursor})
	if err != nil || again.Data.Items[0].ID != second.Data.Items[0].ID {
		t.Fatal("cursor retry changed", err)
	}
	path := "/api/v2/history?monitorID=service&limit=1&cursor=" + url.QueryEscape(first.Data.NextCursor)
	for _, test := range []struct {
		path, token string
		status      int
	}{
		{path, managementReaderToken, 410},
		{strings.Replace(path, "monitorID=service", "monitorID=other", 1), managementOperatorToken, 400},
		{strings.Replace(path, "limit=1", "limit=2", 1), managementOperatorToken, 400},
		{"/api/v2/history", managementOperatorToken, 400},
		{"/api/v2/history?monitorID=service&limit=501", managementOperatorToken, 400},
		{"/api/v2/history?monitorID=service&selector=team%3Dops", managementOperatorToken, 501},
		{"/api/v2/history?monitorID=service&monitorID=other", managementOperatorToken, 400},
	} {
		response, raw := f.request(t, http.MethodGet, test.path, test.token, nil, nil)
		if response.StatusCode != test.status {
			t.Fatalf("query: %d %s", response.StatusCode, raw)
		}
	}
	after := first.Data.NextCursor
	for after != "" {
		page, err := f.sdk.History(ctx, cpra.ListOptions{MonitorID: m.ID, Limit: 1, Cursor: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Data.Items {
			if e.Note == "after initial snapshot" {
				t.Fatal("post-snapshot event entered frozen history")
			}
		}
		after = page.Data.NextCursor
	}
	if err := f.server.cfg.Store.History().Expire(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	response, _ := f.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("retention changed cursor silently", response.StatusCode)
	}
}

func TestManagementHistoryByteBoundPreservesEveryEvent(t *testing.T) {
	f := newManagementFixture(t, true)
	_, m, g := configureControlMonitor(t, f, "large-audit")
	m = controlPulse(t, f, m, g, "failure")
	ctx := context.Background()
	commands := make([]persistence.Command, 300)
	revision := m.IncidentRevision
	note := strings.Repeat("\x01", 4096)
	for i := range commands {
		next := uuid.NewString()
		commands[i] = persistence.Command{Kind: "control", MonitorID: m.ID, At: time.Now().UTC(), Control: &persistence.ControlCommand{Action: "acknowledge", MonitorUID: m.CatalogUID, ExpectedRevision: revision, Revision: next, OperationID: next, IncidentID: m.IncidentID, Actor: "operator", Reason: note, Note: note}}
		revision = next
	}
	for start := 0; start < len(commands); start += 25 {
		results, err := f.server.cfg.Store.Submit(ctx, commands[start:min(start+25, len(commands))])
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		}
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	acks := 0
	for {
		page, err := f.sdk.History(ctx, cpra.ListOptions{MonitorID: m.ID, Limit: 500, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(page.Data)
		if len(raw) > managementMaxPageBytes {
			t.Fatalf("response exceeded byte bound: %d", len(raw))
		}
		pages++
		for _, e := range page.Data.Items {
			if seen[e.ID] {
				t.Fatal("byte continuation duplicated event")
			}
			seen[e.ID] = true
			if e.Kind == "control_acknowledge" {
				acks++
			}
		}
		cursor = page.Data.NextCursor
		if cursor == "" {
			break
		}
	}
	if pages < 2 || acks != 300 {
		t.Fatalf("byte truncation lost audit: pages %d acks %d", pages, acks)
	}
}
