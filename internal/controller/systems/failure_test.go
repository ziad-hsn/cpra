package systems

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestControllerCommitsSafeFailureBeforeNotification(t *testing.T) {
	var mu sync.Mutex
	var delivered string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("private-provider-body password=private-credential"))
			return
		}
		var body struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		delivered = body.Message
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = t.TempDir()
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	world := ecs.NewWorld()
	ent := releaseMonitor(t, &world, 1, 1)
	ecs.NewMap1[components.CodeConfig](&world).Get(ent).Configs = map[string]*components.ColorCodeConfig{"red": {Dispatch: true, Notify: "webhook"}}
	ecs.NewMap1[components.JobStorage](&world).Get(ent).CodeJobs = map[string][]jobs.Job{"red": {&jobs.CodeWebhookJob{URL: receiver.URL, Color: "red", Message: "Service is unhealthy"}}}
	queue := &admissionQueue{}
	system := NewDurableSystem(&world, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, nil, queue, nil, time.Minute, false)
	if err := system.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	system.Initialize(&world)
	state := *system.states.Get(ent)
	dispatch := jobs.NewDispatch(&jobs.PulseHTTPJob{URL: receiver.URL}, ent, "pulse", "", 1, 0)
	dispatch.MonitorID, dispatch.Revision = state.MonitorID, state.Revision
	dispatch.Scheduled = time.Now().UTC()
	if !system.commitResults([]jobs.Result{dispatch.Execute()}) || system.lastError != nil {
		t.Fatal("check result did not commit", system.lastError)
	}
	m, ok := store.Get(state.MonitorID)
	if !ok || m.LastFailure != persistence.NewFailureDiagnostic("unexpected_http_status") {
		t.Fatal("committed state lost safe cause")
	}
	system.dispatchActions(time.Now().UTC())
	if len(queue.admitted) != 1 {
		t.Fatal("notification not admitted", len(queue.admitted))
	}
	if result := queue.admitted[0].Execute(); result.Err != nil {
		t.Fatal(result.Err)
	}
	mu.Lock()
	message := delivered
	mu.Unlock()
	if !strings.Contains(message, "unexpected_http_status") || strings.Contains(message, "private-") {
		t.Fatal("notification lost safe cause or exposed provider body", delivered)
	}
	page, err := store.History().Page(m.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range page.Events {
		if event.Type == "incident_opened" {
			found = true
			if event.Failure != m.LastFailure {
				t.Fatal("incident history lost safe cause")
			}
		}
	}
	if !found {
		t.Fatal("committed incident event not retained")
	}
}
