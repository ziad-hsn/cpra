package httpserver

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMonitorConditionsDetailSampleAndListCoverage(t *testing.T) {
	f := newManagementFixture(t, true)
	_, m, guard := configureControlMonitor(t, f, "condition-detail")
	m = controlPulse(t, f, m, guard, "failure")
	conditions, truncated := m.NotificationConditions()
	row := fleetview.MonitorSummary{ID: 17, MonitorID: m.ID, Name: m.Name, Source: "management",
		IncarnationUID: m.IncarnationID(), ConfigurationRevision: m.Revision, CatalogRevision: m.CatalogRevision,
		ControlRevision: m.ControlRevision, StateGeneration: m.Generation, IncidentID: m.IncidentID, IncidentRevision: m.IncidentRevision,
		Health: "unhealthy", Phase: "incident", NotificationConditionsTruncated: truncated}
	for _, condition := range conditions {
		row.NotificationConditions = append(row.NotificationConditions, fleetview.NotificationCondition{ConditionID: condition.ConditionID, Kind: condition.Kind, Color: condition.Color, State: condition.State})
	}
	index := fleetview.NewIndex([]fleetview.MonitorSummary{row})
	f.server.holder.SetIndex(index)
	read := func(path string, want int) []byte {
		t.Helper()
		response, body := f.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
		if response.StatusCode != want {
			t.Fatalf("%s: status %d, want %d: %s", path, response.StatusCode, want, body)
		}
		return body
	}
	var observation api.MonitorObservation
	if err := json.Unmarshal(read("/api/v2/observations/monitors/"+m.ID, 200), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.NotificationConditionsObservedAt == nil || observation.NotificationConditionsObservedAt.Location() != time.UTC || observation.NotificationConditionsSequence == "" || len(observation.NotificationConditions) == 0 {
		t.Fatal("detail omitted retained conditions or actual sample metadata", observation)
	}
	var numeric fleetview.MonitorSummary
	if err := json.Unmarshal(read("/api/v1/monitors/17", 200), &numeric); err != nil {
		t.Fatal(err)
	}
	if numeric.NotificationConditionsObservedAt.IsZero() || numeric.NotificationConditionsSequence != observation.NotificationConditionsSequence || len(numeric.NotificationConditions) != len(observation.NotificationConditions) {
		t.Fatal("numeric detail differs from stable-ID detail", numeric, observation)
	}
	for _, token := range []string{managementOperatorToken, managementReaderToken} {
		response, _ := f.request(t, http.MethodGet, "/api/v1/monitors/17", token, nil, map[string]string{"Origin": "https://foreign.invalid"})
		if response.StatusCode != http.StatusForbidden {
			t.Fatal("numeric detail bypassed the existing origin policy", response.StatusCode)
		}
	}
	var resource api.Resource
	if err := json.Unmarshal(read("/api/v2/monitors/"+m.ID, 200), &resource); err != nil {
		t.Fatal(err)
	}
	var status api.MonitorStatus
	if err := json.Unmarshal(resource.Status, &status); err != nil || status.NotificationConditionsObservedAt == nil || status.NotificationConditionsSequence != observation.NotificationConditionsSequence || len(status.NotificationConditions) != len(observation.NotificationConditions) {
		t.Fatal("configuration detail omitted sample", status, err)
	}
	for _, path := range []string{"/api/v1/monitors", "/api/v2/observations/monitors", "/api/v2/monitors"} {
		body := string(read(path, 200))
		for _, field := range []string{"notificationConditionsObservedAt", "notificationConditionsSequence", "notification_conditions_observed_at", "notification_conditions_sequence"} {
			if strings.Contains(body, field) {
				t.Fatal("list acquired detail sample metadata", path, field)
			}
		}
	}
	// An old owner row cannot acquire a current sample from its replacement.
	row.IncarnationUID = "earlier-incarnation"
	index.Put(row)
	// Unmarshal into a fresh value: absent metadata must not retain the earlier read.
	observation = api.MonitorObservation{}
	if err := json.Unmarshal(read("/api/v2/observations/monitors/"+m.ID, 200), &observation); err != nil || !observation.NotificationConditionsTruncated || observation.NotificationConditionsObservedAt != nil || observation.NotificationConditionsSequence != "" {
		t.Fatal("mismatched owner attached a retained sample", observation, err)
	}
	// Numeric details share the existing eight-read admission bound.
	for range cap(f.server.managementHTTP.snapshotReads) {
		f.server.managementHTTP.snapshotReads <- struct{}{}
	}
	t.Cleanup(func() {
		for len(f.server.managementHTTP.snapshotReads) > 0 {
			<-f.server.managementHTTP.snapshotReads
		}
	})
	read("/api/v1/monitors/17", 429)
	read("/api/v1/monitors", 200)
}

func TestMonitorConditionFenceAndDecimalSequence(t *testing.T) {
	view := persistence.NotificationConditionView{IncarnationID: "owner", Revision: "config", CatalogRevision: "catalog", ControlRevision: "control", Generation: 12, IncidentID: "incident", IncidentRevision: "incident-revision"}
	row := fleetview.MonitorSummary{IncarnationUID: view.IncarnationID, ConfigurationRevision: view.Revision, CatalogRevision: view.CatalogRevision, ControlRevision: view.ControlRevision, StateGeneration: view.Generation, IncidentID: view.IncidentID, IncidentRevision: view.IncidentRevision}
	if !notificationConditionsMatchRow(view, row) {
		t.Fatal("matching identity rejected")
	}
	for _, change := range []func(*fleetview.MonitorSummary){
		func(r *fleetview.MonitorSummary) { r.IncarnationUID += "x" },
		func(r *fleetview.MonitorSummary) { r.ConfigurationRevision += "x" },
		func(r *fleetview.MonitorSummary) { r.CatalogRevision += "x" },
		func(r *fleetview.MonitorSummary) { r.ControlRevision += "x" },
		func(r *fleetview.MonitorSummary) { r.StateGeneration++ },
		func(r *fleetview.MonitorSummary) { r.IncidentID += "x" },
		func(r *fleetview.MonitorSummary) { r.IncidentRevision += "x" },
	} {
		changed := row
		change(&changed)
		if notificationConditionsMatchRow(view, changed) {
			t.Fatal("different owner or state matched", changed)
		}
	}
	row.NotificationConditionsObservedAt = time.Now().UTC()
	row.NotificationConditionsSequence = strconv.FormatUint(math.MaxUint64, 10)
	raw, err := json.Marshal(monitorObservation(row))
	if err != nil || !strings.Contains(string(raw), `"notificationConditionsSequence":"18446744073709551615"`) {
		t.Fatal("sequence became a lossy JSON number", string(raw), err)
	}
}
