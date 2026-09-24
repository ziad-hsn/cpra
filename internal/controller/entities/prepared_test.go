package entities

import (
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

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func testMonitor() manifest.Monitor {
	return manifest.Monitor{ID: "stable-api", Name: "api", Enabled: true, Pulse: manifest.Pulse{
		Type: "http", Interval: time.Minute, Timeout: time.Second,
		Config: &manifest.PulseHTTPConfig{Url: "http://127.0.0.1:1", Headers: map[string]string{"X-Fixture": "original"}, ExpectedStatus: []int{200}},
	}}
}
func prepareTestMonitor(t *testing.T, m manifest.Monitor) *PreparedMonitor {
	t.Helper()
	p, err := PrepareMonitor(m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestPreparePreservesAbsentAndEmptyNotificationFingerprints(t *testing.T) {
	for _, codes := range []manifest.Codes{nil, {}} {
		m := testMonitor()
		m.Codes = codes
		want, err := manifest.ConfigurationRevision(m, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		p := prepareTestMonitor(t, m)
		if p.Revision() != want {
			t.Fatal("preparation changed existing execution identity")
		}
	}
}
func monitorCount(w *ecs.World) int {
	query := ecs.NewFilter1[components.MonitorState](w).Query()
	count := 0
	for query.Next() {
		count++
	}
	return count
}

func TestPreparationFailureDoesNotAllocateOrReserveIdentity(t *testing.T) {
	cases := []struct {
		name   string
		change func(*manifest.Monitor)
	}{
		{"nil pulse", func(m *manifest.Monitor) { m.Pulse.Config = nil }},
		{"typed nil pulse", func(m *manifest.Monitor) { m.Pulse.Config = (*manifest.PulseHTTPConfig)(nil) }},
		{"bad maintenance", func(m *manifest.Monitor) { m.Maintenance = []manifest.MaintenanceWindow{{Cron: "bad", Duration: "1m"}} }},
		{"wrong recovery target", func(m *manifest.Monitor) {
			m.Intervention = manifest.Intervention{Action: "docker", Target: &manifest.InterventionTargetWebhook{URL: "http://127.0.0.1:1"}}
		}},
		{"late notification failure", func(m *manifest.Monitor) {
			m.Codes = manifest.Codes{"green": {Notify: "log", Config: &manifest.CodeNotificationLog{File: "unused"}}, "red": {Notify: "slack", Config: &manifest.CodeNotificationSlack{}}}
		}},
		{"missing group", func(m *manifest.Monitor) { m.Codes = manifest.Codes{"red": {Dispatch: true, NotifyGroup: "absent"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := ecs.NewWorld()
			manager := NewEntityManager(&w)
			bad := testMonitor()
			tc.change(&bad)
			if err := manager.CreateEntityFromMonitor(&bad, &w); err == nil {
				t.Fatal("invalid monitor accepted")
			}
			if monitorCount(&w) != 0 || len(manager.identities) != 0 {
				t.Fatal("preparation failure changed world or identity map")
			}
			good := testMonitor()
			if err := manager.CreateEntityFromMonitor(&good, &w); err != nil {
				t.Fatalf("failed preparation reserved valid ID: %v", err)
			}
		})
	}
}

func TestPrepareFreezesInputsWithoutProviderExecution(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	m := testMonitor()
	m.Pulse.Config.(*manifest.PulseHTTPConfig).Url = target.URL
	m.Codes = manifest.Codes{"red": {Dispatch: true, NotifyGroup: "oncall"}}
	endpoint := &manifest.CodeNotificationWebhook{URL: target.URL, Headers: map[string]string{"Authorization": "private-fixture-marker"}}
	endpoints := map[string]manifest.Endpoint{"hook": {Type: "webhook", Config: endpoint}}
	groups := manifest.NotificationGroups{"oncall": {"hook"}}
	p, err := PrepareMonitor(m, endpoints, groups)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	oldRevision := p.Revision()
	m.Name = "changed"
	m.Pulse.Config.(*manifest.PulseHTTPConfig).Headers["X-Fixture"] = "changed"
	m.Pulse.Config.(*manifest.PulseHTTPConfig).ExpectedStatus[0] = 503
	endpoint.Headers["Authorization"] = "changed"
	groups["oncall"][0] = "missing"
	delete(m.Codes, "red")
	w := ecs.NewWorld()
	manager := NewEntityManager(&w)
	entity, err := manager.Install(p, &w)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("preparation or installation executed a provider")
	}
	pulse := manager.PulseConfig.Get(entity).Config.(*manifest.PulseHTTPConfig)
	if pulse.Headers["X-Fixture"] != "original" || pulse.ExpectedStatus[0] != 200 || manager.MonitorState.Get(entity).Name != "api" || manager.MonitorState.Get(entity).Revision != oldRevision {
		t.Fatal("source mutation changed frozen configuration")
	}
	storage := manager.JobStorage.Get(entity)
	if len(storage.CodeJobs["red"]) != 1 {
		t.Fatal("source group mutation changed destinations")
	}
	job := storage.CodeJobs["red"][0].(*entityJob).Job.(*jobs.CodeWebhookJob)
	if job.Headers["Authorization"] != "private-fixture-marker" {
		t.Fatal("endpoint input was not cloned")
	}
	if _, err := json.Marshal(p); err == nil {
		t.Fatal("private preparation serializable")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", p, p), "private-fixture-marker") {
		t.Fatal("private data leaked in diagnostics")
	}
	if result := storage.PulseJob.Copy().Execute(); result.Err != nil || result.Ent != entity {
		t.Fatalf("legacy direct job has wrong identity: %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one explicit execution, got %d", requests.Load())
	}
}

func TestInstallOwnershipWorldLocksAndDuplicateIDs(t *testing.T) {
	w := ecs.NewWorld()
	other := ecs.NewWorld()
	manager := NewEntityManager(&w)
	p := prepareTestMonitor(t, testMonitor())
	if _, err := manager.Install(p, &other); err == nil {
		t.Fatal("foreign world accepted")
	}
	query := ecs.NewFilter1[components.MonitorState](&w).Query()
	if _, err := manager.Install(p, &w); err == nil {
		t.Fatal("locked world accepted")
	}
	query.Close()
	entity, err := manager.Install(p, &w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(p, &w); err == nil {
		t.Fatal("preparation reused")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if manager.JobStorage.Get(entity).PulseJob == nil {
		t.Fatal("close removed transferred jobs")
	}
	duplicate := prepareTestMonitor(t, testMonitor())
	if _, err := manager.Install(duplicate, &w); err == nil {
		t.Fatal("duplicate stable identity accepted")
	}
	if monitorCount(&w) != 1 {
		t.Fatal("rejected installation changed world")
	}
	closed := prepareTestMonitor(t, manifest.Monitor{ID: "other", Name: "other", Pulse: testMonitor().Pulse})
	_ = closed.Close()
	if _, err := manager.Install(closed, &w); err == nil {
		t.Fatal("closed preparation accepted")
	}
}

func TestReplacePreservesIncarnationStateAndControl(t *testing.T) {
	w := ecs.NewWorld()
	manager := NewEntityManager(&w)
	m := testMonitor()
	m.Enabled = false
	m.Codes = manifest.Codes{"red": {Dispatch: true, Notify: "log", Config: &manifest.CodeNotificationLog{File: "unused"}}}
	p := prepareTestMonitor(t, m)
	entity, err := manager.Install(p, &w)
	if err != nil {
		t.Fatal(err)
	}
	state := manager.MonitorState.Get(entity)
	state.Flags = components.StateIncidentOpen | components.StatePulsePending
	state.PulseGeneration, state.InterventionGeneration, state.CodeSequence = 91, 32, 7
	state.ConsecutiveFailures, state.VerifyRemaining = 3, 2
	state.LastCheckTime, state.NextCheckTime = time.Unix(100, 0), time.Unix(200, 0)
	state.PendingAlerts = []components.AlertRequest{{Color: "red", Attempts: 2}}
	state.Deliveries = map[string]*components.CodeDelivery{"red": {Generation: 7, Completed: []bool{true, false}}}
	before := *state
	code := manager.CodeStatus.Get(entity).Status["red"]
	code.ConsecutiveFailures, code.NotBefore = 2, time.Unix(300, 0)
	m.Name, m.Enabled = "renamed", true
	m.Pulse.Interval = 2 * time.Minute
	m.Intervention = manifest.Intervention{Action: "docker", Target: &manifest.InterventionTargetDocker{Container: "fixture"}}
	m.Codes["yellow"] = manifest.CodeConfig{Dispatch: false}
	next := prepareTestMonitor(t, m)
	if err := manager.Replace(entity, next, &w); err != nil {
		t.Fatal(err)
	}
	got := manager.MonitorState.Get(entity)
	if current, ok := manager.Lookup(m.ID); !ok || current != entity {
		t.Fatal("replacement changed live incarnation")
	}
	if got.Name != "renamed" || got.Revision != next.Revision() || manager.PulseConfig.Get(entity).Interval != 2*time.Minute {
		t.Fatal("configuration replacement missing")
	}
	want := before
	want.Name, want.Revision, want.Maintenance = got.Name, got.Revision, got.Maintenance
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("replacement reset lifecycle state: got %+v want %+v", *got, want)
	}
	if !manager.Disabled.HasAll(entity) {
		t.Fatal("replacement overrode durable disabled control")
	}
	if status := manager.CodeStatus.Get(entity).Status["red"]; status.ConsecutiveFailures != 2 || !status.NotBefore.Equal(time.Unix(300, 0)) {
		t.Fatal("replacement reset retained color status")
	}
	if !manager.InterventionConfig.HasAll(entity) || manager.JobStorage.Get(entity).InterventionJob == nil {
		t.Fatal("new optional component not installed")
	}
	if len(manager.JobStorage.Get(entity).CodeJobs["yellow"]) != 0 {
		t.Fatal("inert rule received executable job")
	}
	if err := manager.Replace(entity, next, &w); err == nil {
		t.Fatal("replacement reused preparation")
	}
	m.Codes = nil
	m.Intervention = manifest.Intervention{}
	if err := manager.Replace(entity, prepareTestMonitor(t, m), &w); err != nil {
		t.Fatal(err)
	}
	if manager.CodeConfig.HasAll(entity) || manager.CodeStatus.HasAll(entity) || manager.InterventionConfig.HasAll(entity) {
		t.Fatal("removed optional configuration retained")
	}
	if !reflect.DeepEqual(manager.MonitorState.Get(entity).Deliveries, before.Deliveries) {
		t.Fatal("archetype moves lost lifecycle data")
	}
}

func TestRemoveRecreateFencesStaleEntityAndReplacement(t *testing.T) {
	w := ecs.NewWorld()
	manager := NewEntityManager(&w)
	p := prepareTestMonitor(t, testMonitor())
	old, err := manager.Install(p, &w)
	if err != nil {
		t.Fatal(err)
	}
	wrong := testMonitor()
	wrong.ID = "different"
	if err := manager.Replace(old, prepareTestMonitor(t, wrong), &w); err == nil {
		t.Fatal("replacement changed stable identity")
	}
	query := ecs.NewFilter1[components.MonitorState](&w).Query()
	if err := manager.Remove(old, &w); err == nil {
		t.Fatal("removed entity with query open")
	}
	if err := manager.Replace(old, prepareTestMonitor(t, testMonitor()), &w); err == nil {
		t.Fatal("replaced entity with query open")
	}
	query.Close()
	if err := manager.Remove(old, &w); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Lookup("stable-api"); ok {
		t.Fatal("removal retained stable ID")
	}
	current, err := manager.Install(prepareTestMonitor(t, testMonitor()), &w)
	if err != nil {
		t.Fatal(err)
	}
	if current == old || w.Alive(old) {
		t.Fatal("recreated monitor reused full entity incarnation")
	}
	if err := manager.Remove(old, &w); err == nil {
		t.Fatal("stale removal accepted")
	}
	if err := manager.Replace(old, prepareTestMonitor(t, testMonitor()), &w); err == nil {
		t.Fatal("stale replacement accepted")
	}
	if got, ok := manager.Lookup("stable-api"); !ok || got != current || monitorCount(&w) != 1 {
		t.Fatal("stale mutation affected replacement")
	}
}

func TestPreparedOwnershipConcurrentCloseAndInstall(t *testing.T) {
	// Only one goroutine mutates Ark; Close has no world access and competes for
	// ownership of the preparation, never for its transferred component memory.
	for range 20 {
		w := ecs.NewWorld()
		manager := NewEntityManager(&w)
		p := prepareTestMonitor(t, testMonitor())
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Close() }()
		entity, err := manager.Install(p, &w)
		wg.Wait()
		if err == nil && (!w.Alive(entity) || manager.JobStorage.Get(entity).PulseJob == nil) {
			t.Fatal("close invalidated transfer")
		}
	}
}

func TestEntityJobCopyContextAndDispatchIdentity(t *testing.T) {
	w := ecs.NewWorld()
	manager := NewEntityManager(&w)
	p := prepareTestMonitor(t, testMonitor())
	entity, err := manager.Install(p, &w)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	template := manager.JobStorage.Get(entity).PulseJob
	copyJob := template.Copy()
	copyJob.(interface{ SetContext(context.Context) }).SetContext(ctx)
	if result := copyJob.Execute(); result.Err == nil || result.Ent != entity {
		t.Fatalf("copy lost context or identity: %+v", result)
	}
	underlying := template.(*entityJob).Job.(*jobs.PulseHTTPJob)
	if underlying.Context().Err() != nil {
		t.Fatal("copy changed template context")
	}
	dispatch := jobs.NewDispatch(template, entity, "pulse", "", 44, 2)
	dispatch.MonitorID, dispatch.Revision = "stable-api", "new-revision"
	dispatch.SetContext(ctx)
	result := dispatch.Execute()
	if !errors.Is(result.Err, context.Canceled) || result.Ent != entity || result.Generation != 44 || result.Revision != "new-revision" || result.ID != dispatch.ID {
		t.Fatalf("dispatch correlation changed: %+v", result)
	}
}
