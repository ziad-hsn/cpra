package systems

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func agentObserverFixture(t *testing.T, endpoint string, count int) (*agentObserver, []persistence.ActionRecord) {
	t.Helper()
	config := runtimeconfig.Default()
	config.Storage.Mode = "memory"
	store, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{69}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(c persistence.Command) persistence.Result {
		t.Helper()
		results, err := store.Submit(t.Context(), []persistence.Command{c})
		if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil {
			t.Fatal("fixture commit failed", err, results)
		}
		return results[0]
	}
	plain, err := json.Marshal(map[string]any{"format": 1, "url": endpoint, "token": "retained-agent-token",
		"tenant": "operations", "request_timeout": int64(time.Second), "allow_insecure_http": true})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	var actions []persistence.ActionRecord
	for n := 0; n < count; n++ {
		at := time.Now().UTC()
		monitor := persistence.Monitor{ID: fmt.Sprintf("agent-%03d", n), Name: "Agent monitor", Revision: "original-config",
			Policy: persistence.Policy{Enabled: true, Intervention: true, Interval: time.Minute, Unhealthy: 1, Healthy: 1}}
		commit(persistence.Command{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, At: at})
		result := commit(persistence.Command{Kind: "pulse", MonitorID: monitor.ID, Revision: monitor.Revision, Generation: 1, Outcome: "failure", At: at})
		for _, action := range result.Monitor.Actions {
			if action.Kind != "intervention" {
				continue
			}
			payload, err := sealer.Seal(t.Context(), persistence.AgentConnectionBinding(store.Status().NodeID, monitor.ID, action), plain)
			if err != nil {
				t.Fatal(err)
			}
			h, err := store.BeginLocalAction(t.Context(), persistence.Command{Kind: "start", MonitorID: monitor.ID, Revision: monitor.Revision,
				ActionID: action.ID, Driver: "a2a", At: at, AgentConnection: &persistence.AgentConnection{Payload: payload}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.Finish(context.Background()) })
			if err := h.RecordAgentReceipt(t.Context(), persistence.AgentReceiptCommand{Receipt: persistence.AgentReceipt{
				Sequence: 1, TaskID: monitor.ID, ContextID: "original-context", State: "TASK_STATE_WORKING", ObservedAt: at}}); err != nil {
				t.Fatal(err)
			}
			commit(persistence.Command{Kind: "result", MonitorID: monitor.ID, Revision: monitor.Revision, ActionID: action.ID,
				At: at, Outcome: "failure", Ambiguous: true})
			if err := h.Finish(t.Context()); err != nil {
				t.Fatal(err)
			}
			record, exists, err := store.Action(action.ID)
			if err != nil || !exists {
				t.Fatal("fixture action missing", err)
			}
			actions = append(actions, record)
		}
	}
	if len(actions) != count {
		t.Fatal("fixture did not create the requested independent actions")
	}
	if err := catalog.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	observer := newAgentObserver(catalog, store)
	t.Cleanup(observer.stop)
	return observer, actions
}

func TestAgentObserverReadFailuresPreserveUnknownActions(t *testing.T) {
	for _, scenario := range []string{"unavailable", "revoked", "missing", "redirect", "task-substitution", "context-substitution", "oversized", "unchanged"} {
		t.Run(scenario, func(t *testing.T) {
			var requests, redirects atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1) }))
			t.Cleanup(destination.Close)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/tasks/agent-000" || r.URL.Query().Get("historyLength") != "0" ||
					r.URL.Query().Get("tenant") != "operations" || r.Header.Get("Authorization") != "Bearer retained-agent-token" {
					t.Error("observer used an operation, task or identity other than the original read")
				}
				switch scenario {
				case "unavailable":
					http.Error(w, "private-provider-diagnostic", http.StatusServiceUnavailable)
				case "revoked":
					http.Error(w, "private-provider-diagnostic", http.StatusUnauthorized)
				case "missing":
					http.Error(w, "private-provider-diagnostic", http.StatusNotFound)
				case "redirect":
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
				case "oversized":
					_, _ = w.Write(bytes.Repeat([]byte{'x'}, (256<<10)+1))
				default:
					task, contextID := "agent-000", "original-context"
					if scenario == "task-substitution" {
						task = "another-task"
					}
					if scenario == "context-substitution" {
						contextID = "another-context"
					}
					w.Header().Set("Content-Type", "application/a2a+json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": task, "contextId": contextID, "status": map[string]string{"state": "TASK_STATE_WORKING"}})
				}
			}))
			t.Cleanup(server.Close)
			observer, actions := agentObserverFixture(t, server.URL, 1)
			before, _ := observer.store.History().Page(actions[0].MonitorID, "", 100)
			err := observer.observe(actions[0])
			if (err == nil) != (scenario == "unchanged") || requests.Load() != 1 || redirects.Load() != 0 {
				t.Fatal("failed read was retried, redirected or reported as successful", err)
			}
			if err != nil && (strings.Contains(err.Error(), "private-provider-diagnostic") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "retained-agent-token")) {
				t.Fatal("provider failure disclosed sensitive details")
			}
			current, _, _ := observer.store.Action(actions[0].ID)
			after, _ := observer.store.History().Page(current.MonitorID, "", 100)
			if !reflect.DeepEqual(current, actions[0]) || !reflect.DeepEqual(before, after) || !observer.store.Status().Ready {
				t.Fatal("read failure or unchanged polling changed durable state or storage health")
			}
		})
	}
}

func TestAgentObserverBoundsRequestsAndAdvancesPastFailures(t *testing.T) {
	var mutex sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		calls[r.URL.Path]++
		mutex.Unlock()
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	observer, actions := agentObserverFixture(t, server.URL, 6)
	after := observer.scan("")
	mutex.Lock()
	first := len(calls)
	mutex.Unlock()
	if after == "" || first != 4 {
		t.Fatal("scan exceeded its request limit or lost its continuation", first)
	}
	if next := observer.scan(after); next != "" {
		t.Fatal("scan did not finish the remaining page")
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(calls) != len(actions) {
		t.Fatal("failing targets starved later actions")
	}
	for _, count := range calls {
		if count != 1 {
			t.Fatal("scan restarted from the beginning instead of advancing")
		}
	}
}

func TestAgentObserverShutdownCancelsReadAndJoins(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(server.Close)
	observer, actions := agentObserverFixture(t, server.URL, 1)
	observer.start()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not begin reading the retained task")
	}
	stopped := make(chan struct{})
	go func() { observer.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join the observer")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown left the provider request active")
	}
	current, _, _ := observer.store.Action(actions[0].ID)
	if !reflect.DeepEqual(current, actions[0]) || !observer.store.Status().Ready {
		t.Fatal("cancellation changed an unknown action or poisoned storage")
	}
	if err := observer.observe(current); !errors.Is(err, observer.ctx.Err()) {
		t.Fatal("a stopped observer retained read authority", err)
	}
}
