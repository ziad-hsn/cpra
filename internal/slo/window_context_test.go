package slo

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestViewContextHonorsRecorderLockDeadline(t *testing.T) {
	at := time.Unix(1700000400, 0).UTC()
	r := New(at.Add(-time.Hour), 250*time.Millisecond, 5*time.Second)
	r.Expect("http", at)
	r.ChangePaused("http", 1, at)
	r.mu.Lock()
	var once sync.Once
	unlock := func() { once.Do(r.mu.Unlock) }
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	finished, result := make(chan struct{}), make(chan error, 1)
	t.Cleanup(func() { cancel(); unlock(); <-finished })
	go func() {
		defer close(finished)
		view, err := r.ViewContext(ctx, at.Add(11*time.Second), 5*time.Minute, 1)
		if !reflect.DeepEqual(view, View{}) {
			err = errors.New("cancelled reader returned observations")
		}
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("recorder lock wait lost deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SLO reader waited past deadline")
	}
	unlock()
	view, err := r.ViewContext(t.Context(), at.Add(11*time.Second), 5*time.Minute, 1)
	if err != nil || len(view.Reports) != 1 || view.Reports[0].Overdue != 1 || view.Reports[0].PausedMonitorSeconds != 11 {
		t.Fatal("cancelled read lost demand or pause exposure", view, err)
	}
}

func TestViewContextCancellationPreservesMeasurements(t *testing.T) {
	at := time.Unix(1700000400, 0).UTC()
	r := New(at.Add(-time.Hour), 250*time.Millisecond, 5*time.Second)
	r.Observe("tcp", at, at.Add(time.Millisecond), at.Add(2*time.Millisecond), at.Add(3*time.Millisecond), false, 0)
	before := r.Snapshot(at)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, input := range []context.Context{nil, ctx} {
		view, err := r.ViewContext(input, at, 5*time.Minute, 1)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(view, View{}) {
			t.Fatal("cancelled read returned a view", view, err)
		}
	}
	if after := r.Snapshot(at); !reflect.DeepEqual(before, after) {
		t.Fatal("cancelled read changed aggregate state")
	}
}
