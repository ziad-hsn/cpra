package httpserver

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/queue"
)

func TestObservationPageHonorsCursorLockDeadline(t *testing.T) {
	m := &managementHTTP{cursorReady: true, snapshots: make(map[string]managementSnapshot), now: time.Now}
	m.mu.Lock()
	var once sync.Once
	unlock := func() { once.Do(m.mu.Unlock) }
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	finished, result := make(chan struct{}), make(chan error, 1)
	t.Cleanup(func() { cancel(); unlock(); <-finished })
	go func() {
		defer close(finished)
		_, err := (&Server{}).observationPage(ctx, m, url.Values{}, "System", "reader", 1)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("observation cursor lost deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("observation cursor lock blocked past deadline")
	}
	unlock()
	if len(m.snapshots) != 0 {
		t.Fatal("cancelled observation retained a snapshot")
	}
	if _, err := (&Server{}).observationPage(t.Context(), m, url.Values{}, "System", "reader", 1); err != nil {
		t.Fatal("deadline prevented subsequent observation", err)
	}
}

type blockedObservationQueue struct {
	queue.Queue
	entered chan struct{}
	release chan struct{}
}

func (q *blockedObservationQueue) Stats() queue.Stats {
	close(q.entered)
	<-q.release
	return queue.Stats{}
}

func TestObservationCaptureDoesNotHoldCursorMetadataLock(t *testing.T) {
	q := &blockedObservationQueue{entered: make(chan struct{}), release: make(chan struct{})}
	s := &Server{pulseQueue: q}
	m := &managementHTTP{cursorReady: true, snapshots: make(map[string]managementSnapshot), now: time.Now}
	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	release := func() { once.Do(func() { close(q.release) }) }
	finished, result := make(chan struct{}), make(chan error, 1)
	t.Cleanup(func() { cancel(); release(); <-finished })
	go func() {
		defer close(finished)
		_, err := s.observationPage(ctx, m, url.Values{}, "Queue", "reader", 1)
		result <- err
	}()
	select {
	case <-q.entered:
	case <-time.After(time.Second):
		t.Fatal("observation capture did not start")
	}
	if !m.mu.TryLock() {
		t.Fatal("slow stats capture blocked cursor metadata and cleanup")
	}
	m.mu.Unlock()
	cancel()
	release()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled capture published observations", err)
	}
	if len(m.snapshots) != 0 {
		t.Fatal("cancelled capture retained a cursor")
	}
}
