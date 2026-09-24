package httpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/queue"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

func TestManagementOldestPendingAgeAvailabilityAndUnits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stats     queue.Stats
		available bool
		value     float64
		reason    string
	}{
		{"measured", queue.Stats{OldestPendingAvailable: true, OldestPendingAge: 1500 * time.Microsecond}, true, 1.5, ""},
		{"measured_zero", queue.Stats{OldestPendingAvailable: true}, true, 0, ""},
		{"empty", queue.Stats{OldestPendingReason: "queue_empty"}, false, 0, "queue_empty"},
		{"publishing", queue.Stats{OldestPendingReason: "admission_in_progress"}, false, 0, "admission_in_progress"},
		{"unsupported", queue.Stats{}, false, 0, "oldest_pending_age_not_recorded"},
		{"invalid", queue.Stats{OldestPendingAvailable: true, OldestPendingAge: -time.Second}, false, 0, "invalid_observation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := queueObservation("pulse", &observationQueue{value: tc.stats}).OldestPendingMS
			if view.Available != tc.available || view.Value != tc.value || view.Reason != tc.reason {
				t.Fatal("age availability or units changed", view)
			}
		})
	}
	if value := queueObservation("pulse", nil).OldestPendingMS; value.Available || value.Reason != "source_unavailable" {
		t.Fatal("missing queue fabricated age", value)
	}
	f := newManagementFixture(t, true)
	f.server.pulseQueue = &observationQueue{value: queue.Stats{QueueDepth: 1, Capacity: 2, OldestPendingAvailable: true, OldestPendingAge: 1500 * time.Microsecond}}
	page, err := f.sdk.Queues.List(context.Background(), cpra.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Data.Items {
		if item.Name == "pulse" {
			found = true
			if !item.OldestPendingMS.Available || item.OldestPendingMS.Value != 1.5 {
				t.Fatal("v2 wire age units", item)
			}
		}
	}
	if !found {
		t.Fatal("pulse observation missing")
	}
	response, raw := f.request(t, "GET", "/api/v1/queues", managementReaderToken, nil, nil)
	var legacy map[string]queue.Stats
	if response.StatusCode != 200 || json.Unmarshal(raw, &legacy) != nil || !legacy["pulse"].OldestPendingAvailable || legacy["pulse"].OldestPendingAge != 1500*time.Microsecond {
		t.Fatal("v1 age availability/units lost", response.StatusCode)
	}
}

func TestManagementOldestPendingAgeUsesRealPendingQueue(t *testing.T) {
	q, err := queue.NewHybridQueue(queue.HybridQueueConfig{RingCapacity: 2, OverflowCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if value := queueObservation("pulse", q).OldestPendingMS; value.Available || value.Reason != "queue_empty" {
		t.Fatal("empty queue claimed age", value)
	}
	// Only queue operations run; this job is never executed and no target is contacted.
	if err := q.Enqueue(&jobs.PulseTCPJob{}); err != nil {
		t.Fatal(err)
	}
	if value := queueObservation("pulse", q).OldestPendingMS; !value.Available || value.Value < 0 {
		t.Fatal("real pending job missing age", value)
	}
	if _, err := q.Dequeue(); err != nil {
		t.Fatal(err)
	}
	if value := queueObservation("pulse", q).OldestPendingMS; value.Available || value.Reason != "queue_empty" {
		t.Fatal("dequeued job counted as pending", value)
	}
}
