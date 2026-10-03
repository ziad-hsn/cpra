//go:build externaljobs

package persistence

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/slo"
)

func TestWorkerSLOContextHonorsLockDeadline(t *testing.T) {
	s := openCatalogMemory(t)
	for _, blocked := range []struct {
		name string
		lock func()
		free func()
	}{{"schedule", s.workerScheduleMu.Lock, s.workerScheduleMu.Unlock}, {"fsm", s.fsm.mu.Lock, s.fsm.mu.Unlock}} {
		t.Run(blocked.name, func(t *testing.T) {
			blocked.lock()
			var once sync.Once
			unlock := func() { once.Do(blocked.free) }
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			finished, result := make(chan struct{}), make(chan error, 1)
			t.Cleanup(func() { cancel(); unlock(); <-finished })
			go func() {
				defer close(finished)
				view, err := s.SLOViewContext(ctx, time.Now().UTC())
				if !reflect.DeepEqual(view, slo.View{}) {
					err = errors.New("cancelled worker observation published a view")
				}
				result <- err
			}()
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("worker observation lost lock deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("worker observation waited past deadline")
			}
			unlock()
			if _, err := s.SLOViewContext(t.Context(), time.Now().UTC()); err != nil {
				t.Fatal("deadline changed later observation availability", err)
			}
		})
	}
}
