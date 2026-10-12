package systems

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestControllerHoldsContradictoryOpsgenieReceipt(t *testing.T) {
	for _, body := range []string{`{"data":{"success":false,"success":true,"isSuccess":true}}`, `{"data":{"success":false,"SUCCESS":true,"isSuccess":true}}`} {
		t.Run(body, func(t *testing.T) {
			var posts, reads atomic.Int32
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					reads.Add(1)
					_, _ = w.Write([]byte(body))
					return
				}
				posts.Add(1)
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"requestId":"11111111-1111-4111-8111-111111111111"}`))
			}))
			defer receiver.Close()
			cfg := runtimeconfig.Default()
			cfg.Storage.Directory = t.TempDir()
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			world := ecs.NewWorld()
			ent := releaseMonitor(t, &world, 1, 1)
			ecs.NewMap1[components.CodeConfig](&world).Get(ent).Configs = map[string]*components.ColorCodeConfig{"red": {Dispatch: true, Notify: "opsgenie"}}
			ecs.NewMap1[components.JobStorage](&world).Get(ent).CodeJobs = map[string][]jobs.Job{"red": {&jobs.CodeOpsgenieJob{URL: receiver.URL, APIKey: "fixture", Color: "red"}}}
			queue := &admissionQueue{}
			system := NewDurableSystem(&world, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, nil, queue, nil, time.Minute, false)
			if err := system.Load(t.Context()); err != nil {
				t.Fatal(err)
			}
			system.Initialize(&world)
			state := *system.states.Get(ent)
			failed := jobs.Result{Ent: ent, Type: "pulse", MonitorID: state.MonitorID, Revision: state.Revision, Generation: 1, Err: jobs.ErrJobPanic}
			if !system.commitResults([]jobs.Result{failed}) {
				t.Fatal("check result was not committed", system.lastError)
			}
			system.dispatchActions(time.Now().UTC())
			if len(queue.admitted) != 1 {
				t.Fatal("notification not admitted", len(queue.admitted))
			}
			result := queue.admitted[0].Execute()
			if !system.commitResults([]jobs.Result{result}) {
				t.Fatal("delivery outcome was not committed", system.lastError)
			}
			monitor, _ := store.Get(state.MonitorID)
			found := false
			for _, action := range monitor.Actions {
				if action.Kind == "code" {
					found = true
					if action.State != persistence.Unknown {
						t.Fatal("contradictory receipt did not preserve unknown outcome", action.State)
					}
				}
			}
			queue.admitted = nil
			system.dispatchActions(time.Now().UTC().Add(time.Minute))
			if !found || posts.Load() != 1 || reads.Load() != 1 || len(queue.admitted) != 0 {
				t.Fatal("unknown effect was repeated", posts.Load(), reads.Load(), len(queue.admitted))
			}
			if err := store.Snapshot(); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			restored, _ := store.Get(state.MonitorID)
			for _, action := range restored.Actions {
				if action.Kind == "code" && action.State != persistence.Unknown {
					t.Fatal("restart lost unknown receipt outcome")
				}
			}
		})
	}
}
