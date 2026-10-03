package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestOperationPageHonorsLockDeadline(t *testing.T) {
	config := testConfig(t)
	config.Storage.Mode = "memory"
	s, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	view, err := s.OperationSnapshot(time.Now().UTC(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, blocked := range []struct {
		name string
		mu   *sync.RWMutex
	}{{"store", &s.mu}, {"fsm", &s.fsm.mu}, {"history", &s.fsm.history.mu}} {
		t.Run(blocked.name, func(t *testing.T) {
			blocked.mu.Lock()
			var once sync.Once
			unlock := func() { once.Do(blocked.mu.Unlock) }
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			finished, result := make(chan struct{}), make(chan error, 1)
			t.Cleanup(func() { cancel(); unlock(); <-finished })
			go func() {
				defer close(finished)
				rows, next, err := view.Page(ctx, "", "", 100)
				if len(rows) != 0 || next != "" {
					err = errors.New("cancelled read published a page")
				}
				result <- err
			}()
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("operation lock wait lost deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation reader waited past deadline")
			}
			unlock()
			if _, _, err := view.Page(t.Context(), "", "", 100); err != nil {
				t.Fatal("deadline invalidated a reusable view", err)
			}
		})
	}
}

func TestOperationSnapshotContextHonorsLockDeadline(t *testing.T) {
	s := openCatalogMemory(t)
	for _, blocked := range []struct {
		name string
		mu   *sync.RWMutex
	}{{"store", &s.mu}, {"fsm", &s.fsm.mu}, {"history", &s.fsm.history.mu}} {
		t.Run(blocked.name, func(t *testing.T) {
			blocked.mu.Lock()
			var once sync.Once
			unlock := func() { once.Do(blocked.mu.Unlock) }
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			finished, result := make(chan struct{}), make(chan error, 1)
			t.Cleanup(func() { cancel(); unlock(); <-finished })
			go func() {
				defer close(finished)
				view, err := s.OperationSnapshotContext(ctx, time.Now().UTC(), 1<<20)
				if view.store != nil {
					err = errors.New("cancelled snapshot returned a view")
				}
				result <- err
			}()
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("snapshot lost lock deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("snapshot waited past deadline")
			}
		})
	}
}
