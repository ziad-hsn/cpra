package queue

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStatsTimesAreUTCAndAbsentBeforeObservation(t *testing.T) {
	// Compare location identities without changing time.Local: background pool
	// timers also read it, and Local and UTC are distinct even in a UTC zone.
	for _, kind := range []string{"adaptive", "workiva", "hybrid", "fair"} {
		t.Run(kind, func(t *testing.T) {
			var q Queue
			if kind == "fair" {
				f, err := NewFairQueue(8, "http")
				if err != nil {
					t.Fatal(err)
				}
				q = f
				t.Cleanup(q.Close)
			} else {
				q = pendingQueue(t, kind)
			}
			before := q.Stats()
			data, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			if !before.LastEnqueue.IsZero() || !before.LastDequeue.IsZero() || strings.Contains(string(data), "last_enqueue") || strings.Contains(string(data), "last_dequeue") {
				t.Fatal("unused queue fabricated time", string(data))
			}
			if err := q.Enqueue(fairTestJob("http", 1)); err != nil {
				t.Fatal(err)
			}
			if _, err := q.Dequeue(); err != nil {
				t.Fatal(err)
			}
			after := q.Stats()
			for _, at := range []time.Time{after.LastEnqueue, after.LastDequeue} {
				if at.IsZero() || at.Location() != time.UTC {
					t.Fatal("event is not UTC", at)
				}
			}
		})
	}
	q, _ := NewFairQueue(8, "http")
	defer q.Close()
	p, err := NewDynamicWorkerPool(q, WorkerPoolConfig{MinWorkers: 1, MaxWorkers: 4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.DrainAndStop()
	p.lastScaleTime.Store(0)
	data, _ := json.Marshal(p.Stats())
	if strings.Contains(string(data), "last_scale_time") {
		t.Fatal("unused scale time exposed")
	}
	p.lastScaleTime.Store(time.Now().UnixNano())
	if p.Stats().LastScaleTime.Location() != time.UTC {
		t.Fatal("scale time is not UTC")
	}
}
