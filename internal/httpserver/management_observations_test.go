package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/controller"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/queue"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Every operation except Stats would panic through the nil embedded interface:
// observation requests must never dequeue, execute, or enqueue provider jobs.
type observationQueue struct {
	queue.Queue
	value queue.Stats
}

func (q *observationQueue) Stats() queue.Stats { return q.value }

func TestManagementObservationSDKContractsAndSourceUnits(t *testing.T) {
	f := newManagementFixture(t, true)
	q := &observationQueue{value: queue.Stats{Capacity: 10, QueueDepth: 10, EnqueueRate: 4, DequeueRate: 3, Dropped: 2, Dequeued: 1, SampleWindow: time.Second, AvgQueueTime: 1500 * time.Microsecond, MaxQueueTime: 2 * time.Millisecond}}
	f.server.pulseQueue, f.server.intervQueue, f.server.codeQueue = q, q, q
	pool, err := queue.NewDynamicWorkerPool(q, queue.WorkerPoolConfig{MinWorkers: 2, MaxWorkers: 8, NumShards: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.DrainAndStop)
	f.server.pulsePool, f.server.intervPool, f.server.codePool = pool, pool, pool
	f.server.metrics = controller.NewMetricsAggregator()
	f.server.metrics.RecordSystemUpdate("durable", 1500*time.Microsecond, 25, 1)
	now := time.Now().UTC()
	f.server.holder.Set(&fleetview.StatsSnapshot{Generated: now.Add(-time.Minute), Total: 1000000, ByStatus: map[string]int{"down": 1000000}})
	f.server.cfg.Store.SLO().Expect("http", now.Add(-2*time.Second))
	f.server.cfg.Store.SLO().Observe("http", now.Add(-2*time.Second), now.Add(-2*time.Second), now.Add(-time.Second), now, false, 1)
	f.server.cfg.Store.SLO().ChangePaused("http", 1, now.Add(-time.Second))
	f.server.cfg.Store.SLO().ChangePaused("http", 1, now.Add(-time.Second))
	state, err := f.sdk.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Data.Ready || !state.Data.Live || !state.Data.Storage.Available || state.Data.ProjectionFresh || !state.Data.ProjectionAvailable || state.Data.ProjectionAgeMS < 60000 {
		t.Fatal("readiness conflated target health/projection freshness", state.Data)
	}
	if state.Data.UnknownActionsAvailable || state.Data.Storage.BytesAvailable {
		t.Fatal("unmeasured aggregate fabricated")
	}
	queues, err := f.sdk.Queues.List(context.Background(), cpra.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range queues.Data.Items {
		if !item.Available || !item.Saturated || item.Utilization.Value != 1 || item.AverageWait.Value != 1.5 || item.DequeuedPerSecond != 3 || item.EnqueuedPerSecond != 4 || item.CompletedRateAvailable || item.OldestPendingMS.Available {
			t.Fatal("queue mapping", item)
		}
	}
	pools, err := f.sdk.Pools.List(context.Background(), cpra.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pools.Data.Items {
		if !item.Available || item.Capacity != 2 || item.Minimum != 2 || item.Maximum != 8 || item.Workers != 0 || item.BusyAvailable || item.ModelRecommendationAvailable || item.ServiceTime.Available || item.LastScaleAt != nil {
			t.Fatal("pool source meaning lost", item)
		}
	}
	for _, call := range []func() error{
		func() error { _, e := f.sdk.Metrics(context.Background()); return e },
		func() error { _, e := f.sdk.Ready(context.Background()); return e },
		func() error { _, e := f.sdk.Live(context.Background()); return e },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	systems, err := f.sdk.Systems.List(context.Background(), cpra.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !systems.Data.Available || len(systems.Data.Items) != 1 || systems.Data.Items[0].AverageUpdateDuration.Value != 1.5 || systems.Data.Items[0].LastUpdateDurationMS.Available || systems.Data.Items[0].ProgressAt == nil {
		t.Fatal("system observations", systems.Data)
	}
	config, err := f.sdk.RuntimeConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !config.Data.Available || config.Data.ReadOnly || config.Data.StorageMode != "memory" || config.Data.HistoryRetention != "720h0m0s" || config.Data.QueueTarget != "250ms" {
		t.Fatal("runtime config", config.Data)
	}
	view, err := f.sdk.SLO.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Data.Available || len(view.Data.Reports) != 1 || !view.Data.CoverageGap {
		t.Fatal("SLO coverage", view.Data)
	}
	r := view.Data.Reports[0]
	if r.Pipeline != "pulse" || r.Expected != 2 || r.Samples != 1 || r.Missed != 1 || r.ResultAttainment.Value != 0.5 || r.QueueDelay.P99MS != 0 || !r.QueueDelay.Available || r.PausedMonitors != 2 || r.PausedMonitorSeconds < 2 {
		t.Fatal("SLO source meaning lost", r)
	}
	response, raw := f.request(t, "GET", "/api/v2/slo", managementReaderToken, nil, nil)
	if response.StatusCode != 200 || !bytes.Contains(raw, []byte(`"p99Ms":0`)) || !bytes.Contains(raw, []byte(`"timeouts":0`)) {
		t.Fatal("known zero lost on wire", string(raw))
	}
	var prom bytes.Buffer
	if err := f.sdk.Prometheus(context.Background(), &prom); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"# TYPE cpra_slo_window_paused_monitor_seconds gauge", "# TYPE cpra_slo_window_samples gauge", `cpra_slo_paused_monitors{pipeline="pulse",driver="http"} 2`, `cpra_slo_result_attainment_ratio{pipeline="pulse",driver="http"} 0.5`} {
		if !strings.Contains(prom.String(), expected) {
			t.Fatal("missing measured Prometheus family", expected)
		}
	}
}

func TestManagementObservationPaginationAndAuthorization(t *testing.T) {
	f := newManagementFixture(t, true)
	capabilities, err := f.sdk.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	access, err := f.sdk.Access(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range observationRoutes {
		if !slices.Contains(capabilities.Data.ResourceOperations[route.kind], route.operation) || !slices.Contains(access.Data.Permissions, route.operation) {
			t.Fatal("observation missing from discovery or authorized operations", route.operation)
		}
		response, _ := f.request(t, "GET", route.path, "", nil, nil)
		if response.StatusCode != 401 {
			t.Fatalf("%s anonymous=%d", route.operation, response.StatusCode)
		}
		response, _ = f.request(t, "GET", route.path, managementReaderToken, nil, map[string]string{"Origin": "https://hostile.example"})
		if response.StatusCode != 403 {
			t.Fatal("origin bypass", route.operation)
		}
	}
	q := &observationQueue{value: queue.Stats{Capacity: 10, QueueDepth: 1}}
	f.server.codeQueue, f.server.intervQueue, f.server.pulseQueue = q, q, q
	first, err := f.sdk.Queues.List(context.Background(), cpra.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	q.value.QueueDepth = 9
	second, err := f.sdk.Queues.List(context.Background(), cpra.ListOptions{Limit: 1, Cursor: first.Data.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Data.Items) != 1 || second.Data.Items[0].Depth != 1 || second.Data.Items[0].Name == first.Data.Items[0].Name {
		t.Fatal("pagination did not freeze source", second.Data)
	}
	for _, path := range []string{"/api/v2/pools?cursor=" + url.QueryEscape(first.Data.NextCursor), "/api/v2/queues?cursor=" + url.QueryEscape(first.Data.NextCursor) + "&limit=2"} {
		response, _ := f.request(t, "GET", path, managementOperatorToken, nil, nil)
		if response.StatusCode < 400 {
			t.Fatal("cursor binding bypass")
		}
	}
	response, _ := f.request(t, "GET", "/api/v2/queues?cursor="+url.QueryEscape(first.Data.NextCursor), managementReaderToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("principal leaked cursor", response.StatusCode)
	}
	for _, query := range []string{"monitorID=m", "limit=501", "limit=1&limit=1", "unexpected=1", "selector=x"} {
		response, _ := f.request(t, "GET", "/api/v2/queues?"+query, managementReaderToken, nil, nil)
		if response.StatusCode < 400 {
			t.Fatal("unsupported query silently ignored", query)
		}
	}
	if err := f.server.StopAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sdk.Ready(context.Background()); err == nil {
		t.Fatal("draining readiness available")
	}
	if live, err := f.sdk.Live(context.Background()); err != nil || !live.Data.Available {
		t.Fatal("draining liveness unavailable", err)
	}
	if _, err := f.sdk.State(context.Background()); err != nil {
		t.Fatal("drain diagnostics unavailable", err)
	}
}

func TestManagementUnavailableAndBoundedObservations(t *testing.T) {
	f := newManagementFixture(t, true)
	f.server.metrics = controller.NewMetricsAggregator()
	for i := 0; i < 257; i++ {
		f.server.metrics.RegisterSystem(fmt.Sprintf("system-%03d", i))
	}
	result, err := f.sdk.Systems.List(context.Background(), cpra.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Data.Available || len(result.Data.Items) != 0 {
		t.Fatal("truncated systems presented as complete")
	}
	response, raw := f.request(t, "GET", "/api/v2/pools", managementReaderToken, nil, nil)
	var pools api.PoolList
	if response.StatusCode != 200 || api.DecodeResponse(raw, &pools) != nil || len(pools.Items) != 3 {
		t.Fatal("missing pool contract", string(raw))
	}
	for _, item := range pools.Items {
		if item.Available || item.BusyAvailable || item.ModelRecommendationAvailable || item.ServiceTime.Available {
			t.Fatal("missing measurements fabricated", item)
		}
	}
	f.server.cfg.Store.MarkUnavailable(fmt.Errorf("private-backend-credential-marker"))
	state, err := f.sdk.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Data.Ready || state.Data.Storage.Available || !state.Data.Live {
		t.Fatal("failed storage health", state.Data)
	}
	encoded, _ := json.Marshal(state.Data)
	if bytes.Contains(encoded, []byte("private-backend")) {
		t.Fatal("storage diagnostics leaked private error")
	}
}

func TestManagementObservationReadinessAgreesWithLegacy(t *testing.T) {
	f := newManagementFixture(t, true)
	for _, tc := range []struct {
		name       string
		generated  time.Time
		total      int
		allowEmpty bool
		ownerReady func() bool
		want       int
	}{
		{"uninitialized", time.Time{}, 0, false, nil, 503},
		{"future_projection", time.Now().Add(time.Hour), 1, false, nil, 503},
		{"empty_not_allowed", time.Now(), 0, false, nil, 503},
		{"explicit_empty", time.Now(), 0, true, nil, 200},
		{"owner_progress_with_stale_projection", time.Now().Add(-time.Hour), 1000000, false, func() bool { return true }, 200},
		{"owner_stalled_with_fresh_projection", time.Now(), 1000000, false, func() bool { return false }, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.server.cfg.Ready, f.server.cfg.AllowEmpty = tc.ownerReady, tc.allowEmpty
			f.server.holder.Set(&fleetview.StatsSnapshot{Generated: tc.generated, Total: tc.total})
			for _, path := range []string{"/api/v1/readyz", "/api/v2/readyz"} {
				response, _ := f.request(t, "GET", path, managementReaderToken, nil, nil)
				if response.StatusCode != tc.want {
					t.Fatalf("%s: got %d, want %d", path, response.StatusCode, tc.want)
				}
			}
		})
	}
}
