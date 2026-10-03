//go:build externaljobs

package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerObservationsIteratorPreservesAvailabilityAndCursor(t *testing.T) {
	var calls atomic.Int32
	captured := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/external-workers" || r.URL.Query().Get("limit") != "1" {
			t.Error("unexpected worker observation request")
		}
		cursor := "original"
		if call == 2 {
			cursor = "next"
		}
		if r.URL.Query().Get("cursor") != cursor {
			t.Error("original continuation cursor lost")
		}
		row := api.WorkerObservation{WorkerID: "worker", WorkerUID: "incarnation", SessionStatus: "future_status"}
		page := api.WorkerObservationList{GeneratedAt: captured, Items: []api.WorkerObservation{row}}
		if call == 1 {
			page.NextCursor = "next"
		}
		_ = json.NewEncoder(w).Encode(page)
	}, nil)
	iterator := client.IterateWorkers(ListOptions{Limit: 1, Cursor: "original"})
	if calls.Load() != 0 {
		t.Fatal("iterator fetched eagerly")
	}
	for n := 0; n < 2; n++ {
		if !iterator.Next(t.Context()) {
			t.Fatal("missing page", iterator.Err())
		}
		row := iterator.Value()
		if row.LastSeenAt != nil || row.CapacityAvailable || row.SessionStatus != "future_status" {
			t.Fatal("unavailable or future observations changed")
		}
		if calls.Load() != int32(n+1) {
			t.Fatal("iterator prefetched")
		}
	}
	if iterator.Next(t.Context()) || iterator.Err() != nil || calls.Load() != 2 {
		t.Fatal("iterator did not stop", iterator.Err())
	}
}

func TestWorkerObservationsCancellationAndOptionalTimestamp(t *testing.T) {
	var calls atomic.Int32
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"generatedAt":"2026-09-29T10:00:00Z","items":[{"workerID":"one","workerUID":"uid","capacity":0,"capacityAvailable":false,"active":0,"unknownActions":0,"sessionStatus":"no_session"}]}`))
	}, nil)
	page, err := client.Workers(t.Context(), ListOptions{})
	if err != nil || page.Data.Items[0].LastSeenAt != nil {
		t.Fatal("absent timestamp lost", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	iterator := client.IterateWorkers(ListOptions{})
	if iterator.Next(ctx) || !errors.Is(iterator.Err(), context.Canceled) || calls.Load() != 1 {
		t.Fatal("canceled iterator sent a request", iterator.Err())
	}
}
