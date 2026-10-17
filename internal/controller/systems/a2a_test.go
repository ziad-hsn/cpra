package systems

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestA2AControllerReceiptCancellationDoesNotPoisonStorage(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.Header().Set("Content-Type", "application/a2a+json")
		_, _ = w.Write([]byte(`{"task":{"id":"task","status":{"state":"TASK_STATE_COMPLETED"}}}`))
	}))
	defer server.Close()
	s, dispatch, results := agentController(t, server.URL, "intervention")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatch.SetContext(ctx)
	authorize := dispatch.Authorize
	dispatch.Authorize = func(ctx context.Context) (jobs.ActionExecution, error) {
		e, err := authorize(ctx)
		if err == nil {
			record := e.RecordAgentReceipt
			e.RecordAgentReceipt = func(ctx context.Context, r jobs.A2AReceipt) error { cancel(); return record(ctx, r) }
		}
		return e, err
	}
	r := dispatch.Execute()
	if r.Err == nil || !errors.Is(ctx.Err(), context.Canceled) || sends.Load() != 1 {
		t.Fatal("receipt cancellation did not cross the intended boundary", r)
	}
	results <- []jobs.Result{r}
	if !s.drain() || !s.store.Status().Ready {
		t.Fatal("caller cancellation permanently poisoned usable storage", s.lastError)
	}
	m, _ := s.store.Get(dispatch.MonitorID)
	if m.Actions[dispatch.ActionID].State != persistence.Unknown || m.VerifyRemaining != 0 {
		t.Fatal("uncommitted receipt became successful intervention")
	}
}

func agentController(t *testing.T, endpoint, kind string) (*DurableSystem, *jobs.Dispatch, chan []jobs.Result) {
	t.Helper()
	return agentControllerAt(t, endpoint, kind, t.TempDir())
}

func agentControllerAt(t *testing.T, endpoint, kind, directory string) (*DurableSystem, *jobs.Dispatch, chan []jobs.Result) {
	t.Helper()
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = directory
	store, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	w := ecs.NewWorld()
	ent := releaseMonitor(t, &w, 1, 2)
	if kind == "intervention" {
		ecs.NewMap1[components.InterventionConfig](&w).Add(ent, &components.InterventionConfig{Action: "a2a", MaxFailures: 1, Target: &manifest.InterventionTargetA2A{URL: endpoint, TaskTimeout: 5 * time.Second}})
	}
	state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
	state.MonitorID, state.Revision = "agent-monitor", "agent-revision"
	ecs.NewMap1[components.PulseConfig](&w).Get(ent).Interval = time.Hour
	cc := ecs.NewMap1[components.CodeConfig](&w).Get(ent)
	cc.Configs = map[string]*components.ColorCodeConfig{"red": {Dispatch: true, Notify: "a2a"}}
	job, err := jobs.NewA2AJob(jobs.A2AConfig{URL: endpoint, Token: "agent-only-secret", AllowInsecureHTTP: true, RequestTimeout: time.Second, TaskTimeout: 5 * time.Second, PollInterval: time.Second}, kind)
	if err != nil {
		t.Fatal(err)
	}
	storage := ecs.NewMap1[components.JobStorage](&w).Get(ent)
	storage.CodeJobs = map[string][]jobs.Job{"red": {job}}
	if kind == "intervention" {
		storage.InterventionJob = job
		cc.Configs = map[string]*components.ColorCodeConfig{}
	}
	q := &admissionQueue{}
	results := make(chan []jobs.Result, 4)
	s := NewDurableSystem(&w, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, q, q, []<-chan []jobs.Result{results}, time.Minute, false)
	// This fixed key is confined to the subprocess fixture so restart retains
	// the same wrapping identity. Application tests load protected key files.
	wrapper, err := secureconfig.NewLocalWrapper([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LoadAgentConnections(context.Background(), sealer); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.agentObserver.stop() })
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Initialize(&w)
	m, ok := store.Get("agent-monitor")
	if !ok {
		t.Fatal("configured monitor missing")
	}
	committed, err := store.Submit(context.Background(), []persistence.Command{{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: time.Now().UTC()}})
	if err != nil || len(committed) != 1 || committed[0].Err != nil {
		t.Fatal("pulse commit failed", err, committed)
	}
	m, _ = store.Get(m.ID)
	s.project(ent, m)
	s.scheduleActions(ent, m)
	s.dispatchActions(time.Now())
	if len(q.admitted) != 1 {
		t.Fatal("expected one action", len(q.admitted), m.Actions)
	}
	d, ok := q.admitted[0].(*jobs.Dispatch)
	if !ok || d.Driver != "a2a" {
		t.Fatal("wrong driver dispatch")
	}
	return s, d, results
}

func TestA2AControllerCommitsStartAndReceiptBeforeNetworkProgress(t *testing.T) {
	for _, kind := range []string{"intervention", "code"} {
		t.Run(kind, func(t *testing.T) {
			var s *DurableSystem
			var dispatch *jobs.Dispatch
			var sends, polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				m, ok := s.store.Get(dispatch.MonitorID)
				a := m.Actions[dispatch.ActionID]
				if !ok || a.State != persistence.Started || a.ExecutorKind != "agent" || a.ExecutorSession == "" || !a.ExecutorFinishedAt.IsZero() {
					t.Error("network request preceded committed start")
				}
				if r.Header.Get("Authorization") != "Bearer agent-only-secret" {
					t.Error("missing designated agent credential")
				}
				w.Header().Set("Content-Type", "application/a2a+json")
				if r.Method == http.MethodPost {
					sends.Add(1)
					var input struct {
						Message struct {
							ID    string `json:"messageId"`
							Parts []struct {
								Data struct{ MonitorID, IncidentID, ActionID, ConfigurationRevision, Role string } `json:"data"`
							} `json:"parts"`
						} `json:"message"`
					}
					if json.NewDecoder(r.Body).Decode(&input) != nil || input.Message.ID != a.ID || len(input.Message.Parts) != 2 {
						t.Error("outgoing action identity lost")
					} else {
						data := input.Message.Parts[1].Data
						if data.MonitorID != m.ID || data.IncidentID != a.IncidentID || data.ConfigurationRevision != a.Revision || data.ActionID != a.ID {
							t.Error("outgoing context does not match committed action")
						}
					}
					_, _ = w.Write([]byte(`{"task":{"id":"original-task","contextId":"original-context","status":{"state":"TASK_STATE_WORKING"}}}`))
					return
				}
				polls.Add(1)
				if r.URL.Path != "/tasks/original-task" || a.AgentReceipt == nil || a.AgentReceipt.TaskID != "original-task" || a.AgentReceipt.Sequence != 1 {
					t.Error("task lookup preceded receipt commit")
				}
				_, _ = w.Write([]byte(`{"id":"original-task","contextId":"original-context","status":{"state":"TASK_STATE_COMPLETED"}}`))
			}))
			defer server.Close()
			var results chan []jobs.Result
			s, dispatch, results = agentController(t, server.URL, kind)
			result := dispatch.Execute()
			if result.Err != nil || result.FinalizationErr != nil {
				t.Fatal(result)
			}
			results <- []jobs.Result{result}
			if !s.drain() {
				t.Fatal("result did not commit", s.lastError)
			}
			m, _ := s.store.Get(dispatch.MonitorID)
			a := m.Actions[dispatch.ActionID]
			if a.State != persistence.Succeeded || a.ExecutorFinishedAt.IsZero() || sends.Load() != 1 {
				t.Fatal("action state lost", a, sends.Load())
			}
			if kind == "intervention" && (m.VerifyRemaining != 2 || a.AgentReceipt.Sequence != 2 || polls.Load() != 1) {
				t.Fatal("task completion did not start independent health verification", m, polls.Load())
			}
			if kind == "code" && (m.VerifyRemaining != 0 || !m.Incident || a.AgentReceipt.Sequence != 1 || polls.Load() != 0) {
				t.Fatal("notification acceptance claimed recovery or polled", m)
			}
			if duplicate := dispatch.Copy().Execute(); duplicate.Err == nil || !duplicate.ExecutionStart.IsZero() {
				t.Fatal("finished action granted another invocation")
			}
			if sends.Load() != 1 {
				t.Fatal("duplicate action contacted provider")
			}
			page, err := s.store.History().Page(m.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			observed := 0
			for _, event := range page.Events {
				if event.Type == "agent_task_observed" {
					observed++
				}
			}
			if observed != int(a.AgentReceipt.Sequence) {
				t.Fatal("receipt history lost or duplicated", observed)
			}
		})
	}
}

func TestA2AControllerUnknownOutcomeSurvivesRestartWithoutSending(t *testing.T) {
	for _, mode := range []string{"lost reply", "failed task", "interrupted task"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "lost reply" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/a2a+json")
				state := "TASK_STATE_FAILED"
				if mode == "interrupted task" {
					state = "TASK_STATE_WORKING"
				}
				_, _ = w.Write([]byte(`{"task":{"id":"original-task","status":{"state":"` + state + `"}}}`))
			}))
			defer server.Close()
			s, dispatch, results := agentController(t, server.URL, "intervention")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dispatch.SetContext(ctx)
			if mode == "interrupted task" {
				authorize := dispatch.Authorize
				dispatch.Authorize = func(ctx context.Context) (jobs.ActionExecution, error) {
					e, err := authorize(ctx)
					if err == nil {
						record := e.RecordAgentReceipt
						e.RecordAgentReceipt = func(ctx context.Context, r jobs.A2AReceipt) error { err := record(ctx, r); cancel(); return err }
					}
					return e, err
				}
			}
			r := dispatch.Execute()
			if r.Err == nil || r.FinalizationErr != nil {
				t.Fatal("uncertain action lost", r)
			}
			results <- []jobs.Result{r}
			if !s.drain() {
				t.Fatal(s.lastError)
			}
			before, _ := s.store.Get(dispatch.MonitorID)
			a := before.Actions[dispatch.ActionID]
			if a.State != persistence.Unknown || before.VerifyRemaining != 0 || calls.Load() != 1 {
				t.Fatal("uncertain action treated as rejection or success", a, calls.Load())
			}
			if err := s.store.Snapshot(); err != nil {
				t.Fatal(err)
			}
			if err := s.store.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := persistence.Open(context.Background(), s.config)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			view, ok, err := restored.Action(a.ID)
			if err != nil || !ok || view.State != persistence.Unknown || !view.Held || view.ExecutorFenced != (mode == "failed task") {
				t.Fatal("restart cleared unknown outcome or fenced a running task", view, err)
			}
			if mode != "lost reply" && (view.AgentReceipt == nil || view.AgentReceipt.TaskID != "original-task") {
				t.Fatal("restart lost remote task")
			}
			h, err := restored.BeginLocalAction(context.Background(), persistence.Command{Kind: "start", MonitorID: before.ID, Revision: a.Revision, ActionID: a.ID, Driver: "a2a", At: time.Now().UTC()})
			if h != nil {
				_ = h.Finish(context.Background())
			}
			if err == nil || calls.Load() != 1 {
				t.Fatal("restart repeated unknown action", err, calls.Load())
			}
		})
	}
}

func TestA2AControllerChangedConfigurationCannotRedirectQueuedAction(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	s, dispatch, _ := agentController(t, server.URL, "intervention")
	m, _ := s.store.Get(dispatch.MonitorID)
	newConfig := persistence.Monitor{ID: m.ID, Revision: "new-target-revision", Name: m.Name, Policy: m.Policy}
	_, err := s.store.Submit(context.Background(), []persistence.Command{{Kind: "configure", MonitorID: m.ID, Revision: newConfig.Revision, Config: &newConfig, At: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	r := dispatch.Execute()
	if r.Err == nil || !r.ExecutionStart.IsZero() || calls.Load() != 0 {
		t.Fatal("old queued action executed after configuration changed", r)
	}
}

func TestA2AControllerKeepsOldTargetReceiptAfterConfigurationChange(t *testing.T) {
	var s *DurableSystem
	var dispatch *jobs.Dispatch
	var sends, polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/a2a+json")
		if r.Method == http.MethodPost {
			sends.Add(1)
			_, _ = w.Write([]byte(`{"task":{"id":"original-task","status":{"state":"TASK_STATE_WORKING"}}}`))
			return
		}
		polls.Add(1)
		m, _ := s.store.Get(dispatch.MonitorID)
		newConfig := persistence.Monitor{ID: m.ID, Revision: "new-target-revision", Name: m.Name, Policy: m.Policy}
		if _, err := s.store.Submit(context.Background(), []persistence.Command{{Kind: "configure", MonitorID: m.ID, Revision: newConfig.Revision, Config: &newConfig, At: time.Now().UTC()}}); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"id":"original-task","status":{"state":"TASK_STATE_COMPLETED"}}`))
	}))
	defer server.Close()
	var results chan []jobs.Result
	s, dispatch, results = agentController(t, server.URL, "intervention")
	r := dispatch.Execute()
	if r.Err != nil || r.FinalizationErr != nil {
		t.Fatal(r)
	}
	results <- []jobs.Result{r}
	if !s.drain() {
		t.Fatal(s.lastError)
	}
	m, _ := s.store.Get(dispatch.MonitorID)
	a := m.Actions[dispatch.ActionID]
	if m.Revision != "new-target-revision" || m.VerifyRemaining != 0 || a.Revision != dispatch.Revision || a.State != persistence.Unknown || a.AgentReceipt == nil || a.AgentReceipt.State != "TASK_STATE_COMPLETED" || a.LateEvidence == nil {
		t.Fatal("old completion changed replacement monitor or lost original evidence", m)
	}
	if sends.Load() != 1 || polls.Load() != 1 {
		t.Fatal("target change replayed the operation")
	}
	if dispatch.Copy().Execute().Err == nil || sends.Load() != 1 {
		t.Fatal("old dispatch invoked again")
	}
}
