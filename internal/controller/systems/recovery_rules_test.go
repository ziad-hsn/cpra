package systems

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestRuleSelectedProductionJobIsCommittedBeforeInvocation(t *testing.T) {
	t.Run("repaired preparation", func(t *testing.T) { testRuleSelectedProductionJob(t, false) })
	t.Run("target recovered before preparation", func(t *testing.T) { testRuleSelectedProductionJob(t, true) })
}

func testRuleSelectedProductionJob(t *testing.T, recoverBeforePreparation bool) {
	t.Helper()
	var selected, other atomic.Int64
	var healthy atomic.Bool
	var store *persistence.Store
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if healthy.Load() {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/selected" {
			m, ok := store.Get("rules")
			if !ok || m.RecoveryDecision.Selection.RuleID != "unavailable" {
				t.Error("provider ran before committed selection")
			}
			started := false
			for _, a := range m.Actions {
				started = started || a.Kind == "intervention" && a.State == persistence.Started && a.Rule.RuleID == "unavailable"
			}
			if !started {
				t.Error("provider ran without committed start")
			}
			selected.Add(1)
		} else {
			other.Add(1)
		}
		w.WriteHeader(500) // The effect already occurred: never authorize replay.
	}))
	defer receiver.Close()
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = t.TempDir()
	var err error
	store, err = persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w := ecs.NewWorld()
	mapper := entities.NewEntityManager(&w)
	m := manifest.Monitor{ID: "rules", Name: "rules", Enabled: true, Pulse: manifest.Pulse{Type: "http", Interval: time.Second, Timeout: time.Second, UnhealthyThreshold: 2, HealthyThreshold: 2, Config: &manifest.PulseHTTPConfig{Url: receiver.URL, ExpectedStatus: []int{200}}}, Intervention: manifest.Intervention{TaxonomyVersion: "1", MaxAttempts: 3, Rules: []manifest.InterventionRule{
		{ID: "forbidden", Match: manifest.FailureMatch{Codes: []string{"unexpected_http_status"}, HTTPStatuses: []int{403}}, Action: "webhook", Target: &manifest.InterventionTargetWebhook{URL: receiver.URL + "/other", Method: "POST", Timeout: time.Second}},
		{ID: "unavailable", Match: manifest.FailureMatch{Codes: []string{"unexpected_http_status"}, HTTPStatuses: []int{503}}, Action: "webhook", Target: &manifest.InterventionTargetWebhook{URL: receiver.URL + "/selected", Method: "POST", Timeout: time.Second}},
	}}}
	p, err := entities.PrepareMonitor(m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := mapper.Install(p, &w)
	if err != nil {
		t.Fatal(err)
	}
	storage := ecs.NewMap1[components.JobStorage](&w).Get(ent)
	if storage.InterventionJob != nil {
		t.Fatal("eagerly prepared recovery branches")
	}
	queue := &admissionQueue{}
	s := NewDurableSystem(&w, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, queue, &admissionQueue{}, nil, time.Minute, false)
	if err = s.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Initialize(&w)
	for n := uint64(1); n <= 2; n++ {
		state := s.states.Get(ent)
		storage = ecs.NewMap1[components.JobStorage](&w).Get(ent)
		d := jobs.NewDispatch(storage.PulseJob, ent, "pulse", "", n, 0)
		d.MonitorID, d.Revision, d.Scheduled = state.MonitorID, state.Revision, time.Now().UTC()
		d.Driver = "http"
		if !s.commitResults([]jobs.Result{d.Execute()}) {
			t.Fatal(s.lastError)
		}
	}

	// Preparation is local to the selected intervention. A missing runtime target
	// must not mark durable storage or the controller permanently unavailable.
	choices := s.interventions.Get(ent)
	original := choices.Choices[1].Target
	choices.Choices[1].Target = nil
	s.dispatchActions(time.Now().UTC())
	if len(queue.admitted) != 0 || s.lastError != nil || storage.InterventionPreparationAction == "" {
		t.Fatal("selected preparation failure stopped the controller or admitted work", s.lastError)
	}
	s.Update(&w)
	if s.lastError != nil || !s.Progressing() {
		t.Fatal("local preparation stopped owner progress", s.lastError)
	}
	if recoverBeforePreparation {
		healthy.Store(true)
		for n := uint64(3); n <= 4; n++ {
			state := s.states.Get(ent)
			d := jobs.NewDispatch(storage.PulseJob, ent, "pulse", "", n, 0)
			d.MonitorID, d.Revision, d.Scheduled, d.Driver = state.MonitorID, state.Revision, time.Now().UTC(), "http"
			if !s.commitResults([]jobs.Result{d.Execute()}) {
				t.Fatal(s.lastError)
			}
		}
		current, _ := store.Get("rules")
		summary := s.summary(ent, current)
		if storage.InterventionPreparationAction != "" || summary.RecoveryDecision != nil && summary.RecoveryDecision.Reason == "preparation_unavailable" || summary.Health != "healthy" {
			t.Fatal("cancelled recovery retained a preparation problem", summary.RecoveryDecision, summary.Health)
		}
		s.dispatchActions(time.Now().UTC())
		if len(queue.admitted) != 0 || selected.Load() != 0 || other.Load() != 0 {
			t.Fatal("recovery invoked after target health recovered")
		}
		return
	}
	choices.Choices[1].Target = original
	storage.InterventionPreparationRetry = time.Time{}
	s.dispatchActions(time.Now().UTC().Add(2 * time.Second))
	if storage.InterventionPreparationAction != "" {
		t.Fatal("repaired selected preparation remained unavailable")
	}
	if len(queue.admitted) != 1 {
		t.Fatal("selected action not admitted", len(queue.admitted), s.lastError)
	}
	r := queue.admitted[0].Execute()
	if !s.commitResults([]jobs.Result{r}) {
		t.Fatal(s.lastError)
	}
	current, _ := store.Get("rules")
	if selected.Load() != 1 || other.Load() != 0 || current.RecoveryDecision.Selection.RuleID != "unavailable" {
		t.Fatal("wrong provider invocation count or selection")
	}
	unknown := current.ActionProjection != nil && current.ActionProjection.LatestRecovery != nil && current.ActionProjection.LatestRecovery.State == persistence.Unknown
	if !unknown {
		t.Fatal("effect followed by 500 was not held")
	}
	s.dispatchActions(time.Now().UTC())
	if len(queue.admitted) != 1 || selected.Load() != 1 {
		t.Fatal("ambiguous action replayed")
	}
	if err = store.Snapshot(); err != nil {
		t.Fatal(err)
	}
}
