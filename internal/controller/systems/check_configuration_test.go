package systems

import (
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestControllerCAFailureDoesNotCreateAnOutage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	world := ecs.NewWorld()
	ent := releaseMonitor(t, &world, 1, 1)
	queue := &admissionQueue{}
	system := newIncidentSystem(t, &world, queue, queue)
	state := *system.states.Get(ent)
	ca := filepath.Join(t.TempDir(), "private-configuration-marker.pem")
	job, err := jobs.CreatePulseJob(manifest.Pulse{Type: "http", Timeout: time.Second, Retries: 3, Config: &manifest.PulseHTTPConfig{Url: server.URL, CAFile: ca}}, ent)
	if err != nil {
		t.Fatal(err)
	}
	for generation := uint64(1); generation <= 2; generation++ {
		if generation == 2 {
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
		}
		scheduled := time.Now().UTC()
		system.store.SLO().Expect("http", scheduled)
		result := job.Execute()
		result.MonitorID, result.Revision, result.Generation, result.Type, result.Driver = state.MonitorID, state.Revision, generation, "pulse", "http"
		result.Scheduled, result.ExecutionStart, result.ExecutionEnd = scheduled, scheduled, time.Now().UTC()
		if !system.commitResults([]jobs.Result{result}) {
			t.Fatal("preparation result failed to commit", system.lastError)
		}
		m, _ := system.store.Get(state.MonitorID)
		if generation == 1 {
			if calls.Load() != 0 || m.Incident || m.Recovering || len(m.Actions) != 0 || !m.LastCheck.IsZero() || m.TotalChecks != 0 || m.ConfigurationProblem.Code != "ca_file_missing" {
				t.Fatal("local CA failure produced an outage or contacted the target")
			}
			if result.Err == nil || strings.Contains(result.Err.Error(), "private-configuration-marker") {
				t.Fatal("unsafe preparation error")
			}
			v := system.store.SLO().View(time.Now().Add(15*time.Second), 5*time.Minute, 1).Reports[0]
			if v.Expected != 1 || v.Missed != 1 || v.Samples != 0 || *v.ResultAttainment != 0 {
				t.Fatal("preparation improved SLO attainment", v)
			}
		} else if calls.Load() != 1 || result.Err != nil || m.ConfigurationProblem.Code != "" || m.SuccessfulChecks != 1 || len(m.Actions) != 0 {
			t.Fatal("fixed CA did not restore ordinary checks without a fabricated recovery", result.Err)
		}
	}
}

func TestCheckPreparationHoldsQueuedRecoveryWithoutRedispatch(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "held_without_start", true: "repaired"}[repair], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				world := ecs.NewWorld()
				ent := interventionMonitor(t, &world, "http://127.0.0.1:1")
				queue := &admissionQueue{}
				system := newIncidentSystem(t, &world, queue, &admissionQueue{})
				m := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("target down")})
				original := incidentActions(m, "intervention", "")[0]
				applyIncidentPulse(t, system, ent, jobs.Result{Err: &jobs.ConfigurationError{Code: "ca_file_missing"}})
				for n := 0; n < 10; n++ {
					system.dispatchActions(time.Now())
					if len(queue.admitted) != 0 || !system.actionDue[ent].Equal(original.Deadline) {
						t.Fatal("configuration problem redispatched recovery instead of waiting for its original deadline")
					}
				}
				if repair {
					applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("target still down")})
					system.dispatchActions(time.Now())
					if len(queue.admitted) != 1 || queue.admitted[0].(*jobs.Dispatch).ActionID != original.ID {
						t.Fatal("actual observation did not release original queued recovery")
					}
				} else {
					time.Sleep(2*original.ExecutionBudget + time.Second)
					system.dispatchActions(time.Now())
					current, _ := monitorWithActionHistory(t, system.store, m.ID)
					if len(queue.admitted) != 0 || current.Actions[original.ID].State != persistence.Queued || !current.Actions[original.ID].Deadline.IsZero() || current.Actions[original.ID].ExecutionBudget != original.ExecutionBudget || system.lastError != nil {
						t.Fatal("local preparation consumed an unstarted recovery budget", system.lastError)
					}
				}
			})
		})
	}
}
