package systems

import (
	"github.com/ziad-hsn/cpra/internal/jobs"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

func TestTLSWarningAlertsWithoutRecoveryAndCriticalStillFails(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	address := server.Listener.Addr().(*net.TCPAddr)
	probe := &jobs.PulseTLSJob{Host: address.IP.String(), Port: address.Port, WarnDays: 1000000, CriticalDays: 1, InsecureSkipVerify: true, Timeout: time.Second}
	w := ecs.NewWorld()
	ent := interventionMonitor(t, &w, server.URL)
	notifications := &admissionQueue{}
	pulse := newIncidentSystem(t, &w, &admissionQueue{}, notifications)
	state := pulse.states.Get(ent)
	apply := func() jobs.Result {
		result := probe.Execute()
		applyIncidentPulse(t, pulse, ent, result)
		return result
	}
	if result := apply(); result.Err != nil || result.Warning == "" {
		t.Fatalf("warning threshold was ignored: %+v", result)
	}
	apply()
	if state.IsInterventionPending() || state.ConsecutiveFailures != 0 || classifyStatus(state) != "degraded" {
		t.Fatalf("warning triggered failure/recovery: %+v", state)
	}
	monitor, _ := pulse.store.Get(state.MonitorID)
	if len(incidentActions(monitor, "code", "yellow")) != 1 {
		t.Fatal("warning did not produce one yellow delivery")
	}
	row, ok := pulse.index.Get(ent.ID())
	if !ok || row.Warning == "" {
		t.Fatal("warning detail was not exposed")
	}
	pulse.dispatchActions(time.Now().UTC())
	if len(notifications.admitted) != 1 {
		t.Fatal("warning notification was not admitted")
	}
	result := notifications.admitted[0].Execute()
	if result.Err != nil || !pulse.commitResults([]jobs.Result{result}) {
		t.Fatal("warning notification attempt did not complete", result.Err, pulse.lastError)
	}
	probe.WarnDays = 0
	apply()
	monitor, _ = pulse.store.Get(state.MonitorID)
	if len(incidentActions(monitor, "code", "green")) != 0 {
		t.Fatal("warning clearance notified before the healthy-check threshold")
	}
	apply()
	monitor, _ = pulse.store.Get(state.MonitorID)
	if classifyStatus(state) != "up" || len(incidentActions(monitor, "code", "green")) != 1 {
		t.Fatalf("warning clearance was not observed: %+v", state)
	}
	probe.CriticalDays = 1000000
	if result := apply(); result.Err == nil {
		t.Fatal("critical threshold did not fail")
	}
	if !state.IsInterventionPending() || state.ConsecutiveFailures != 1 {
		t.Fatal("critical failure did not follow the recovery policy")
	}
}
