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

func TestNotificationWarningOutageRecoveryDispatchKeepsProviderIdentity(t *testing.T) {
	for _, provider := range []string{"pagerduty", "opsgenie"} {
		t.Run(provider, func(t *testing.T) {
			var mu sync.Mutex
			var identities, effects []string
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"data":{"success":true,"isSuccess":true}}`))
					return
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				var key, effect string
				if provider == "pagerduty" {
					if json.Unmarshal(body["dedup_key"], &key) != nil || json.Unmarshal(body["event_action"], &effect) != nil {
						t.Error("missing provider identity or action")
						return
					}
				} else if strings.HasSuffix(r.URL.Path, "/close") {
					key = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/alerts/"), "/close")
					effect = "resolve"
					if r.URL.Query().Get("identifierType") != "alias" {
						t.Error("closure did not address the original alias")
					}
				} else {
					if json.Unmarshal(body["alias"], &key) != nil {
						t.Error("missing provider identity")
						return
					}
					effect = "trigger"
				}
				mu.Lock()
				identities = append(identities, key)
				effects = append(effects, effect)
				mu.Unlock()
				w.WriteHeader(http.StatusAccepted)
				if provider == "opsgenie" {
					_, _ = w.Write([]byte(`{"requestId":"11111111-1111-4111-8111-111111111111"}`))
				}
			}))
			defer receiver.Close()
			cfg := runtimeconfig.Default()
			cfg.Storage.Mode = "memory"
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			world := ecs.NewWorld()
			ent := releaseMonitor(t, &world, 1, 1)
			codeConfig := ecs.NewMap1[components.CodeConfig](&world).Get(ent)
			storage := ecs.NewMap1[components.JobStorage](&world).Get(ent)
			codeConfig.Configs = map[string]*components.ColorCodeConfig{}
			storage.CodeJobs = map[string][]jobs.Job{}
			for _, color := range []string{"yellow", "red", "green"} {
				codeConfig.Configs[color] = &components.ColorCodeConfig{Dispatch: true, Notify: provider, EndpointIDs: []string{"same-destination"}}
				var job jobs.Job = &jobs.CodePagerDutyJob{URL: receiver.URL, RoutingKey: "fixture", Color: color, Message: "safe summary"}
				if provider == "opsgenie" {
					job = &jobs.CodeOpsgenieJob{URL: receiver.URL + "/v2/alerts", APIKey: "fixture", Color: color, Message: "safe summary"}
				}
				storage.CodeJobs[color] = []jobs.Job{job}
			}
			queue := &admissionQueue{}
			outcomes := make(chan []jobs.Result, 4)
			system := NewDurableSystem(&world, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, nil, queue, []<-chan []jobs.Result{outcomes}, time.Minute, false)
			if err := system.Load(t.Context()); err != nil {
				t.Fatal(err)
			}
			system.Initialize(&world)
			state := *system.states.Get(ent)
			for index, outcome := range []string{"success", "failure", "success"} {
				now := time.Now().UTC()
				results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: state.MonitorID, Revision: state.Revision, At: now, Generation: uint64(index + 1), Outcome: outcome, Warning: index == 0}})
				if err != nil || len(results) != 1 || results[0].Err != nil {
					t.Fatal("observation did not commit", err, results)
				}
				monitor := *results[0].Monitor
				system.project(ent, monitor)
				system.scheduleActions(ent, monitor)
				system.dispatchActions(now)
				for _, dispatch := range queue.admitted {
					result := dispatch.Execute()
					if result.Err != nil {
						t.Fatal("provider dispatch", result.Err)
					}
					outcomes <- []jobs.Result{result}
					if !system.drain() || system.lastError != nil {
						t.Fatal("provider result did not commit", system.lastError)
					}
				}
				queue.admitted = nil
			}
			mu.Lock()
			defer mu.Unlock()
			if len(identities) < 3 || effects[len(effects)-1] != "resolve" {
				t.Fatal("warning, outage or closure never reached the receiver", effects)
			}
			for _, identity := range identities {
				if identity == "" || identity != identities[0] {
					t.Fatal("severity escalation orphaned a provider alert", effects)
				}
			}
		})
	}
}
