package httpserver

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestManualIgnoreLogsOnlyCommittedTransitions(t *testing.T) {
	f := newManagementFixture(t, true)
	_, monitor, _ := configureControlMonitor(t, f, "log-controls")
	var mu sync.Mutex
	var logs []map[string]interface{}
	f.server.managementHTTP.logInfo = func(message string, fields ...interface{}) {
		record := map[string]interface{}{"message": message}
		for i := 0; i+1 < len(fields); i += 2 {
			record[fields[i].(string)] = fields[i+1]
		}
		mu.Lock()
		logs = append(logs, record)
		mu.Unlock()
	}
	reason := "private-audit-reason-marker"
	ignored, err := f.sdk.Monitors.Ignore(t.Context(), monitor.ID, api.ControlRequest{Revision: monitor.ControlRevision, Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := f.server.cfg.Store.Get(monitor.ID)
	if _, err := f.sdk.Monitors.Ignore(t.Context(), monitor.ID, api.ControlRequest{Revision: current.ControlRevision, Reason: reason}); err == nil {
		t.Fatal("unchanged Ignore unexpectedly committed")
	}
	cleared, err := f.sdk.Monitors.Unignore(t.Context(), monitor.ID, api.ControlRequest{Revision: current.ControlRevision, Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 2 {
		t.Fatal("incorrect transition count", logs)
	}
	for i, event := range []string{"monitor_ignored", "monitor_unignored"} {
		if logs[i]["event_type"] != event || logs[i]["actor"] != "operator" || logs[i]["monitor_id"] != monitor.ID || logs[i]["monitor_uid"] != monitor.CatalogUID || logs[i]["control_revision"] == "" || logs[i]["ignore_cause"] != "operator" {
			t.Fatal("missing transition identity", logs)
		}
	}
	if logs[0]["operation_id"] != ignored.OperationID || logs[1]["operation_id"] != cleared.OperationID {
		t.Fatal("wrong committed operation", logs)
	}
	raw, err := json.Marshal(logs)
	if err != nil || strings.Contains(string(raw), reason) || strings.Contains(string(raw), "provider-not-invoked") {
		t.Fatal("private input leaked into logs", err)
	}
	page, err := f.server.cfg.Store.History().Page(monitor.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, event := range page.Events {
		if event.Type == "monitor_ignored" || event.Type == "monitor_unignored" {
			found++
			if event.Reason != reason || event.Actor != "operator" {
				t.Fatal("audited reason/actor changed", event.Type)
			}
		}
	}
	if found != 2 {
		t.Fatal("committed audit events missing", found)
	}
}
