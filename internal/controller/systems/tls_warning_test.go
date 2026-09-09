package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/web/snapshot"
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
	state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
	pulse := NewBatchPulseResultSystem(&w, nil, NewPulseScheduler(), &ReadyQueue{}, NewCodeScheduler(), noopLogger{}, NewStateLogger(false), nil)
	apply := func() jobs.Result {
		result := probe.Execute()
		state.PulseGeneration++
		state.SetPulsePending(true)
		result.Ent, result.Generation = ent, state.PulseGeneration
		pulse.ProcessBatch([]jobs.Result{result})
		return result
	}
	if result := apply(); result.Err != nil || result.Warning == "" {
		t.Fatalf("warning threshold was ignored: %+v", result)
	}
	apply()
	if state.IsInterventionNeeded() || state.ConsecutiveFailures != 0 || classifyStatus(state) != "degraded" {
		t.Fatalf("warning triggered failure/recovery: %+v", state)
	}
	if len(state.PendingAlerts) != 1 || state.PendingAlerts[0].Color != "yellow" {
		t.Fatalf("warning must produce one yellow alert: %+v", state.PendingAlerts)
	}
	holder := snapshot.NewHolder()
	snapshots := NewBatchStatsSnapshotSystem(&w, noopLogger{}, holder, time.Second, 100)
	snapshots.Initialize(&w)
	snapshots.Update(&w)
	if holder.Get().Monitors[0].Warning == "" {
		t.Fatal("warning detail was not exposed")
	}
	probe.WarnDays = 0
	apply()
	if classifyStatus(state) != "up" || len(state.PendingAlerts) != 2 || state.PendingAlerts[1].Color != "green" {
		t.Fatalf("warning clearance was not observed: %+v", state)
	}
	probe.CriticalDays = 1000000
	if result := apply(); result.Err == nil {
		t.Fatal("critical threshold did not fail")
	}
	if !state.IsInterventionNeeded() || state.ConsecutiveFailures != 1 {
		t.Fatal("critical failure did not follow the recovery policy")
	}
}
